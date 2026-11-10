package httpapi

import (
	"context"
	"sync"
)

// subscribeHold delays the subscribed ack for the business fixture.
// Production does not construct one. A held socket is not subscribed, so the
// hub does not deliver publishes until release.
type subscribeHold struct {
	mu       sync.Mutex
	held     map[string]chan struct{}
	holdNext bool
}

func newSubscribeHold() *subscribeHold {
	return &subscribeHold{held: map[string]chan struct{}{}}
}

// Hold keeps the next ack for sessionID until Release.
func (h *subscribeHold) Hold(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.held[sessionID]; !ok {
		h.held[sessionID] = make(chan struct{})
	}
}

// ArmNext holds whichever session subscribes next.
func (h *subscribeHold) ArmNext() {
	h.mu.Lock()
	h.holdNext = true
	h.mu.Unlock()
}

// Release lets a held ack proceed. An empty sessionID releases every hold.
func (h *subscribeHold) Release(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sessionID == "" {
		for id, ch := range h.held {
			close(ch)
			delete(h.held, id)
		}
		h.holdNext = false
		return
	}
	if ch, ok := h.held[sessionID]; ok {
		close(ch)
		delete(h.held, sessionID)
	}
}

// Delayed implements hub.SubscribeAcker. A hold-next flag is consumed by the
// first session that arrives and then waits with the others.
func (h *subscribeHold) Delayed(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.held[sessionID]; ok {
		return true
	}
	if h.holdNext {
		h.held[sessionID] = make(chan struct{})
		h.holdNext = false
		return true
	}
	return false
}

// Ready implements hub.SubscribeAcker.
func (h *subscribeHold) Ready(ctx context.Context, sessionID string) bool {
	h.mu.Lock()
	ch := h.held[sessionID]
	h.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}
