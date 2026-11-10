package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/store"
)

// contractStandIn speaks the public Makez task routes over a real TCP listener.
// It is not the Makez process. Makez be00c829 was not started.
const (
	standLogSeq    = 7
	standCursor    = "stdout:20,stderr:0"
	standLieCursor = "stdout:999,stderr:9"
	standErrorText = "stand-in: model API key is not configured"
	standToken     = "makez-test-machine-key"
)

type contractStandIn struct {
	URL          string
	Workspace    string
	TaskID       string
	RunID        string
	Token        string
	server       *http.Server
	immediate    bool
	conversation string
	botID        string

	mu             sync.Mutex
	posts          int
	gets           int
	cursors        []string
	postBody       []byte
	paths          []string
	resumed        bool
	idempotencyKey string
}

func startContractStandIn(t *testing.T, conversation, bot string, immediate bool) *contractStandIn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &contractStandIn{
		URL:          "http://" + ln.Addr().String(),
		Workspace:    "ws-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		TaskID:       strings.ReplaceAll(uuid.NewString(), "-", ""),
		RunID:        strings.ReplaceAll(uuid.NewString(), "-", ""),
		Token:        standToken,
		conversation: conversation,
		botID:        bot,
		immediate:    immediate,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tasks", s.submit)
	mux.HandleFunc("GET /v1/tasks/{id}", s.get)
	mux.HandleFunc("GET /v1/tasks/{id}/events", s.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	s.server = &http.Server{Handler: mux}
	go func() { _ = s.server.Serve(ln) }()
	t.Cleanup(func() {
		_ = s.server.Close()
	})
	return s
}

func (s *contractStandIn) submit(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if bytesContainsOwner(raw) {
		http.Error(w, "owner_id is not a field", http.StatusBadRequest)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		Config struct {
			Execution struct {
				Contract       string `json:"contract"`
				Scope          string `json:"scope"`
				BotID          string `json:"bot_id"`
				ConversationID string `json:"conversation_id"`
				HarnessPin     string `json:"harness_pin"`
			} `json:"execution"`
		} `json:"config"`
		AgentID      string `json:"agent_id"`
		AgentVersion string `json:"agent_version"`
		Timeout      int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	if body.AgentID != "pi" || body.AgentVersion != "1" || body.Config.Execution.Contract != "2026-11-10.1" || body.Config.Execution.HarnessPin != "dd30101cfc5b0609411dd80b6f93adbc14db14cf" || body.Config.Execution.Scope != "personal" || body.Timeout != 180 {
		http.Error(w, "contract", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || !strings.HasPrefix(key, "turn/") || !strings.HasSuffix(key, "/submit") {
		http.Error(w, "idempotency", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts++
	s.idempotencyKey = key
	s.paths = append(s.paths, r.Method+" "+r.URL.Path)
	s.postBody = append([]byte(nil), raw...)
	if body.Config.Execution.ConversationID != "" {
		s.conversation = body.Config.Execution.ConversationID
	}
	if body.Config.Execution.BotID != "" {
		s.botID = body.Config.Execution.BotID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(s.taskBodyLocked("QUEUED", "", false, false, ""))
}

func (s *contractStandIn) get(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	if r.PathValue("id") != s.TaskID {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	s.paths = append(s.paths, r.Method+" "+r.URL.Path)
	state, reason := "RUNNING", ""
	exit := false
	if s.immediate || s.resumed {
		state, reason, exit = "FAILED", "AGENT_FAILED", true
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.taskBodyLocked(state, reason, true, exit, standCursor))
}

func (s *contractStandIn) events(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	if r.PathValue("id") != s.TaskID {
		http.NotFound(w, r)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	s.mu.Lock()
	s.cursors = append(s.cursors, cursor)
	s.paths = append(s.paths, r.Method+" "+r.URL.RequestURI())
	if n, err := parseInt(cursor); err == nil && n >= standLogSeq {
		s.resumed = true
	}
	includeLog := cursor == "" || cursor == "0"
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if includeLog {
		log, err := s.logFrame()
		if err != nil {
			http.Error(w, "log", http.StatusInternalServerError)
			return
		}
		if _, err := io.WriteString(w, log); err != nil {
			return
		}
	}
	// The status frame has no id line. Its JSON says FAILED and carries a
	// different cursor. That JSON is not the authoritative task record.
	s.mu.Lock()
	status := s.taskBodyLocked("FAILED", "AGENT_FAILED", true, true, standLieCursor)
	s.mu.Unlock()
	fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
}

func (s *contractStandIn) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		http.Error(w, "auth", http.StatusUnauthorized)
		return false
	}
	if r.Header.Get("X-Makez-Workspace-ID") != s.Workspace {
		http.Error(w, "workspace", http.StatusForbidden)
		return false
	}
	return true
}

func (s *contractStandIn) taskBodyLocked(state, reason string, withRun, withExit bool, cursor string) []byte {
	body := map[string]any{
		"id":            s.TaskID,
		"workspace_id":  s.Workspace,
		"agent_id":      "pi",
		"agent_version": "1",
		"state":         state,
		"config": map[string]any{
			"execution": map[string]any{
				"contract":        "2026-11-10.1",
				"scope":           "personal",
				"bot_id":          s.botID,
				"conversation_id": s.conversation,
				"harness_pin":     "dd30101cfc5b0609411dd80b6f93adbc14db14cf",
			},
		},
	}
	if reason != "" {
		body["reason"] = reason
	}
	if withRun {
		run := map[string]any{
			"id":     s.RunID,
			"state":  state,
			"cursor": cursor,
		}
		if state == "FAILED" {
			run["state"] = "FAILED"
		}
		if withExit {
			run["exit"] = map[string]any{"Code": 1, "Signal": ""}
		}
		body["run"] = run
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func (s *contractStandIn) logFrame() (string, error) {
	text := `{"kind":"pi","record":{"success":false,"error":"` + standErrorText + `"}}`
	payload, err := json.Marshal(map[string]any{
		"seq":    standLogSeq,
		"run_id": s.RunID,
		"type":   "log",
		"payload": map[string]any{
			"stream": "stdout",
			"text":   text,
		},
		"created_at": "2026-11-10T06:00:00Z",
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("id: %d\nevent: log\ndata: %s\n\n", standLogSeq, payload), nil
}

func (s *contractStandIn) snapshot() (posts int, cursors []string, paths []string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts, append([]string(nil), s.cursors...), append([]string(nil), s.paths...), append([]byte(nil), s.postBody...)
}

func bytesContainsOwner(raw []byte) bool {
	return strings.Contains(string(raw), `"owner_id"`)
}

func parseInt(v string) (int64, error) {
	if v == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int64
	_, err := fmt.Sscan(v, &n)
	return n, err
}

func TestLiveStandInAssociatesAndRecovers(t *testing.T) {
	if config.TestMakezBinding != store.TestBindingLabel {
		t.Fatal("test binding label drifted")
	}
	stand := startContractStandIn(t, "", "", false)
	cfg := config.Config{
		MakezAPIURL:      stand.URL,
		MakezAPIToken:    stand.Token,
		MakezWorkspaceID: stand.Workspace,
		MakezBinding:     config.TestMakezBinding,
		Credentials:      testCredentials(),
	}
	srv, pool := startProduction(t, cfg)
	health := asMap(t, mustGET(t, srv, "/v1/health"))
	if health["adapter"] != "unavailable" || health["makez"] != "disconnected" {
		t.Fatalf("health = %#v", health)
	}

	agent := createAgent(t, srv, "Ada")
	conv := openDefault(t, srv, agent)
	status, body := postSubmitBody(t, srv, testToken, conv, "desktop-submit-1", []byte(`{"content":"nope","owner_id":"client-owner","options":{}}`))
	if status != http.StatusBadRequest {
		t.Fatalf("client owner %d %s", status, body)
	}
	if posts, _, _, _ := stand.snapshot(); posts != 0 {
		t.Fatalf("client owner reached the stand-in, posts=%d", posts)
	}

	const content = "Ask the offline pi"
	status, body = postSubmit(t, srv, testToken, conv, "desktop-submit-1", content)
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, body)
	}
	first := asMap(t, body)
	if first["execution"] != "test-binding" || first["replayed"] != false {
		t.Fatalf("first = %#v", first)
	}
	if _, ok := first["assistantMessage"]; ok {
		t.Fatalf("running GET projected an assistant: %#v", first)
	}
	turn := first["turnId"].(string)
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE turn_id = $1 AND role = 'assistant'`, turn) != 0 {
		t.Fatal("assistant row before the terminal GET")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 {
		t.Fatal("status frame created a terminal observation")
	}
	var applied int64
	var processing, taskID, runID, workspace, owner string
	if err := pool.QueryRow(context.Background(), `
		SELECT c.applied_public_seq, c.processing_cursor, t.makez_task_id, t.makez_run_id, t.makez_workspace_id, t.owner_id
		FROM turns t
		JOIN source_checkpoints c ON c.task_id = t.makez_task_id AND c.run_id = t.makez_run_id
		WHERE t.id = $1`, turn).Scan(&applied, &processing, &taskID, &runID, &workspace, &owner); err != nil {
		t.Fatal(err)
	}
	if applied != standLogSeq || processing != standCursor || taskID != stand.TaskID || runID != stand.RunID || workspace != stand.Workspace || owner != testOwner || owner == workspace {
		t.Fatalf("link applied=%d cursor=%s task=%s run=%s ws=%s owner=%s", applied, processing, taskID, runID, workspace, owner)
	}
	if taskID == "ddeda2f7e3423d3e40d43f69a126afe7" {
		t.Fatal("stored the captured fixture task")
	}
	posts, cursors, _, postBody := stand.snapshot()
	if posts != 1 || len(cursors) != 1 || cursors[0] != "" {
		t.Fatalf("posts=%d cursors=%v", posts, cursors)
	}
	if strings.Contains(string(postBody), `"owner_id"`) {
		t.Fatalf("submit body carried owner_id: %s", postBody)
	}
	var sent struct {
		Prompt string `json:"prompt"`
		Config struct {
			Execution struct {
				BotID          string `json:"bot_id"`
				ConversationID string `json:"conversation_id"`
			} `json:"execution"`
		} `json:"config"`
	}
	if err := json.Unmarshal(postBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Prompt != content || sent.Config.Execution.BotID != agent || sent.Config.Execution.ConversationID != conv {
		t.Fatalf("submit = %#v", sent)
	}
	if got := stand.idempotency(); got != "turn/"+turn+"/submit" {
		t.Fatalf("makez key = %q", got)
	}

	restarted, err := New(pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(restarted.Handler)
	t.Cleanup(func() {
		restarted.Stop()
		srv2.Close()
	})
	status, body = postSubmit(t, srv2, testToken, conv, "desktop-submit-1", content)
	if status != http.StatusOK {
		t.Fatalf("restart %d %s", status, body)
	}
	second := asMap(t, body)
	if second["replayed"] != true || second["execution"] != "test-binding" || second["turnId"] != turn {
		t.Fatalf("restart response = %#v", second)
	}
	assistant := second["assistantMessage"].(map[string]any)
	if assistant["status"] != "error" || assistant["role"] != "assistant" {
		t.Fatalf("assistant = %#v", assistant)
	}
	errObj := assistant["error"].(map[string]any)
	if errObj["code"] != "AGENT_FAILED" || errObj["message"] != standErrorText {
		t.Fatalf("error = %#v", errObj)
	}
	if strings.Contains(errObj["message"].(string), "No API key found for the selected model") {
		t.Fatal("projected the captured fixture text")
	}
	messageSeq := number(t, assistant["seq"])
	if messageSeq == standLogSeq || messageSeq == 999 {
		t.Fatalf("message.seq = %d", messageSeq)
	}
	_, cursors, _, _ = stand.snapshot()
	if len(cursors) != 2 || cursors[1] != fmt.Sprintf("%d", standLogSeq) {
		t.Fatalf("resume cursors = %v", cursors)
	}

	status, body = postSubmit(t, srv2, testToken, conv, "desktop-submit-1", content)
	if status != http.StatusOK {
		t.Fatalf("replay %d %s", status, body)
	}
	third := asMap(t, body)
	again := third["assistantMessage"].(map[string]any)
	if again["id"] != assistant["id"] || third["turnId"] != turn {
		t.Fatalf("replay = %#v", third)
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE session_id = $1 AND role = 'assistant'`, conv) != 1 {
		t.Fatal("restart created a second assistant")
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE session_id = $1 AND role = 'user'`, conv) != 1 {
		t.Fatal("restart created a second user message")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 1 {
		t.Fatal("terminal observation was duplicated")
	}
	if scalar(t, pool, `SELECT count(*) FROM source_log_dedup WHERE run_id = $1`, stand.RunID) != 1 {
		t.Fatal("public log was stored more than once")
	}
	if scalar(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'source_offset'`) != 0 {
		t.Fatal("source_offset was frozen as a column")
	}
	high := number(t, snapshotOf(t, srv2, conv)["highWater"])
	if high == standLogSeq || messageSeq == applied || high == applied {
		t.Fatalf("cursors collapsed high=%d seq=%d applied=%d", high, messageSeq, applied)
	}
	posts, cursors, paths, _ := stand.snapshot()
	if posts != 1 {
		t.Fatalf("posts = %d paths = %v", posts, paths)
	}
	for _, path := range paths {
		if strings.Contains(path, "/ack") || strings.Contains(path, "owner") {
			t.Fatalf("unexpected request %s", path)
		}
	}
	if len(cursors) != 3 || cursors[2] != fmt.Sprintf("%d", standLogSeq) {
		t.Fatalf("third cursor = %v", cursors)
	}
	if events := eventsAfter(t, srv2, conv, 0); len(events) == 0 {
		t.Fatal("no business events")
	} else {
		var sawError bool
		for _, ev := range events {
			if ev["type"] == "message.completed" {
				t.Fatal("projected message.completed")
			}
			if ev["type"] == "message.error" {
				sawError = true
				if number(t, ev["cursor"]) == standLogSeq {
					t.Fatalf("business cursor used the public seq: %#v", ev)
				}
			}
		}
		if !sawError {
			t.Fatal("no message.error")
		}
	}
}

func (s *contractStandIn) idempotency() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idempotencyKey
}

func TestServiceBytesRollBackTogether(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	agent := createAgent(t, srv, "Ada")
	conv := openDefault(t, srv, agent)
	status, body := postSubmit(t, srv, testToken, conv, uuid.NewString(), "Hello from the fault stand-in")
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, body)
	}
	turn := asMap(t, body)["turnId"].(string)
	stand := startContractStandIn(t, conv, agent, true)
	events := standFetch(t, stand, http.MethodGet, "/v1/tasks/"+stand.TaskID+"/events")
	task := standFetch(t, stand, http.MethodGet, "/v1/tasks/"+stand.TaskID)
	if !bytesContains(events, "event: status\n") || bytesContains(events, "event: status\nid:") {
		t.Fatalf("status frame = %s", events)
	}
	if !bytesContains(task, `"state":"FAILED"`) || !bytesContains(task, `"reason":"AGENT_FAILED"`) {
		t.Fatalf("get = %s", task)
	}
	binding := store.LiveBinding{
		Label:          store.TestBindingLabel,
		OwnerID:        testOwner,
		ConversationID: conv,
		TurnID:         turn,
		BotID:          agent,
		WorkspaceID:    stand.Workspace,
	}
	st := store.New(pool)
	_, err := st.ProjectServiceRead(context.Background(), binding, store.ServiceRead{
		SSE: []byte("id: 4\nevent: status\ndata: {\"state\":\"FAILED\"}\n\n"),
	}, store.FaultNone)
	if err == nil || !strings.Contains(err.Error(), "sse id") {
		t.Fatalf("status id err = %v", err)
	}
	hint, err := st.ProjectServiceRead(context.Background(), binding, store.ServiceRead{
		SSE: []byte("event: status\ndata: {\"state\":\"FAILED\",\"reason\":\"AGENT_FAILED\"}\n\n"),
	}, store.FaultNone)
	if err != nil || !hint.StatusHintOnly || hint.MessageID != "" {
		t.Fatalf("status hint = %#v %v", hint, err)
	}
	succeeded, err := json.Marshal(map[string]any{
		"id": stand.TaskID, "workspace_id": stand.Workspace,
		"agent_id": "pi", "agent_version": "1", "state": "SUCCEEDED",
		"config": map[string]any{"execution": map[string]any{"conversation_id": conv}},
		"run":    map[string]any{"id": stand.RunID, "state": "SUCCEEDED", "cursor": standCursor},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ProjectServiceRead(context.Background(), binding, store.ServiceRead{AuthoritativeGET: succeeded}, store.FaultNone)
	if !errors.Is(err, store.ErrUnsupportedTerminal) {
		t.Fatalf("succeeded err = %v", err)
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE turn_id = $1 AND role = 'assistant'`, turn) != 0 {
		t.Fatal("refused reads created an assistant")
	}

	read := store.ServiceRead{SSE: events, AuthoritativeGET: task}
	_, err = st.ProjectServiceRead(context.Background(), binding, read, store.FaultCommit)
	var injected store.InjectedFault
	if !errors.As(err, &injected) || injected.Point != store.FaultCommit {
		t.Fatalf("fault = %v", err)
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE turn_id = $1 AND role = 'assistant'`, turn) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM source_checkpoints`) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM source_log_dedup`) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM outbox WHERE conversation_id = $1 AND kind = 'publish_business_event'`, conv) != 0 {
		t.Fatal("fault commit left a projection row")
	}
	result, err := st.ProjectServiceRead(context.Background(), binding, read, store.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.AppliedPublicSeq != standLogSeq || result.ProcessingCursor != standCursor {
		t.Fatalf("project = %#v", result)
	}
	again, err := store.New(pool).ProjectServiceRead(context.Background(), binding, read, store.FaultNone)
	if err != nil || !again.Replayed || again.MessageID != result.MessageID {
		t.Fatalf("replay = %#v %v", again, err)
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE turn_id = $1 AND role = 'assistant'`, turn) != 1 {
		t.Fatal("replay added an assistant")
	}
}

func standFetch(t *testing.T, stand *contractStandIn, method, path string) []byte {
	t.Helper()
	req, err := http.NewRequest(method, stand.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+stand.Token)
	req.Header.Set("X-Makez-Workspace-ID", stand.Workspace)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	return raw
}

func bytesContains(raw []byte, part string) bool {
	return strings.Contains(string(raw), part)
}
