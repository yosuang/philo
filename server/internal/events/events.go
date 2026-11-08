// Package events builds the WebSocket payloads described by the phase-1 contract.
package events

import "encoding/json"

// Delta is one incremental piece of assistant text.
type Delta struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// MessageRef is a terminal message event without a body.
type MessageRef struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	SessionID string `json:"sessionId"`
}

// MessageError surfaces a generation failure. Code is extra; message is required.
type MessageError struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId"`
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
}

// WithType flattens payload's JSON object and sets the event name.
// Approval, task, and session events are the full object plus type.
func WithType(eventType string, payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	out["type"] = eventType
	return out, nil
}
