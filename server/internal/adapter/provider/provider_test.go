package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yosuang/philo/server/internal/adapter"
)

func TestMissingKeyDoesNotEmit(t *testing.T) {
	a := New("", "http://127.0.0.1:1")
	_, err := a.Stream(context.Background(), adapter.Request{
		ModelID:     "m",
		UserContent: "/draft ship it",
	}, func(string) error {
		t.Fatal("emitted a chunk without a key")
		return nil
	})
	var failure *adapter.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Code != "provider_not_configured" {
		t.Fatalf("code = %s", failure.Code)
	}
	if strings.Contains(failure.Message, "dev adapter reply") {
		t.Fatal(failure.Message)
	}
}

func TestStreamsProviderTextOnly(t *testing.T) {
	var sawAuth, sawModel bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer test-key" {
			sawAuth = true
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"model":"unit-model"`) {
			sawModel = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"alpha\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"beta\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a := New("test-key", srv.URL)
	var got strings.Builder
	res, err := a.Stream(context.Background(), adapter.Request{
		ModelID:      "unit-model",
		Instructions: "be brief",
		UserContent:  "/draft ship it",
	}, func(text string) error {
		got.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawAuth || !sawModel {
		t.Fatalf("auth=%v model=%v", sawAuth, sawModel)
	}
	if got.String() != "alphabeta" {
		t.Fatalf("text = %q", got.String())
	}
	if strings.Contains(got.String(), "dev adapter") || strings.Contains(got.String(), "not a live model") {
		t.Fatalf("provider leaked a dev reply: %q", got.String())
	}
	if res.Approval != nil {
		t.Fatal("provider adapter must not create approvals")
	}
}

func TestHTTPErrorIsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := New("test-key", srv.URL)
	_, err := a.Stream(context.Background(), adapter.Request{ModelID: "m", UserContent: "hi"}, func(string) error {
		t.Fatal("emitted")
		return nil
	})
	var failure *adapter.Failure
	if !errors.As(err, &failure) || failure.Code != "provider_error" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(failure.Message, "401") {
		t.Fatalf("message = %q", failure.Message)
	}
}
