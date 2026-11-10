package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/store"
)

func dialEvents(t *testing.T, srv *httptest.Server) *socket {
	t.Helper()
	rawURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/events"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(rawURL, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &socket{conn: conn, ev: make(chan map[string]any, 128)}
	go func() {
		for {
			var msg map[string]any
			if err := conn.ReadJSON(&msg); err != nil {
				close(s.ev)
				return
			}
			s.ev <- msg
		}
	}()
	return s
}

func TestSubscribeAckHeldUntilRelease(t *testing.T) {
	srv, _ := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Reed"))
	code, body := postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"type":           "fixture.hold-subscribe",
		"conversationId": conversationID,
	})
	if code != 200 {
		t.Fatalf("hold %d %s", code, body)
	}

	ws := dialEvents(t, srv)
	if err := ws.conn.WriteJSON(map[string]string{"type": "subscribe", "sessionId": conversationID}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ws.ev:
		t.Fatalf("ack arrived while held: %#v", ev)
	case <-time.After(400 * time.Millisecond):
	}

	code, body = postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"type":           "fixture.engine-delta",
		"conversationId": conversationID,
		"messageId":      "m",
		"text":           "missed",
	})
	if code != 200 || asMap(t, body)["cursorless"] != true {
		t.Fatalf("engine delta %d %s", code, body)
	}
	select {
	case ev := <-ws.ev:
		t.Fatalf("publish delivered before subscribe: %#v", ev)
	case <-time.After(300 * time.Millisecond):
	}

	code, body = postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"type":           "fixture.release-subscribe",
		"conversationId": conversationID,
	})
	if code != 200 {
		t.Fatalf("release %d %s", code, body)
	}
	ack := ws.next(t, 2*time.Second)
	if ack["type"] != "subscribed" || ack["sessionId"] != conversationID {
		t.Fatalf("ack %#v", ack)
	}

	code, body = postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"type":           "fixture.engine-delta",
		"conversationId": conversationID,
		"messageId":      "m",
		"text":           "streambit",
	})
	if code != 200 {
		t.Fatalf("second delta %d %s", code, body)
	}
	frame := ws.next(t, 2*time.Second)
	if frame["type"] != "message.delta" || frame["text"] != "streambit" {
		t.Fatalf("frame %#v", frame)
	}
	if _, ok := frame["cursor"]; ok {
		t.Fatalf("cursorless engine delta carried a cursor: %#v", frame)
	}
	if frame["sessionId"] != conversationID {
		t.Fatalf("session %#v", frame)
	}
}

func TestBusinessEventDuringReadIsOutsideTheResponse(t *testing.T) {
	srv, _ := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Reed"))
	seed := fixtureOK(t, srv, conversationID, "seed", "fixture.seed", "", "", "")
	messageID, _ := seed["messageId"].(string)
	if messageID == "" {
		t.Fatalf("seed %#v", seed)
	}
	fixtureOK(t, srv, conversationID, "delta", "message.delta", messageID, "reed", "")
	done := fixtureOK(t, srv, conversationID, "done", "message.completed", messageID, "", "complete")
	high, _ := done["cursor"].(float64)
	if high == 0 {
		t.Fatalf("high %#v", done)
	}

	ws := subscribe(t, srv, conversationID)
	code, body := postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"type":           "fixture.stage-during-read",
		"conversationId": conversationID,
		"eventKey":       "during",
		"messageId":      messageID,
		"text":           "midonce",
	})
	if code != 200 {
		t.Fatalf("stage %d %s", code, body)
	}
	status, raw := getEvents(t, srv, conversationID, int64(high))
	if status != 200 {
		t.Fatalf("events %d %s", status, raw)
	}
	events := asMap(t, raw)["events"].([]any)
	if len(events) != 0 {
		t.Fatalf("read response included the in-flight event: %#v", events)
	}
	frame := ws.next(t, 2*time.Second)
	if frame["type"] != "message.delta" || frame["text"] != "midonce" {
		t.Fatalf("during-read frame %#v", frame)
	}
	cursor, _ := frame["cursor"].(float64)
	if cursor <= high {
		t.Fatalf("cursor %v high %v", cursor, high)
	}
	again := eventsAfter(t, srv, conversationID, int64(cursor))
	if len(again) != 0 {
		t.Fatalf("events after the during-read cursor: %#v", again)
	}
}

func TestFixtureHoldRouteIsAbsentWithoutTheFixture(t *testing.T) {
	srv, _ := startProduction(t, config.Config{})
	code, body := postFixture(t, srv, map[string]any{
		"source": store.FixtureSource,
		"type":   "fixture.hold-subscribe",
	})
	if code != 404 {
		t.Fatalf("fixture route without the fixture: %d %s", code, body)
	}
}
