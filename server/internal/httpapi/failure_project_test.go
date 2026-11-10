package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/store"
)

func TestCapturedFailureProjectsOneError(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conv, turn := seedUnavailableTurn(t, srv)
	before := effectCounts(t, pool, conv)
	cap := store.CapturedNoKeyFailure()
	st := store.New(pool)

	hint, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding: testBinding(conv, turn),
		SSE:     cap.SSEAfter0,
	}, store.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if hint.StatusHintOnly || hint.MessageID != "" {
		t.Fatalf("log plus status frame without a GET = %#v", hint)
	}
	if assistants(t, srv, conv) != 0 || errorEvents(t, srv, conv) != 0 {
		t.Fatal("status frame or log text projected a terminal row")
	}
	if scalar(t, pool, `SELECT count(*) FROM source_log_dedup WHERE run_id = $1`, storeRun()) != 1 {
		t.Fatal("public log was not stored once")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 {
		t.Fatal("status frame created a terminal observation")
	}

	result, err := st.ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || !result.Published || !result.LocalReceiptAcked {
		t.Fatalf("result = %#v", result)
	}
	if result.AppliedPublicSeq != 1 || result.ProcessingCursor != "stdout:378,stderr:0" {
		t.Fatalf("cursors = %#v", result)
	}
	if result.BusinessCursor == result.AppliedPublicSeq || result.BusinessCursor == 378 {
		t.Fatalf("business cursor = %d public seq = %d", result.BusinessCursor, result.AppliedPublicSeq)
	}
	assertOneFailure(t, srv, pool, conv, turn)
	if effectCounts(t, pool, conv).turns != before.turns || effectCounts(t, pool, conv).wakes != before.wakes {
		t.Fatal("projection created another turn or wake")
	}
	if scalar(t, pool, `SELECT count(*) FROM outbox WHERE conversation_id = $1 AND kind = 'submit'`, conv) != 1 {
		t.Fatal("submit outbox changed")
	}
	if scalar(t, pool, `SELECT count(*) FROM source_log_dedup WHERE run_id = $1`, storeRun()) != 1 {
		t.Fatal("duplicate source log was stored")
	}
	if scalar(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'source_offset'`) != 0 {
		t.Fatal("source_offset was frozen as a column")
	}

	again, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.MessageID != result.MessageID {
		t.Fatalf("restart = %#v", again)
	}
	assertOneFailure(t, srv, pool, conv, turn)

	extra := appendSeq(t, cap.PublicLog, 2, "not in the capture")
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		DurableLogs:      extra,
		AuthoritativeGET: cap.TaskGET,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	if scalar(t, pool, `SELECT count(*) FROM source_log_dedup WHERE run_id = $1`, storeRun()) != 1 {
		t.Fatal("append after terminal was stored")
	}
	assertOneFailure(t, srv, pool, conv, turn)

	mutated := mutateTask(t, cap.TaskGET, func(body map[string]any) {
		body["state"] = "SUCCEEDED"
		body["reason"] = "SHOULD_NOT_REPLACE"
		body["owner_id"] = "client-owner"
		body["source_offset"] = 378
	})
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		AuthoritativeGET: mutated,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	assertOneFailure(t, srv, pool, conv, turn)
	var owner, workspace string
	if err := pool.QueryRow(context.Background(), `SELECT owner_id, makez_workspace_id FROM turns WHERE id = $1`, turn).Scan(&owner, &workspace); err != nil {
		t.Fatal(err)
	}
	if owner != testOwner || workspace != "api-test-88c8390626d97c5e675871cc" || owner == workspace {
		t.Fatalf("owner = %s workspace = %s", owner, workspace)
	}
}

func TestStatusAndNonTerminalDoNotSucceed(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	cap := store.CapturedNoKeyFailure()
	st := store.New(pool)

	conv, turn := seedUnavailableTurn(t, srv)
	hint, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding: testBinding(conv, turn),
		SSE:     cap.SSELastEvent,
	}, store.FaultNone)
	if err != nil || !hint.StatusHintOnly {
		t.Fatalf("hint = %#v err = %v", hint, err)
	}
	if assistants(t, srv, conv) != 0 || errorEvents(t, srv, conv) != 0 || effectCounts(t, pool, conv).messages != 1 {
		t.Fatal("no-id status changed the conversation")
	}

	conv, turn = seedUnavailableTurn(t, srv)
	raw, err := json.Marshal([]map[string]any{{
		"seq": 1, "run_id": "run-arbitrary", "type": "log",
		"payload":    map[string]any{"stream": "stdout", "text": "all done"},
		"created_at": "2026-11-10T04:23:40Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:     testBinding(conv, turn),
		DurableLogs: raw,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	if assistants(t, srv, conv) != 0 {
		t.Fatal("arbitrary text created an assistant")
	}
	for _, item := range messageList(t, srv, conv) {
		if item["status"] == "complete" && item["role"] != "user" {
			t.Fatalf("non-user success = %#v", item)
		}
	}

	conv, turn = seedUnavailableTurn(t, srv)
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		AuthoritativeGET: cap.TaskPreparing,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	if assistants(t, srv, conv) != 0 || scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 {
		t.Fatal("preparing GET projected a terminal row")
	}

	conv, turn = seedUnavailableTurn(t, srv)
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		AuthoritativeGET: mutateTask(t, cap.TaskGET, func(body map[string]any) { body["state"] = "SUCCEEDED" }),
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	if assistants(t, srv, conv) != 0 || errorEvents(t, srv, conv) != 0 {
		t.Fatal("SUCCEEDED body declared success")
	}
}

func TestConflictingPiFailureDoesNotReplaceTheStoredError(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conv, turn := seedUnavailableTurn(t, srv)
	cap := store.CapturedNoKeyFailure()
	st := store.New(pool)
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:     testBinding(conv, turn),
		DurableLogs: cap.PublicLog,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	// Same public seq, and this body is itself a Pi failure. It must not
	// become the assistant error. The stored log text stays the message.
	replacement := `{"kind":"pi","record":{"id":"m1","type":"response","command":"prompt","success":false,"error":"REPLACED"}}`
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		DurableLogs:      appendSeq(t, cap.PublicLog, 1, replacement),
		AuthoritativeGET: cap.TaskGET,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	assertOneFailure(t, srv, pool, conv, turn)
	var text string
	if err := pool.QueryRow(context.Background(), `
		SELECT failure_text FROM source_log_dedup WHERE run_id = $1 AND public_seq = 1`, storeRun(),
	).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "No API key found for the selected model.") || strings.Contains(text, "REPLACED") {
		t.Fatalf("stored text = %q", text)
	}
}

func TestLogThenTerminalKeepsTheCapturedError(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conv, turn := seedUnavailableTurn(t, srv)
	cap := store.CapturedNoKeyFailure()
	st := store.New(pool)
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:     testBinding(conv, turn),
		DurableLogs: cap.PublicLog,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	changed := appendSeq(t, cap.PublicLog, 1, "different text")
	// Same public seq, different text. The capture's duplicate page stayed seq 1.
	// This call is the conflicting body, which must not replace the stored text.
	var rows []map[string]any
	if err := json.Unmarshal(changed, &rows); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:     testBinding(conv, turn),
		DurableLogs: changed,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := pool.QueryRow(context.Background(), `
		SELECT failure_text FROM source_log_dedup WHERE run_id = $1 AND public_seq = 1`, storeRun(),
	).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "No API key found for the selected model.") {
		t.Fatalf("stored text replaced: %q", text)
	}
	if _, err := st.ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          testBinding(conv, turn),
		AuthoritativeGET: cap.TaskGET,
	}, store.FaultNone); err != nil {
		t.Fatal(err)
	}
	assertOneFailure(t, srv, pool, conv, turn)
}

func TestUnlabeledBindingWritesNothing(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conv, turn := seedUnavailableTurn(t, srv)
	binding := testBinding(conv, turn)
	binding.Label = "owner-workspace"
	_, err := store.New(pool).ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          binding,
		AuthoritativeGET: store.CapturedNoKeyFailure().TaskGET,
	}, store.FaultNone)
	if !errors.Is(err, store.ErrUnlabeledBinding) {
		t.Fatalf("err = %v", err)
	}
	binding = testBinding(conv, turn)
	binding.CapturedConversationID = "other-conversation"
	_, err = store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultNone)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(pool).ProjectCapturedFailure(context.Background(), store.CaptureInput{
		Binding:          binding,
		AuthoritativeGET: store.CapturedNoKeyFailure().TaskGET,
	}, store.FaultNone)
	if !errors.Is(err, store.ErrBindingMismatch) {
		t.Fatalf("mismatch err = %v", err)
	}
	if assistants(t, srv, conv) != 1 {
		t.Fatal("rejected binding changed the transcript")
	}
}

func TestT14FaultPoints(t *testing.T) {
	preCommit := []store.FaultPoint{
		store.FaultProjection,
		store.FaultDedup,
		store.FaultCheckpoint,
		store.FaultOutbox,
		store.FaultCommit,
	}
	for _, point := range preCommit {
		t.Run(string(point), func(t *testing.T) {
			srv, pool := startProduction(t, config.Config{})
			conv, turn := seedUnavailableTurn(t, srv)
			_, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), point)
			var injected store.InjectedFault
			if !errors.As(err, &injected) || injected.Point != point {
				t.Fatalf("err = %v", err)
			}
			if assistants(t, srv, conv) != 0 || errorEvents(t, srv, conv) != 0 {
				t.Fatal("fault before commit left a projection")
			}
			if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 ||
				scalar(t, pool, `SELECT count(*) FROM source_checkpoints`) != 0 ||
				scalar(t, pool, `SELECT count(*) FROM source_log_dedup`) != 0 ||
				scalar(t, pool, `SELECT count(*) FROM projection_receipts`) != 0 ||
				scalar(t, pool, `SELECT count(*) FROM outbox WHERE kind = 'publish_business_event'`) != 0 {
				t.Fatal("fault before commit left projection rows")
			}
			var taskID *string
			if err := pool.QueryRow(context.Background(), `SELECT makez_task_id FROM turns WHERE id = $1`, turn).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			if taskID != nil {
				t.Fatal("turn was linked before commit")
			}
		})
	}

	t.Run("publish", func(t *testing.T) {
		srv, pool := startProduction(t, config.Config{})
		conv, turn := seedUnavailableTurn(t, srv)
		_, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultPublish)
		var injected store.InjectedFault
		if !errors.As(err, &injected) || injected.Point != store.FaultPublish {
			t.Fatalf("err = %v", err)
		}
		assertCommittedUnpublished(t, srv, pool, conv)
		if _, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultNone); err != nil {
			t.Fatal(err)
		}
		assertOneFailure(t, srv, pool, conv, turn)
	})

	t.Run("source_ack", func(t *testing.T) {
		srv, pool := startProduction(t, config.Config{})
		conv, turn := seedUnavailableTurn(t, srv)
		_, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultSourceAck)
		var injected store.InjectedFault
		if !errors.As(err, &injected) || injected.Point != store.FaultSourceAck {
			t.Fatalf("err = %v", err)
		}
		if !published(t, pool, conv) || acked(t, pool) {
			t.Fatal("source ack fault did not stop between publish and the local receipt")
		}
		assertOneErrorRow(t, srv, conv)
		if _, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultNone); err != nil {
			t.Fatal(err)
		}
		assertOneFailure(t, srv, pool, conv, turn)
	})
}

func seedUnavailableTurn(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	agent := createAgent(t, srv, "Ada")
	conv := openDefault(t, srv, agent)
	status, body := postSubmit(t, srv, testToken, conv, uuid.NewString(), "Hello")
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, body)
	}
	parsed := asMap(t, body)
	if parsed["execution"] != "unavailable" {
		t.Fatalf("execution = %#v", parsed["execution"])
	}
	return conv, parsed["turnId"].(string)
}

func testBinding(conv, turn string) store.TestBinding {
	return store.TestBinding{
		Label:                  store.TestBindingLabel,
		OwnerID:                testOwner,
		ConversationID:         conv,
		TurnID:                 turn,
		CapturedConversationID: "conv-wire",
	}
}

func fullCapture(conv, turn string) store.CaptureInput {
	cap := store.CapturedNoKeyFailure()
	return store.CaptureInput{
		Binding:          testBinding(conv, turn),
		DurableLogs:      cap.PublicLog,
		SSE:              cap.SSEAfter0,
		AuthoritativeGET: cap.TaskGET,
	}
}

func storeRun() string {
	return "5a1ac4ffa210a3ba5d805343e1b0b1a7"
}

func assertOneFailure(t *testing.T, srv *httptest.Server, pool *pgxpool.Pool, conv, turn string) {
	t.Helper()
	assertOneErrorRow(t, srv, conv)
	snap := snapshotOf(t, srv, conv)
	high := number(t, snap["highWater"])
	if high == 378 || high <= 1 {
		t.Fatalf("highWater = %d", high)
	}
	events := eventsAfter(t, srv, conv, 0)
	var errorCursor int64
	for _, ev := range events {
		if ev["type"] == "message.error" {
			errorCursor = number(t, ev["cursor"])
			if ev["status"] != "error" || ev["code"] != "AGENT_FAILED" {
				t.Fatalf("event = %#v", ev)
			}
			if !strings.HasPrefix(ev["message"].(string), "No API key found for the selected model.") {
				t.Fatalf("event message = %#v", ev["message"])
			}
		}
		if ev["type"] == "message.completed" {
			t.Fatal("projection emitted message.completed")
		}
	}
	if errorCursor != high {
		t.Fatalf("error cursor %d highWater %d", errorCursor, high)
	}
	if len(eventsAfter(t, srv, conv, high)) != 0 {
		t.Fatal("events after the snapshot watermark were not empty")
	}
	var applied int64
	var processing string
	if err := pool.QueryRow(context.Background(), `
		SELECT applied_public_seq, processing_cursor FROM source_checkpoints
		WHERE task_id = $1`, "ddeda2f7e3423d3e40d43f69a126afe7").Scan(&applied, &processing); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || processing != "stdout:378,stderr:0" || applied == high {
		t.Fatalf("checkpoint seq = %d cursor = %q highWater = %d", applied, processing, high)
	}
	var messageSeq int64
	if err := pool.QueryRow(context.Background(), `
		SELECT seq FROM messages WHERE turn_id = $1 AND role = 'assistant'`, turn).Scan(&messageSeq); err != nil {
		t.Fatal(err)
	}
	if messageSeq == 378 || messageSeq == applied {
		t.Fatalf("message.seq = %d public seq = %d", messageSeq, applied)
	}
	if !published(t, pool, conv) || !acked(t, pool) {
		t.Fatal("publish or local receipt was not recorded")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 1 {
		t.Fatal("terminal observation was duplicated")
	}
}

func assertOneErrorRow(t *testing.T, srv *httptest.Server, conv string) {
	t.Helper()
	if assistants(t, srv, conv) != 1 || errorEvents(t, srv, conv) != 1 {
		t.Fatalf("assistants = %d errors = %d", assistants(t, srv, conv), errorEvents(t, srv, conv))
	}
	var user, assistant map[string]any
	for _, item := range messageList(t, srv, conv) {
		switch item["role"] {
		case "user":
			user = item
		case "assistant":
			assistant = item
		}
	}
	if user["content"] != "Hello" || user["status"] != "complete" {
		t.Fatalf("user = %#v", user)
	}
	if assistant["status"] != "error" || assistant["content"] != "" {
		t.Fatalf("assistant = %#v", assistant)
	}
	errObj, _ := assistant["error"].(map[string]any)
	if errObj["code"] != "AGENT_FAILED" || !strings.HasPrefix(errObj["message"].(string), "No API key found for the selected model.") {
		t.Fatalf("error = %#v", assistant["error"])
	}
	if number(t, assistant["seq"]) == 378 {
		t.Fatal("message.seq used the process cursor")
	}
}

func assertCommittedUnpublished(t *testing.T, srv *httptest.Server, pool *pgxpool.Pool, conv string) {
	t.Helper()
	assertOneErrorRow(t, srv, conv)
	if published(t, pool, conv) || acked(t, pool) {
		t.Fatal("publish fault left the outbox published or the receipt acked")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 1 ||
		scalar(t, pool, `SELECT count(*) FROM source_checkpoints`) != 1 ||
		scalar(t, pool, `SELECT count(*) FROM outbox WHERE conversation_id = $1 AND kind = 'publish_business_event'`, conv) != 1 {
		t.Fatal("publish fault did not keep projection, dedup, checkpoint, and outbox together")
	}
}

func assistants(t *testing.T, srv *httptest.Server, conv string) int {
	t.Helper()
	n := 0
	for _, item := range messageList(t, srv, conv) {
		if item["role"] == "assistant" {
			n++
		}
	}
	return n
}

func errorEvents(t *testing.T, srv *httptest.Server, conv string) int {
	t.Helper()
	n := 0
	for _, ev := range eventsAfter(t, srv, conv, 0) {
		if ev["type"] == "message.error" {
			n++
		}
	}
	return n
}

func snapshotOf(t *testing.T, srv *httptest.Server, conv string) map[string]any {
	t.Helper()
	return asMap(t, mustGET(t, srv, "/v1/sessions/"+conv+"/snapshot"))
}

func published(t *testing.T, pool *pgxpool.Pool, conv string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(), `
		SELECT published_at IS NOT NULL FROM outbox
		WHERE conversation_id = $1 AND kind = 'publish_business_event'`, conv).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func acked(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(), `
		SELECT acked_at IS NOT NULL FROM projection_receipts
		WHERE task_id = $1`, "ddeda2f7e3423d3e40d43f69a126afe7").Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func scalar(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func number(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		t.Fatalf("number = %#v", v)
		return 0
	}
}

func mutateTask(t *testing.T, raw []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	edit(body)
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func appendSeq(t *testing.T, raw []byte, seq int, text string) []byte {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	next := map[string]any{}
	for k, v := range rows[0] {
		next[k] = v
	}
	next["seq"] = seq
	payload := map[string]any{}
	for k, v := range rows[0]["payload"].(map[string]any) {
		payload[k] = v
	}
	payload["text"] = text
	next["payload"] = payload
	if seq == 1 {
		rows[0] = next
	} else {
		rows = append(rows, next)
	}
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
