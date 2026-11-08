package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yosuang/philo/server/internal/adapter/dev"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/migrate"
)

func TestUnknownAdapterIsRefused(t *testing.T) {
	_, err := New(nil, config.Config{Adapter: "mystery"})
	if err == nil || !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthAndAgentSessionCRUD(t *testing.T) {
	srv := start(t, config.Config{})
	status, body := do(t, srv, http.MethodGet, "/v1/health", nil)
	if status != http.StatusOK {
		t.Fatalf("health %d %s", status, body)
	}
	health := asMap(t, body)
	if health["ok"] != true || health["adapter"] != "dev" || health["makez"] != "disconnected" {
		t.Fatalf("health = %#v", health)
	}

	status, body = do(t, srv, http.MethodGet, "/v1/connectors", nil)
	if status != http.StatusOK {
		t.Fatalf("connectors %d %s", status, body)
	}
	connectors := asMap(t, body)["connectors"].([]any)
	if len(connectors) != 1 {
		t.Fatalf("connectors = %#v", connectors)
	}
	makez := connectors[0].(map[string]any)
	if makez["id"] != "makez" || makez["connected"] != false || makez["reason"] != "PHILO_MAKEZ_URL is not set" {
		t.Fatalf("makez = %#v", makez)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/agents", map[string]any{"name": "  ", "modelId": "local"})
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_request" {
		t.Fatalf("empty name %d %s", status, body)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/agents", map[string]any{
		"name":         "Ada",
		"instructions": "Be brief.",
		"modelId":      "dev-local",
	})
	if status != http.StatusOK {
		t.Fatalf("create agent %d %s", status, body)
	}
	agent := asMap(t, body)
	assertUTC(t, agent["createdAt"])
	agentID := agent["id"].(string)

	status, body = do(t, srv, http.MethodPatch, "/v1/agents/"+agentID, map[string]any{"instructions": "Be briefer."})
	if status != http.StatusOK {
		t.Fatalf("patch %d %s", status, body)
	}
	patched := asMap(t, body)
	if patched["name"] != "Ada" || patched["instructions"] != "Be briefer." || patched["modelId"] != "dev-local" {
		t.Fatalf("patched = %#v", patched)
	}

	status, body = do(t, srv, http.MethodGet, "/v1/agents", nil)
	if status != http.StatusOK || len(asMap(t, body)["agents"].([]any)) != 1 {
		t.Fatalf("list agents %d %s", status, body)
	}

	other := createAgent(t, srv, "Bea")
	status, body = do(t, srv, http.MethodPost, "/v1/sessions", map[string]any{"agentId": agentID, "title": "Named"})
	if status != http.StatusOK {
		t.Fatalf("session %d %s", status, body)
	}
	named := asMap(t, body)
	if named["title"] != "Named" || named["agentId"] != agentID {
		t.Fatalf("session = %#v", named)
	}
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+named["id"].(string)+"/messages", map[string]any{"content": "  "})
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_request" {
		t.Fatalf("empty content %d %s", status, body)
	}
	createSession(t, srv, other)
	status, body = do(t, srv, http.MethodGet, "/v1/sessions?agentId="+agentID, nil)
	if status != http.StatusOK {
		t.Fatalf("list sessions %d %s", status, body)
	}
	sessions := asMap(t, body)["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["id"] != named["id"] {
		t.Fatalf("sessions = %#v", sessions)
	}

	status, body = do(t, srv, http.MethodGet, "/v1/sessions", nil)
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_request" {
		t.Fatalf("missing agentId %d %s", status, body)
	}
	status, body = do(t, srv, http.MethodGet, "/v1/agents/not-a-uuid", nil)
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_id" {
		t.Fatalf("bad id %d %s", status, body)
	}
	status, body = do(t, srv, http.MethodGet, "/v1/agents/11111111-1111-4111-8111-111111111111", nil)
	if status != http.StatusNotFound || errCode(t, body) != "not_found" {
		t.Fatalf("missing agent %d %s", status, body)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := newDB(t)
	if err := migrate.Up(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	app, err := New(pool, config.Config{Adapter: "dev", DevChunkDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		app.Stop()
		srv.Close()
	})
	if id := createAgent(t, srv, "Ada"); id == "" {
		t.Fatal("empty id")
	}
}

func TestStreamPersistsTranscript(t *testing.T) {
	srv := start(t, config.Config{})
	agentID := createAgent(t, srv, "Ada")
	sessionID := createSession(t, srv, agentID)
	ws := subscribe(t, srv, sessionID)

	content := "hello there friend"
	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": content})
	if status != http.StatusOK {
		t.Fatalf("post %d %s", status, body)
	}
	posted := asMap(t, body)
	user := posted["userMessage"].(map[string]any)
	assistant := posted["assistantMessage"].(map[string]any)
	if user["role"] != "user" || user["status"] != "complete" || user["content"] != content {
		t.Fatalf("user = %#v", user)
	}
	if assistant["role"] != "assistant" || assistant["status"] != "streaming" {
		t.Fatalf("assistant = %#v", assistant)
	}
	assistantID := assistant["id"].(string)

	_, seen := ws.until(t, "message.completed", 5*time.Second)
	if hasType(seen, "message.error") || hasType(seen, "message.canceled") {
		t.Fatalf("unexpected terminal events %#v", eventTypes(seen))
	}
	if deltas(seen) != dev.Reply(content) {
		t.Fatalf("deltas = %q", deltas(seen))
	}
	if !hasTitle(seen, content) {
		t.Fatalf("missing session title event in %#v", eventTypes(seen))
	}

	got := pollStatus(t, srv, sessionID, assistantID, "complete")
	if got["content"] != dev.Reply(content) {
		t.Fatalf("stored = %q", got["content"])
	}
	meta := got["metadata"].(map[string]any)
	if meta["adapter"] != "dev" {
		t.Fatalf("metadata = %#v", meta)
	}
	if _, ok := got["error"]; ok {
		t.Fatalf("error present: %#v", got["error"])
	}
	if strings.Contains(strings.ToLower(got["content"].(string)), "grok") {
		t.Fatal("dev reply must not pretend to be Grok")
	}

	status, body = do(t, srv, http.MethodGet, "/v1/sessions/"+sessionID+"/messages", nil)
	if status != http.StatusOK {
		t.Fatalf("reload %d %s", status, body)
	}
	messages := asMap(t, body)["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["role"] != "user" || messages[1].(map[string]any)["content"] != dev.Reply(content) {
		t.Fatalf("reload = %#v", messages)
	}
	status, body = do(t, srv, http.MethodGet, "/v1/sessions/"+sessionID, nil)
	if asMap(t, body)["title"] != content {
		t.Fatalf("title = %s", body)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/messages/"+assistantID+"/retry", map[string]any{})
	if status != http.StatusConflict || errCode(t, body) != "not_retryable" {
		t.Fatalf("retry complete %d %s", status, body)
	}
	status, body = do(t, srv, http.MethodPost, "/v1/messages/"+user["id"].(string)+"/retry", map[string]any{})
	if status != http.StatusConflict || errCode(t, body) != "not_retryable" {
		t.Fatalf("retry user %d %s", status, body)
	}
	status, body = do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+sessionID, nil)
	if status != http.StatusOK || len(asMap(t, body)["approvals"].([]any)) != 0 {
		t.Fatalf("approvals = %s", body)
	}
}

func TestCancelPersistsPartialTextAndRetry(t *testing.T) {
	srv := start(t, config.Config{DevChunkDelay: 120 * time.Millisecond})
	agentID := createAgent(t, srv, "Ada")
	sessionID := createSession(t, srv, agentID)
	ws := subscribe(t, srv, sessionID)
	content := "hello cancel"
	full := dev.Reply(content)

	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": content})
	if status != http.StatusOK {
		t.Fatalf("post %d %s", status, body)
	}
	assistantID := asMap(t, body)["assistantMessage"].(map[string]any)["id"].(string)
	_, first := ws.until(t, "message.delta", 5*time.Second)

	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel %d %s", status, body)
	}
	canceled := asMap(t, body)
	if canceled["status"] != "canceled" {
		t.Fatalf("cancel body = %#v", canceled)
	}
	_, rest := ws.until(t, "message.canceled", 5*time.Second)
	seen := append(first, rest...)
	if hasType(seen, "message.completed") {
		t.Fatal("completed after cancel")
	}

	stored := pollStatus(t, srv, sessionID, assistantID, "canceled")
	got := stored["content"].(string)
	if got == "" || got == full || !strings.HasPrefix(full, got) {
		t.Fatalf("partial = %q full = %q", got, full)
	}
	if !strings.HasPrefix(got, deltas(seen)) && deltas(seen) != got {
		t.Fatalf("deltas %q are not a prefix of %q", deltas(seen), got)
	}

	status, body = do(t, srv, http.MethodGet, "/v1/sessions/"+sessionID+"/messages", nil)
	reloaded := asMap(t, body)["messages"].([]any)[1].(map[string]any)
	if reloaded["content"] != got || reloaded["status"] != "canceled" {
		t.Fatalf("reload = %#v", reloaded)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/messages/"+assistantID+"/retry", nil)
	if status != http.StatusOK {
		t.Fatalf("retry %d %s", status, body)
	}
	if asMap(t, body)["status"] != "streaming" {
		t.Fatalf("retry body = %s", body)
	}
	ws.until(t, "message.completed", 5*time.Second)
	done := pollStatus(t, srv, sessionID, assistantID, "complete")
	if done["content"] != full {
		t.Fatalf("retried = %q", done["content"])
	}
}

func TestReplyInProgress(t *testing.T) {
	srv := start(t, config.Config{DevChunkDelay: 150 * time.Millisecond})
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": "one"})
	if status != http.StatusOK {
		t.Fatalf("first %d %s", status, body)
	}
	assistantID := asMap(t, body)["assistantMessage"].(map[string]any)["id"].(string)
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": "two"})
	if status != http.StatusConflict || errCode(t, body) != "reply_in_progress" {
		t.Fatalf("second %d %s", status, body)
	}
	pollStatus(t, srv, sessionID, assistantID, "complete")
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/cancel", nil)
	if status != http.StatusConflict || errCode(t, body) != "nothing_to_cancel" {
		t.Fatalf("idle cancel %d %s", status, body)
	}
}

func TestDraftApprovalApproveAndReject(t *testing.T) {
	srv := start(t, config.Config{})
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	ws := subscribe(t, srv, sessionID)

	post := func(content string) string {
		t.Helper()
		status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": content})
		if status != http.StatusOK {
			t.Fatalf("post %d %s", status, body)
		}
		return asMap(t, body)["assistantMessage"].(map[string]any)["id"].(string)
	}

	assistantID := post("/draft send the weekly note")
	_, seen := ws.until(t, "message.completed", 5*time.Second)
	if !hasType(seen, "approval.requested") {
		t.Fatalf("events = %s", eventTypes(seen))
	}
	done := pollStatus(t, srv, sessionID, assistantID, "complete")
	if done["content"] != dev.Reply("/draft send the weekly note") {
		t.Fatalf("reply = %q", done["content"])
	}

	status, body := do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+sessionID, nil)
	if status != http.StatusOK {
		t.Fatalf("approvals %d %s", status, body)
	}
	approvals := asMap(t, body)["approvals"].([]any)
	if len(approvals) != 1 {
		t.Fatalf("approvals = %#v", approvals)
	}
	approval := approvals[0].(map[string]any)
	if approval["kind"] != "draft_action" || approval["status"] != "pending" || approval["summary"] != "send the weekly note" {
		t.Fatalf("approval = %#v", approval)
	}
	assertUTC(t, approval["createdAt"])
	if approval["messageId"] != assistantID || approval["sessionId"] != sessionID {
		t.Fatalf("approval links = %#v", approval)
	}
	status, body = do(t, srv, http.MethodGet, "/v1/tasks?sessionId="+sessionID, nil)
	if len(asMap(t, body)["tasks"].([]any)) != 0 {
		t.Fatalf("tasks before decision = %s", body)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+approval["id"].(string)+"/decision", map[string]any{"decision": "approve"})
	if status != http.StatusOK {
		t.Fatalf("approve %d %s", status, body)
	}
	if asMap(t, body)["status"] != "approved" {
		t.Fatalf("decision = %s", body)
	}
	task := waitTask(t, srv, sessionID, ws)
	if task["status"] != "completed" || task["source"] != "philo-server" {
		t.Fatalf("task = %#v", task)
	}
	result := task["result"].(string)
	if !strings.Contains(result, "send the weekly note") || !strings.Contains(result, "philo-server") || strings.Contains(strings.ToLower(result), "makez") {
		t.Fatalf("result = %q", result)
	}
	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+approval["id"].(string)+"/decision", map[string]any{"decision": "approve"})
	if status != http.StatusConflict || errCode(t, body) != "approval_not_pending" {
		t.Fatalf("second decision %d %s", status, body)
	}

	rejectSession := createSession(t, srv, createAgent(t, srv, "Bea"))
	rejectWS := subscribe(t, srv, rejectSession)
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+rejectSession+"/messages", map[string]any{"content": "/draft drop the note"})
	if status != http.StatusOK {
		t.Fatalf("reject post %d %s", status, body)
	}
	rejectWS.until(t, "approval.requested", 5*time.Second)
	status, body = do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+rejectSession, nil)
	rejectID := asMap(t, body)["approvals"].([]any)[0].(map[string]any)["id"].(string)
	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+rejectID+"/decision", map[string]any{"decision": "reject"})
	if status != http.StatusOK || asMap(t, body)["status"] != "rejected" {
		t.Fatalf("reject %d %s", status, body)
	}
	time.Sleep(250 * time.Millisecond)
	status, body = do(t, srv, http.MethodGet, "/v1/tasks?sessionId="+rejectSession, nil)
	if status != http.StatusOK || len(asMap(t, body)["tasks"].([]any)) != 0 {
		t.Fatalf("reject tasks = %s", body)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+rejectID+"/decision", map[string]any{"decision": "maybe"})
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_request" {
		t.Fatalf("bad decision %d %s", status, body)
	}
}

func TestCanceledDraftCreatesNoApproval(t *testing.T) {
	srv := start(t, config.Config{DevChunkDelay: 120 * time.Millisecond})
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	ws := subscribe(t, srv, sessionID)
	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": "/draft ship it"})
	if status != http.StatusOK {
		t.Fatalf("post %d %s", status, body)
	}
	ws.until(t, "message.delta", 5*time.Second)
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/cancel", nil)
	if status != http.StatusOK || asMap(t, body)["status"] != "canceled" {
		t.Fatalf("cancel %d %s", status, body)
	}
	time.Sleep(300 * time.Millisecond)
	status, body = do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+sessionID, nil)
	if status != http.StatusOK || len(asMap(t, body)["approvals"].([]any)) != 0 {
		t.Fatalf("approvals = %s", body)
	}
}

func TestProviderMissingKeyDoesNotUseDev(t *testing.T) {
	srv := start(t, config.Config{Adapter: "provider"})
	status, body := do(t, srv, http.MethodGet, "/v1/health", nil)
	if asMap(t, body)["adapter"] != "unconfigured" {
		t.Fatalf("health = %s", body)
	}
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	ws := subscribe(t, srv, sessionID)
	status, body = do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": "/draft ship it"})
	if status != http.StatusOK {
		t.Fatalf("post %d %s", status, body)
	}
	assistant := asMap(t, body)["assistantMessage"].(map[string]any)
	if assistant["metadata"].(map[string]any)["adapter"] != "provider" {
		t.Fatalf("metadata = %#v", assistant["metadata"])
	}
	ev, _ := ws.until(t, "message.error", 5*time.Second)
	if ev["code"] != "provider_not_configured" {
		t.Fatalf("event = %#v", ev)
	}
	if !strings.Contains(ev["message"].(string), "does not fall back") {
		t.Fatalf("message = %#v", ev["message"])
	}
	got := pollStatus(t, srv, sessionID, assistant["id"].(string), "error")
	apiErr := got["error"].(map[string]any)
	if apiErr["code"] != "provider_not_configured" {
		t.Fatalf("error = %#v", apiErr)
	}
	if strings.Contains(got["content"].(string), "not a live model") || strings.Contains(got["content"].(string), "dev adapter") {
		t.Fatalf("fell back to dev: %q", got["content"])
	}
	status, body = do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+sessionID, nil)
	if len(asMap(t, body)["approvals"].([]any)) != 0 {
		t.Fatalf("provider created an approval: %s", body)
	}
}

func TestProviderStreamsRemoteText(t *testing.T) {
	var sawKey bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-key" {
			sawKey = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"alpha\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"beta\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	srv := start(t, config.Config{
		Adapter:         "provider",
		ProviderAPIKey:  "test-key",
		ProviderBaseURL: upstream.URL,
	})
	if asMap(t, mustGET(t, srv, "/v1/health"))["adapter"] != "provider" {
		t.Fatal("health adapter")
	}
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	ws := subscribe(t, srv, sessionID)
	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{"content": "/draft ship it"})
	if status != http.StatusOK {
		t.Fatalf("post %d %s", status, body)
	}
	assistantID := asMap(t, body)["assistantMessage"].(map[string]any)["id"].(string)
	ws.until(t, "message.completed", 5*time.Second)
	got := pollStatus(t, srv, sessionID, assistantID, "complete")
	if got["content"] != "alphabeta" || got["metadata"].(map[string]any)["adapter"] != "provider" {
		t.Fatalf("message = %#v", got)
	}
	if !sawKey {
		t.Fatal("provider was not called with the key")
	}
	if len(asMap(t, mustGET(t, srv, "/v1/approvals?sessionId="+sessionID))["approvals"].([]any)) != 0 {
		t.Fatal("provider adapter created an approval")
	}
}

func TestMakezHealthOnTheAPI(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(okSrv.Close)
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	t.Cleanup(badSrv.Close)

	up := start(t, config.Config{MakezURL: okSrv.URL})
	health := asMap(t, mustGET(t, up, "/v1/health"))
	if health["makez"] != "connected" {
		t.Fatalf("health = %#v", health)
	}
	conn := asMap(t, mustGET(t, up, "/v1/connectors"))["connectors"].([]any)[0].(map[string]any)
	if conn["connected"] != true || conn["reason"] != "Makez health check succeeded" {
		t.Fatalf("connector = %#v", conn)
	}

	down := start(t, config.Config{MakezURL: badSrv.URL})
	health = asMap(t, mustGET(t, down, "/v1/health"))
	if health["makez"] != "disconnected" {
		t.Fatalf("bad health = %#v", health)
	}
	conn = asMap(t, mustGET(t, down, "/v1/connectors"))["connectors"].([]any)[0].(map[string]any)
	if conn["connected"] != false || !strings.Contains(conn["reason"].(string), "HTTP 503") {
		t.Fatalf("bad connector = %#v", conn)
	}
}

func start(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	if cfg.Adapter == "" {
		cfg.Adapter = "dev"
	}
	if cfg.Adapter == "dev" && cfg.DevChunkDelay == 0 {
		cfg.DevChunkDelay = 25 * time.Millisecond
	}
	pool := newDB(t)
	app, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		app.Stop()
		srv.Close()
	})
	return srv
}

func createAgent(t *testing.T, srv *httptest.Server, name string) string {
	t.Helper()
	status, body := do(t, srv, http.MethodPost, "/v1/agents", map[string]any{
		"name":         name,
		"instructions": "Be brief.",
		"modelId":      "dev-local",
	})
	if status != http.StatusOK {
		t.Fatalf("create agent %d %s", status, body)
	}
	return asMap(t, body)["id"].(string)
}

func createSession(t *testing.T, srv *httptest.Server, agentID string) string {
	t.Helper()
	status, body := do(t, srv, http.MethodPost, "/v1/sessions", map[string]any{"agentId": agentID})
	if status != http.StatusOK {
		t.Fatalf("create session %d %s", status, body)
	}
	return asMap(t, body)["id"].(string)
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func mustGET(t *testing.T, srv *httptest.Server, path string) []byte {
	t.Helper()
	status, body := do(t, srv, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s %d %s", path, status, body)
	}
	return body
}

func asMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("json: %v body=%s", err, body)
	}
	return out
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	m := asMap(t, body)
	wrap, _ := m["error"].(map[string]any)
	if wrap == nil || wrap["code"] == "" || wrap["message"] == "" {
		t.Fatalf("error shape: %s", body)
	}
	return wrap["code"].(string)
}

func assertUTC(t *testing.T, v any) {
	t.Helper()
	s, _ := v.(string)
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil || !strings.HasSuffix(s, "Z") {
		t.Fatalf("timestamp %q", s)
	}
}

func pollStatus(t *testing.T, srv *httptest.Server, sessionID, messageID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		status, body := do(t, srv, http.MethodGet, "/v1/sessions/"+sessionID+"/messages", nil)
		last = body
		if status != http.StatusOK {
			t.Fatalf("messages %d %s", status, body)
		}
		for _, item := range asMap(t, body)["messages"].([]any) {
			msg := item.(map[string]any)
			if msg["id"] == messageID && msg["status"] == want {
				return msg
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("message %s did not become %s; last %s", messageID, want, last)
	return nil
}

func waitTask(t *testing.T, srv *httptest.Server, sessionID string, ws *socket) map[string]any {
	t.Helper()
	sawRunning := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-ws.ev:
			if !ok {
				t.Fatal("websocket closed while waiting for the task")
			}
			if ev["type"] == "task.updated" && ev["status"] == "running" && ev["source"] == "philo-server" {
				sawRunning = true
			}
		default:
		}
		status, body := do(t, srv, http.MethodGet, "/v1/tasks?sessionId="+sessionID, nil)
		if status != http.StatusOK {
			t.Fatalf("tasks %d %s", status, body)
		}
		tasks := asMap(t, body)["tasks"].([]any)
		if len(tasks) == 1 && tasks[0].(map[string]any)["status"] == "completed" {
			if !sawRunning {
				// The running event may still be buffered behind the completed one.
				drainUntil(t, ws, deadline, func(ev map[string]any) bool {
					if ev["type"] == "task.updated" && ev["status"] == "running" {
						sawRunning = true
					}
					return sawRunning
				})
			}
			if !sawRunning {
				t.Fatal("task completed without a running event")
			}
			return tasks[0].(map[string]any)
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("task did not complete")
	return nil
}

type socket struct {
	conn *websocket.Conn
	ev   chan map[string]any
}

func subscribe(t *testing.T, srv *httptest.Server, sessionID string) *socket {
	t.Helper()
	rawURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/events"
	conn, _, err := websocket.DefaultDialer.Dial(rawURL, nil)
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
	if err := conn.WriteJSON(map[string]string{"type": "subscribe", "sessionId": sessionID}); err != nil {
		t.Fatal(err)
	}
	ev := s.next(t, 2*time.Second)
	if ev["type"] != "subscribed" || ev["sessionId"] != sessionID {
		t.Fatalf("subscribe ack %#v", ev)
	}
	return s
}

func (s *socket) next(t *testing.T, d time.Duration) map[string]any {
	t.Helper()
	select {
	case ev, ok := <-s.ev:
		if !ok {
			t.Fatal("websocket closed")
		}
		return ev
	case <-time.After(d):
		t.Fatal("websocket timeout")
		return nil
	}
}

func (s *socket) until(t *testing.T, typ string, d time.Duration) (map[string]any, []map[string]any) {
	t.Helper()
	var seen []map[string]any
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-s.ev:
			if !ok {
				t.Fatalf("websocket closed, saw %s", eventTypes(seen))
			}
			seen = append(seen, ev)
			if ev["type"] == typ {
				return ev, seen
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s, saw %s", typ, eventTypes(seen))
		}
	}
}

func drainUntil(t *testing.T, ws *socket, deadline time.Time, pred func(map[string]any) bool) {
	t.Helper()
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-ws.ev:
			if !ok {
				return
			}
			if pred(ev) {
				return
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func deltas(events []map[string]any) string {
	var b strings.Builder
	for _, ev := range events {
		if ev["type"] == "message.delta" {
			text, _ := ev["text"].(string)
			b.WriteString(text)
		}
	}
	return b.String()
}

func hasType(events []map[string]any, typ string) bool {
	for _, ev := range events {
		if ev["type"] == typ {
			return true
		}
	}
	return false
}

func hasTitle(events []map[string]any, title string) bool {
	for _, ev := range events {
		if ev["type"] == "session.updated" && ev["title"] == title {
			return true
		}
	}
	return false
}

func eventTypes(events []map[string]any) string {
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		typ, _ := ev["type"].(string)
		parts = append(parts, typ)
	}
	return strings.Join(parts, ",")
}
