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

// SubscribeAcker can delay the subscribed ack. A nil acker acknowledges
// immediately. Production does not install one.
type SubscribeAcker interface {
	// Delayed reports whether this subscribe must wait before the socket is
	// marked subscribed. A true result does not acknowledge yet.
	Delayed(sessionID string) bool
	// Ready waits until the ack may be sent. False means do not subscribe.
	Ready(ctx context.Context, sessionID string) bool
}

// Hub is the in-process event fanout. Persisted rows, not this hub, are the history.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	acker   SubscribeAcker
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

// SetAcker installs a fixture ack gate. Nil keeps the immediate ack.
// Call it before serving. It is not a production control.
func (h *Hub) SetAcker(acker SubscribeAcker) {
	h.mu.Lock()
	h.acker = acker
	h.mu.Unlock()
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		h.acknowledge(ctx, c, in.SessionID)
	}
}

// acknowledge marks the socket subscribed and sends the ack.
// A delayed acker does neither until Ready. Publishes before that are not
// delivered on this socket.
func (h *Hub) acknowledge(ctx context.Context, c *client, sessionID string) {
	h.mu.Lock()
	acker := h.acker
	h.mu.Unlock()
	send := func() {
		if ctx.Err() != nil {
			return
		}
		c.setSession(sessionID)
		h.enqueue(c, map[string]string{"type": "subscribed", "sessionId": sessionID})
		log.Printf("subscribe acknowledged session=%s", sessionID)
	}
	if acker == nil || !acker.Delayed(sessionID) {
		send()
		return
	}
	log.Printf("subscribe ack held session=%s", sessionID)
	go func() {
		if !acker.Ready(ctx, sessionID) {
			log.Printf("subscribe ack dropped session=%s", sessionID)
			return
		}
		send()
	}()
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
