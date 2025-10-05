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

func newWSHub() *wsHub {
	return &wsHub{
		channelSubs: make(map[int64]map[*wsClient]struct{}),
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
	for client := range subs {
		client.enqueue(payload)
	}
	h.mu.RUnlock()
}

type wsClient struct {
	state         *serverState
	hub           *wsHub
	conn          *websocket.Conn
	send          chan []byte
	user          user
	subscriptions map[int64]struct{}
	mu            sync.Mutex
	closeOnce     sync.Once
}

type wsInbound struct {
	Type      string `json:"type"`
	ChannelID int64  `json:"channelId"`
	Content   string `json:"content"`
}

type wsOutbound struct {
	Type      string      `json:"type"`
	ChannelID int64       `json:"channelId,omitempty"`
	Message   *messageDTO `json:"message,omitempty"`
	Error     string      `json:"error,omitempty"`
	Code      string      `json:"code,omitempty"`
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
	if _, already := c.subscriptions[channelID]; already {
		c.mu.Unlock()
		return
	}
	c.subscriptions[channelID] = struct{}{}
	c.mu.Unlock()

	c.hub.subscribe(c, channelID)
	c.enqueueJSON(wsOutbound{Type: "subscribed", ChannelID: channelID})
}

func (c *wsClient) handleUnsubscribe(channelID int64) {
	c.mu.Lock()
	if _, exists := c.subscriptions[channelID]; !exists {
		c.mu.Unlock()
		return
	}
	delete(c.subscriptions, channelID)
	c.mu.Unlock()

	c.hub.unsubscribe(c, channelID)
	c.enqueueJSON(wsOutbound{Type: "unsubscribed", ChannelID: channelID})
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

	c.state.broadcastMessage(toMessageDTO(msg))
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
		state: s,
		hub:   s.ws,
		conn:  conn,
		send:  make(chan []byte, 32),
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
