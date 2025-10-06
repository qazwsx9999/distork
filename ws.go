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
		return true
	},
}

type wsHub struct {
	mu          sync.RWMutex
	channelSubs map[int64]map[*wsClient]struct{}
}

type voiceState struct {
	mu    sync.RWMutex
	rooms map[int64]*voiceRoom
}

type voiceRoom struct {
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

	voiceJoined    bool
	voiceID        string
	voiceChannelID int64
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
	return &wsHub{channelSubs: make(map[int64]map[*wsClient]struct{})}
}

func newVoiceState() *voiceState {
	return &voiceState{rooms: make(map[int64]*voiceRoom)}
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

func (s *serverState) voiceJoin(channelID int64, client *wsClient) ([]voiceParticipant, voiceParticipant, error) {
	s.voice.mu.Lock()
	defer s.voice.mu.Unlock()

	if client.voiceJoined && client.voiceChannelID != 0 && client.voiceChannelID != channelID {
		s.voiceLeaveLocked(client.voiceChannelID, client)
	}

	room := s.voice.rooms[channelID]
	if room == nil {
		room = &voiceRoom{participants: make(map[string]*wsClient)}
		s.voice.rooms[channelID] = room
	}

	if client.voiceID == "" {
		client.voiceID = generateSessionID()
	}
	client.voiceJoined = true
	client.voiceChannelID = channelID
	room.participants[client.voiceID] = client

	participants := make([]voiceParticipant, 0, len(room.participants)-1)
	for id, other := range room.participants {
		if other == client {
			continue
		}
		participants = append(participants, voiceParticipant{
			ID:          id,
			Email:       other.user.Email,
			DisplayName: other.user.DisplayName,
		})
	}

	self := voiceParticipant{
		ID:          client.voiceID,
		Email:       client.user.Email,
		DisplayName: client.user.DisplayName,
	}

	return participants, self, nil
}

func (s *serverState) voiceLeave(channelID int64, client *wsClient) (voiceParticipant, bool) {
	s.voice.mu.Lock()
	defer s.voice.mu.Unlock()
	return s.voiceLeaveLocked(channelID, client)
}

func (s *serverState) voiceLeaveLocked(channelID int64, client *wsClient) (voiceParticipant, bool) {
	room := s.voice.rooms[channelID]
	if room == nil {
		client.voiceJoined = false
		client.voiceChannelID = 0
		client.voiceID = ""
		return voiceParticipant{}, false
	}

	id := client.voiceID
	if id == "" {
		for candidateID, participant := range room.participants {
			if participant == client {
				id = candidateID
				break
			}
		}
	}
	if id == "" {
		return voiceParticipant{}, false
	}

	part := voiceParticipant{ID: id, Email: client.user.Email, DisplayName: client.user.DisplayName}
	delete(room.participants, id)
	client.voiceJoined = false
	client.voiceChannelID = 0
	client.voiceID = ""

	if len(room.participants) == 0 {
		delete(s.voice.rooms, channelID)
	}
	return part, true
}

func (s *serverState) voiceParticipants(channelID int64, exclude *wsClient) []voiceParticipant {
	s.voice.mu.RLock()
	defer s.voice.mu.RUnlock()
	room := s.voice.rooms[channelID]
	if room == nil {
		return nil
	}
	participants := make([]voiceParticipant, 0, len(room.participants))
	for id, client := range room.participants {
		if exclude != nil && client == exclude {
			continue
		}
		participants = append(participants, voiceParticipant{
			ID:          id,
			Email:       client.user.Email,
			DisplayName: client.user.DisplayName,
		})
	}
	return participants
}

func (s *serverState) voiceBroadcast(channelID int64, outbound wsOutbound, exclude *wsClient) {
	payload, err := json.Marshal(outbound)
	if err != nil {
		log.Printf("marshal voice broadcast: %v", err)
		return
	}

	s.voice.mu.RLock()
	room := s.voice.rooms[channelID]
	if room != nil {
		for _, client := range room.participants {
			if exclude != nil && client == exclude {
				continue
			}
			client.enqueue(append([]byte(nil), payload...))
		}
	}
	s.voice.mu.RUnlock()
}

var errVoiceTargetMissing = errors.New("voice target missing")
var errVoiceNotInRoom = errors.New("voice sender not in room")

func (s *serverState) voiceSignal(channelID int64, sender *wsClient, targetID string, payload json.RawMessage) error {
	s.voice.mu.RLock()
	room := s.voice.rooms[channelID]
	if room == nil {
		s.voice.mu.RUnlock()
		return errVoiceNotInRoom
	}
	target, ok := room.participants[targetID]
	if !ok {
		s.voice.mu.RUnlock()
		return errVoiceTargetMissing
	}
	s.voice.mu.RUnlock()

	signal := wsOutbound{
		Type:      "voice:signal",
		ChannelID: channelID,
		Signal: &voiceSignal{
			From:        sender.voiceID,
			Email:       sender.user.Email,
			DisplayName: sender.user.DisplayName,
			Payload:     payload,
		},
	}
	target.enqueueJSON(signal)
	return nil
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
		c.handleVoiceJoin(evt.ChannelID)
	case "voice:leave":
		c.handleVoiceLeave(evt.ChannelID)
	case "voice:signal":
		c.handleVoiceSignal(evt.ChannelID, evt.Target, evt.Payload)
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

func (c *wsClient) handleVoiceJoin(channelID int64) {
	if channelID <= 0 {
		c.sendError("voice_invalid", "channel id required")
		return
	}

	ch, exists, err := c.state.channelByID(context.Background(), channelID)
	if err != nil {
		c.sendError("internal", "failed to load channel")
		return
	}
	if !exists || ch.Kind != "voice" {
		c.sendError("voice_invalid", "not a voice channel")
		return
	}

	hasAccess, err := c.state.userHasServerAccess(context.Background(), c.user.Email, ch.ServerID)
	if err != nil {
		c.sendError("internal", "permission check failed")
		return
	}
	if !hasAccess {
		c.sendError("forbidden", "no access to voice channel")
		return
	}

	participants, self, err := c.state.voiceJoin(channelID, c)
	if err != nil {
		log.Printf("voice join: %v", err)
		c.sendError("internal", "failed to join voice")
		return
	}

	outbound := wsOutbound{Type: "voice:participants", ChannelID: channelID, Participants: participants, Self: &self}
	c.enqueueJSON(outbound)
	c.state.voiceBroadcast(channelID, wsOutbound{Type: "voice:peer-joined", ChannelID: channelID, Peer: &self}, c)
}

func (c *wsClient) handleVoiceLeave(channelID int64) {
	if channelID == 0 {
		channelID = c.voiceChannelID
	}
	if channelID == 0 {
		return
	}
	participant, removed := c.state.voiceLeave(channelID, c)
	if removed {
		c.state.voiceBroadcast(channelID, wsOutbound{Type: "voice:peer-left", ChannelID: channelID, Peer: &participant}, c)
	}
}

func (c *wsClient) handleVoiceSignal(channelID int64, target string, payload json.RawMessage) {
	if channelID == 0 {
		channelID = c.voiceChannelID
	}
	if !c.voiceJoined || channelID == 0 || c.voiceChannelID != channelID {
		c.sendError("voice_not_joined", "join voice before signaling")
		return
	}
	if target == "" || len(payload) == 0 {
		c.sendError("voice_invalid", "signal requires target")
		return
	}
	if err := c.state.voiceSignal(channelID, c, target, payload); err != nil {
		if errors.Is(err, errVoiceTargetMissing) {
			c.sendError("voice_target_missing", "target not found")
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
		if c.voiceChannelID != 0 {
			participant, removed := c.state.voiceLeave(c.voiceChannelID, c)
			if removed {
				c.state.voiceBroadcast(c.voiceChannelID, wsOutbound{Type: "voice:peer-left", ChannelID: c.voiceChannelID, Peer: &participant}, c)
			}
		}

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

func (c *wsClient) voiceParticipant() voiceParticipant {
	return voiceParticipant{
		ID:          c.voiceID,
		Email:       c.user.Email,
		DisplayName: c.user.DisplayName,
	}
}
