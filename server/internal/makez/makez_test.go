package makez

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnsetIsDisconnected(t *testing.T) {
	got := Status(context.Background(), "  ", nil)
	if got.Connected || got.ID != "makez" || got.Reason != "PHILO_MAKEZ_URL is not set" {
		t.Fatalf("%#v", got)
	}
	if HealthValue(got) != "disconnected" {
		t.Fatal(HealthValue(got))
	}
}

func TestHTTPStatus(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	got := Status(context.Background(), ok.URL, ok.Client())
	if !got.Connected || HealthValue(got) != "connected" {
		t.Fatalf("%#v", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	got = Status(context.Background(), bad.URL, bad.Client())
	if got.Connected || !strings.Contains(got.Reason, "HTTP 502") {
		t.Fatalf("%#v", got)
	}
}

func TestUnreachable(t *testing.T) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	got := Status(context.Background(), "http://127.0.0.1:1", client)
	if got.Connected || !strings.Contains(got.Reason, "Makez health check failed") {
		t.Fatalf("%#v", got)
	}
}
