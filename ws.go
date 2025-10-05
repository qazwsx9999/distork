package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 45 * time.Second
	wsMaxMessage = 64 * 1024
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow same-origin cookies; adjust if you introduce cross-origin usage.
		return true
	},
}

type wsHub struct {
	mu          sync.RWMutex
	channelSubs map[int64]map[*wsClient]struct{}
}

type voiceState struct {
	mu           sync.RWMutex
	participants map[string]*wsClient
}

type voiceParticipant struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type voiceSignal struct {
	From        string          `json:"from"`
	Email       string          `json:"email"`
	DisplayName string          `json:"displayName"`
	Payload     json.RawMessage `json:"payload"`
}

type wsClient struct {
	id            string
	state         *serverState
	hub           *wsHub
	conn          *websocket.Conn
	send          chan []byte
	user          user
	subscriptions map[int64]struct{}
	mu            sync.Mutex
	closeOnce     sync.Once

	voiceJoined bool
	voiceID     string
}

type wsInbound struct {
	Type      string          `json:"type"`
	ChannelID int64           `json:"channelId,omitempty"`
	Content   string          `json:"content,omitempty"`
	Target    string          `json:"target,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type wsOutbound struct {
	Type         string             `json:"type"`
	ChannelID    int64              `json:"channelId,omitempty"`
	Message      *messageDTO        `json:"message,omitempty"`
	Error        string             `json:"error,omitempty"`
	Code         string             `json:"code,omitempty"`
	Participants []voiceParticipant `json:"participants,omitempty"`
	Self         *voiceParticipant  `json:"self,omitempty"`
	Peer         *voiceParticipant  `json:"peer,omitempty"`
	Signal       *voiceSignal       `json:"signal,omitempty"`
}

func newWSHub() *wsHub {
	return &wsHub{
		channelSubs: make(map[int64]map[*wsClient]struct{}),
	}
}

func newVoiceState() *voiceState {
	return &voiceState{
		participants: make(map[string]*wsClient),
	}
}

func (h *wsHub) subscribe(client *wsClient, channelID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.channelSubs[channelID]
	if subs == nil {
		subs = make(map[*wsClient]struct{})
		h.channelSubs[channelID] = subs
	}
	subs[client] = struct{}{}
}

func (h *wsHub) unsubscribe(client *wsClient, channelID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.channelSubs[channelID]; ok {
		delete(subs, client)
		if len(subs) == 0 {
			delete(h.channelSubs, channelID)
		}
	}
}

func (h *wsHub) removeClient(client *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channelID, subs := range h.channelSubs {
		if _, ok := subs[client]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.channelSubs, channelID)
			}
		}
	}
}

func (h *wsHub) broadcast(channelID int64, payload []byte) {
	h.mu.RLock()
	subs := h.channelSubs[channelID]
	clients := make([]*wsClient, 0, len(subs))
	for client := range subs {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		client.enqueue(payload)
	}
}

func (c *wsClient) readLoop() {
	defer c.close()

	c.conn.SetReadLimit(wsMaxMessage)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		var evt wsInbound
		if err := c.conn.ReadJSON(&evt); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws read error: %v", err)
			}
			break
		}
		c.handleEvent(evt)
	}
}

func (c *wsClient) writeLoop() {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *wsClient) handleEvent(evt wsInbound) {
	switch evt.Type {
	case "subscribe":
		c.handleSubscribe(evt.ChannelID)
	case "unsubscribe":
		c.handleUnsubscribe(evt.ChannelID)
	case "message":
		c.handleMessage(evt.ChannelID, evt.Content)
	case "voice:join":
		c.handleVoiceJoin()
	case "voice:leave":
		c.handleVoiceLeave()
	case "voice:signal":
		c.handleVoiceSignal(evt.Target, evt.Payload)
	default:
		c.sendError("unsupported_event", "unsupported event type")
	}
}

func (c *wsClient) handleSubscribe(channelID int64) {
	if channelID <= 0 {
		c.sendError("invalid_channel", "channel id required")
		return
	}
	ch, exists, err := c.state.channelByID(context.Background(), channelID)
	if err != nil {
		log.Printf("ws subscribe channel lookup: %v", err)
		c.sendError("internal", "failed to subscribe")
		return
	}
	if !exists {
		c.sendError("not_found", "channel not found")
		return
	}

	hasAccess, err := c.state.userHasServerAccess(context.Background(), c.user.Email, ch.ServerID)
	if err != nil {
		log.Printf("ws subscribe access: %v", err)
		c.sendError("internal", "failed to subscribe")
		return
	}
	if !hasAccess {
		c.sendError("forbidden", "no access to channel")
		return
	}

	c.mu.Lock()
	if c.subscriptions == nil {
		c.subscriptions = make(map[int64]struct{})
	}
	c.subscriptions[channelID] = struct{}{}
	c.mu.Unlock()

	c.hub.subscribe(c, channelID)
}

func (c *wsClient) handleUnsubscribe(channelID int64) {
	c.mu.Lock()
	if c.subscriptions != nil {
		delete(c.subscriptions, channelID)
	}
	c.mu.Unlock()
	c.hub.unsubscribe(c, channelID)
}

func (c *wsClient) handleMessage(channelID int64, content string) {
	content = strings.TrimSpace(content)
	if channelID <= 0 || content == "" {
		c.sendError("invalid_message", "channel and content required")
		return
	}

	c.mu.Lock()
	_, subscribed := c.subscriptions[channelID]
	c.mu.Unlock()
	if !subscribed {
		c.sendError("not_subscribed", "subscribe before sending")
		return
	}

	if utf8.RuneCountInString(content) > 2000 {
		c.sendError("too_long", "message too long")
		return
	}

	msg, err := c.state.saveMessage(context.Background(), channelID, c.user.Email, content)
	if err != nil {
		log.Printf("ws save message: %v", err)
		c.sendError("internal", "failed to save message")
		return
	}
	if msg.AuthorDisplayName == "" {
		msg.AuthorDisplayName = c.user.DisplayName
	}

	dto := toMessageDTO(msg)
	c.state.broadcastMessage(dto)
}

func (c *wsClient) handleVoiceJoin() {
	if c.voiceJoined {
		participants := c.state.voiceSnapshot(c)
		self := c.voiceParticipant()
		c.enqueueJSON(wsOutbound{Type: "voice:participants", Participants: participants, Self: &self})
		return
	}

	participants, self := c.state.voiceRegister(c)
	c.enqueueJSON(wsOutbound{Type: "voice:participants", Participants: participants, Self: &self})
	c.state.voiceNotifyJoin(self, c)
}

func (c *wsClient) handleVoiceLeave() {
	participant, removed := c.state.voiceUnregister(c)
	if removed {
		c.state.voiceNotifyLeave(participant, c)
	}
}

func (c *wsClient) handleVoiceSignal(target string, payload json.RawMessage) {
	if !c.voiceJoined || c.voiceID == "" {
		c.sendError("voice_not_joined", "join voice before sending media")
		return
	}
	if target == "" || len(payload) == 0 {
		c.sendError("voice_invalid", "signal target and payload required")
		return
	}
	if err := c.state.voiceSignal(c, target, payload); err != nil {
		if errors.Is(err, errVoiceTargetMissing) {
			c.sendError("voice_target_missing", "target not available")
		} else {
			log.Printf("voice signal: %v", err)
			c.sendError("internal", "failed to forward signal")
		}
	}
}

func (c *wsClient) sendError(code, message string) {
	c.enqueueJSON(wsOutbound{Type: "error", Code: code, Error: message})
}

func (c *wsClient) enqueue(payload []byte) {
	select {
	case c.send <- payload:
	default:
		select {
		case <-c.send:
		default:
		}
		select {
		case c.send <- payload:
		default:
		}
	}
}

func (c *wsClient) enqueueJSON(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("ws marshal outbound: %v", err)
		return
	}
	c.enqueue(payload)
}

func (c *wsClient) close() {
	c.closeOnce.Do(func() {
		c.state.voiceUnregister(c)
		c.hub.removeClient(c)

		c.mu.Lock()
		conn := c.conn
		send := c.send
		c.conn = nil
		c.send = nil
		c.mu.Unlock()

		if send != nil {
			close(send)
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func (s *serverState) handleWS(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := s.userFromRequest(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		if !errors.Is(err, http.ErrHijacked) {
			log.Printf("upgrade websocket: %v", err)
		}
		return
	}

	client := &wsClient{
		id:    generateSessionID(),
		state: s,
		hub:   s.ws,
		conn:  conn,
		send:  make(chan []byte, 64),
		user:  currentUser,
	}

	go client.writeLoop()
	client.readLoop()
}

func (s *serverState) broadcastMessage(msg messageDTO) {
	outbound := wsOutbound{Type: "message", ChannelID: msg.ChannelID, Message: &msg}
	payload, err := json.Marshal(outbound)
	if err != nil {
		log.Printf("marshal broadcast message: %v", err)
		return
	}
	s.ws.broadcast(msg.ChannelID, payload)
}

var errVoiceTargetMissing = errors.New("voice target missing")

func (s *serverState) voiceRegister(client *wsClient) ([]voiceParticipant, voiceParticipant) {
	s.voice.mu.Lock()
	defer s.voice.mu.Unlock()

	if client.voiceJoined && client.voiceID != "" {
		participants := make([]voiceParticipant, 0, len(s.voice.participants))
		for _, other := range s.voice.participants {
			if other == client {
				continue
			}
			participants = append(participants, other.voiceParticipant())
		}
		return participants, client.voiceParticipant()
	}

	if client.voiceID == "" {
		client.voiceID = generateSessionID()
	}
	client.voiceJoined = true

	participants := make([]voiceParticipant, 0, len(s.voice.participants))
	for _, other := range s.voice.participants {
		participants = append(participants, other.voiceParticipant())
	}

	s.voice.participants[client.voiceID] = client

	return participants, client.voiceParticipant()
}

func (s *serverState) voiceUnregister(client *wsClient) (voiceParticipant, bool) {
	s.voice.mu.Lock()
	defer s.voice.mu.Unlock()

	if !client.voiceJoined || client.voiceID == "" {
		return voiceParticipant{}, false
	}

	part := client.voiceParticipant()
	delete(s.voice.participants, client.voiceID)
	client.voiceJoined = false
	client.voiceID = ""
	return part, true
}

func (s *serverState) voiceSnapshot(exclude *wsClient) []voiceParticipant {
	s.voice.mu.RLock()
	defer s.voice.mu.RUnlock()
	participants := make([]voiceParticipant, 0, len(s.voice.participants))
	for _, other := range s.voice.participants {
		if other == exclude {
			continue
		}
		participants = append(participants, other.voiceParticipant())
	}
	return participants
}

func (s *serverState) voiceNotifyJoin(part voiceParticipant, exclude *wsClient) {
	outbound := wsOutbound{Type: "voice:peer-joined", Peer: &part}
	s.voiceBroadcast(outbound, exclude)
}

func (s *serverState) voiceNotifyLeave(part voiceParticipant, exclude *wsClient) {
	outbound := wsOutbound{Type: "voice:peer-left", Peer: &part}
	s.voiceBroadcast(outbound, exclude)
}

func (s *serverState) voiceSignal(sender *wsClient, targetID string, payload json.RawMessage) error {
	s.voice.mu.RLock()
	target, ok := s.voice.participants[targetID]
	s.voice.mu.RUnlock()
	if !ok {
		return errVoiceTargetMissing
	}

	signal := wsOutbound{Type: "voice:signal", Signal: &voiceSignal{
		From:        sender.voiceID,
		Email:       sender.user.Email,
		DisplayName: sender.user.DisplayName,
		Payload:     payload,
	}}
	target.enqueueJSON(signal)
	return nil
}

func (s *serverState) voiceBroadcast(outbound wsOutbound, exclude *wsClient) {
	payload, err := json.Marshal(outbound)
	if err != nil {
		log.Printf("marshal voice broadcast: %v", err)
		return
	}

	s.voice.mu.RLock()
	clients := make([]*wsClient, 0, len(s.voice.participants))
	for _, client := range s.voice.participants {
		if exclude != nil && client == exclude {
			continue
		}
		clients = append(clients, client)
	}
	s.voice.mu.RUnlock()

	for _, client := range clients {
		client.enqueue(append([]byte(nil), payload...))
	}
}

func (c *wsClient) voiceParticipant() voiceParticipant {
	return voiceParticipant{
		ID:          c.voiceID,
		Email:       c.user.Email,
		DisplayName: c.user.DisplayName,
	}
}
