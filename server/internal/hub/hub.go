// Package hub fans WebSocket events out to clients that subscribed to a session.
package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// SessionLookup reports whether a session id exists.
type SessionLookup func(ctx context.Context, sessionID string) (bool, error)

// Hub is the in-process event fanout. Persisted rows, not this hub, are the history.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
}

type client struct {
	conn *websocket.Conn
	send chan []byte

	mu         sync.Mutex
	sessionID  string
	subscribed bool
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{clients: map[*client]struct{}{}}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// Handle upgrades GET /v1/events and blocks until the socket closes.
func (h *Hub) Handle(w http.ResponseWriter, r *http.Request, sessions SessionLookup) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	c := h.add(conn)
	defer h.remove(c)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var in struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(data, &in); err != nil {
			h.enqueue(c, errorEvent("invalid_json", "expected a JSON object"))
			continue
		}
		if in.Type != "subscribe" {
			h.enqueue(c, errorEvent("invalid_request", "unsupported event type"))
			continue
		}
		if in.SessionID == "" {
			h.enqueue(c, errorEvent("invalid_request", "sessionId is required"))
			continue
		}
		if _, err := uuid.Parse(in.SessionID); err != nil {
			h.enqueue(c, errorEvent("invalid_id", "sessionId must be a UUID"))
			continue
		}
		lookupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ok, err := sessions(lookupCtx, in.SessionID)
		cancel()
		if err != nil {
			h.enqueue(c, errorEvent("internal", "could not load the session"))
			continue
		}
		if !ok {
			h.enqueue(c, errorEvent("not_found", "session not found"))
			continue
		}
		c.setSession(in.SessionID)
		h.enqueue(c, map[string]string{"type": "subscribed", "sessionId": in.SessionID})
	}
}

// Publish sends one event to subscribers of sessionID.
// A full buffer drops the event. The database remains the source of truth.
func (h *Hub) Publish(sessionID string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("websocket marshal: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.wants(sessionID) {
			continue
		}
		select {
		case c.send <- body:
		default:
			log.Printf("dropping websocket event for session %s", sessionID)
		}
	}
}

func (h *Hub) add(conn *websocket.Conn) *client {
	c := &client{conn: conn, send: make(chan []byte, 256)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go c.writePump(h)
	return c
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	close(c.send)
}

func (h *Hub) enqueue(c *client, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; !ok {
		return
	}
	select {
	case c.send <- body:
	default:
		log.Printf("dropping websocket event")
	}
}

func (c *client) setSession(id string) {
	c.mu.Lock()
	c.sessionID = id
	c.subscribed = true
	c.mu.Unlock()
}

func (c *client) wants(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscribed && c.sessionID == sessionID
}

func (c *client) writePump(h *Hub) {
	defer c.conn.Close()
	for msg := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			h.remove(c)
			return
		}
	}
}

func errorEvent(code, message string) map[string]any {
	return map[string]any{
		"type": "error",
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}
}
