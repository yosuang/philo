package dev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yosuang/philo/server/internal/adapter"
)

func TestReplyIsObviouslyLocal(t *testing.T) {
	got := Reply("hello")
	if !strings.Contains(got, "philo dev adapter") || !strings.Contains(got, "not a live model") {
		t.Fatalf("reply = %q", got)
	}
	if strings.Contains(strings.ToLower(got), "grok") {
		t.Fatalf("reply must not pretend to be Grok: %q", got)
	}
}

func TestDraftSummary(t *testing.T) {
	summary, ok := draftSummary("/draft send the weekly note")
	if !ok || summary != "send the weekly note" {
		t.Fatalf("summary = %q ok=%v", summary, ok)
	}
	if _, ok := draftSummary("/draftfoo"); ok {
		t.Fatal("prefix requires a space")
	}
	if _, ok := draftSummary("please /draft later"); ok {
		t.Fatal("must start with /draft ")
	}
	summary, ok = draftSummary("/draft ")
	if !ok || summary != "" {
		t.Fatalf("empty summary = %q ok=%v", summary, ok)
	}
	summary, ok = draftSummary("/draft first line\nsecond")
	if !ok || summary != "first line" {
		t.Fatalf("line summary = %q", summary)
	}
}

func TestStreamChunksCoverReplyAndRequestApproval(t *testing.T) {
	a := New(0)
	var got strings.Builder
	res, err := a.Stream(context.Background(), adapter.Request{UserContent: "/draft ship the note"}, func(text string) error {
		got.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != Reply("/draft ship the note") {
		t.Fatalf("stream = %q", got.String())
	}
	if res.Approval == nil || res.Approval.Kind != "draft_action" || res.Approval.Summary != "ship the note" {
		t.Fatalf("approval = %#v", res.Approval)
	}
}

func TestEmptyDraftDoesNotRequestApproval(t *testing.T) {
	a := New(0)
	res, err := a.Stream(context.Background(), adapter.Request{UserContent: "/draft "}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Approval != nil {
		t.Fatalf("approval = %#v", res.Approval)
	}
}

func TestStreamStopsWhenCanceled(t *testing.T) {
	a := New(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := a.Stream(ctx, adapter.Request{UserContent: "hello"}, func(string) error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}
