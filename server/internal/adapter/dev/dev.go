// Package dev is the labeled development model adapter.
// Replies are deterministic and obviously not a live model.
package dev

import (
	"context"
	"strings"
	"time"

	"github.com/yosuang/philo/server/internal/adapter"
)

const (
	name       = "dev"
	chunkParts = 8
	prefix     = "/draft "
)

// Adapter streams a fixed local reply.
type Adapter struct {
	delay time.Duration
}

// New returns the dev adapter. delay is the pause between stream chunks.
func New(delay time.Duration) *Adapter {
	if delay < 0 {
		delay = 0
	}
	return &Adapter{delay: delay}
}

func (a *Adapter) Name() string { return name }

// Reply is the full deterministic text for a user message.
func Reply(user string) string {
	var b strings.Builder
	b.WriteString("[philo dev adapter] Deterministic local reply. This is not a live model.")
	if summary, ok := draftSummary(user); ok {
		if summary == "" {
			b.WriteString("\n/draft needs a summary after the space. No approval was created.")
		} else {
			b.WriteString("\nDraft action is waiting for approval: ")
			b.WriteString(summary)
		}
		return b.String()
	}
	b.WriteString("\nReceived: ")
	b.WriteString(user)
	return b.String()
}

// Stream emits Reply in pieces. A user message that starts with "/draft "
// and has a non-empty summary asks the engine to create one pending approval
// after the stream finishes. This adapter does not approve or run the action.
func (a *Adapter) Stream(ctx context.Context, req adapter.Request, emit func(text string) error) (adapter.Result, error) {
	if err := ctx.Err(); err != nil {
		return adapter.Result{}, err
	}
	parts := chunks(Reply(req.UserContent), chunkParts)
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return adapter.Result{}, err
		}
		if err := emit(part); err != nil {
			return adapter.Result{}, err
		}
		if i == len(parts)-1 || a.delay == 0 {
			continue
		}
		timer := time.NewTimer(a.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return adapter.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	summary, ok := draftSummary(req.UserContent)
	if !ok || summary == "" {
		return adapter.Result{}, nil
	}
	return adapter.Result{Approval: &adapter.ApprovalSpec{
		Kind:    "draft_action",
		Summary: summary,
	}}, nil
}

func draftSummary(content string) (string, bool) {
	if !strings.HasPrefix(content, prefix) {
		return "", false
	}
	rest := content[len(prefix):]
	if i := strings.IndexAny(rest, "\r\n"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest), true
}

func chunks(s string, parts int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	if parts < 1 {
		parts = 1
	}
	if parts > len(runes) {
		parts = len(runes)
	}
	size := (len(runes) + parts - 1) / parts
	out := make([]string, 0, parts)
	for i := 0; i < len(runes); i += size {
		j := i + size
		if j > len(runes) {
			j = len(runes)
		}
		out = append(out, string(runes[i:j]))
	}
	return out
}
