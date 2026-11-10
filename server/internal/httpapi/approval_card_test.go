package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/auth"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/store"
)

const otherToken = "other-token"

func TestApprovalCardBindsSnapshotAndRequester(t *testing.T) {
	cfg := config.Config{
		Credentials: []auth.Credential{
			{Token: testToken, ActorID: testActor, OwnerID: testOwner},
			{Token: otherToken, ActorID: "actor-other", OwnerID: "owner-other"},
		},
		DevChunkDelay: 10 * time.Millisecond,
	}
	srv, pool := startConfigured(t, cfg, true)
	sessionID := createSession(t, srv, createAgent(t, srv, "Ada"))
	ws := subscribe(t, srv, sessionID)

	status, body := do(t, srv, http.MethodPost, "/v1/sessions/"+sessionID+"/messages", map[string]any{
		"content": "/draft send the weekly note",
	})
	if status != http.StatusOK {
		t.Fatalf("draft %d %s", status, body)
	}
	ws.until(t, "approval.requested", 5*time.Second)
	cards := approvalList(t, srv, sessionID)
	if len(cards) != 1 {
		t.Fatalf("cards = %#v", cards)
	}
	s1 := cards[0]
	s1ID := s1["id"].(string)
	if s1["status"] != "pending" || s1["requesterActorId"] != testActor {
		t.Fatalf("card = %#v", s1)
	}
	if kind, summary := snapSummary(t, s1); kind != "draft_action" || summary != "send the weekly note" {
		t.Fatalf("snapshot = %s %s", kind, summary)
	}
	held := cardEffects(t, pool, sessionID)
	if held.tasks != 0 {
		t.Fatalf("task before a decision: %+v", held)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s1ID+"/edit", editBody("send the weekly note"))
	if status != http.StatusOK {
		t.Fatalf("same snapshot %d %s", status, body)
	}
	if asMap(t, body)["id"] != s1ID || asMap(t, body)["requesterActorId"] != testActor {
		t.Fatalf("same snapshot body = %s", body)
	}
	if cards = approvalList(t, srv, sessionID); len(cards) != 1 || cards[0]["status"] != "pending" {
		t.Fatalf("same snapshot cards = %#v", cards)
	}

	status, body = postApproval(t, srv, "", "/v1/approvals/"+s1ID+"/edit", editBody("send the stolen note"))
	if status != http.StatusUnauthorized || errCode(t, body) != "authentication_required" {
		t.Fatalf("edit without bearer %d %s", status, body)
	}
	status, body = postApproval(t, srv, otherToken, "/v1/approvals/"+s1ID+"/edit", editBody("send the stolen note"))
	if status != http.StatusForbidden || errCode(t, body) != "permission_denied" {
		t.Fatalf("edit by other owner %d %s", status, body)
	}
	if cards = approvalList(t, srv, sessionID); len(cards) != 1 || cards[0]["status"] != "pending" {
		t.Fatalf("unauthorized edit cards = %#v", cards)
	}
	if _, summary := snapSummary(t, cards[0]); summary != "send the weekly note" {
		t.Fatalf("unauthorized edit snapshot = %s", summary)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s1ID+"/edit", editBody("send the revised note"))
	if status != http.StatusOK {
		t.Fatalf("edit %d %s", status, body)
	}
	s2 := asMap(t, body)
	s2ID := s2["id"].(string)
	if s2ID == s1ID || s2["status"] != "pending" || s2["requesterActorId"] != testActor {
		t.Fatalf("new card = %#v", s2)
	}
	if kind, summary := snapSummary(t, s2); kind != "draft_action" || summary != "send the revised note" {
		t.Fatalf("new snapshot = %s %s", kind, summary)
	}
	cards = approvalList(t, srv, sessionID)
	if len(cards) != 2 {
		t.Fatalf("cards = %#v", cards)
	}
	old := findApproval(t, cards, s1ID)
	if old["status"] != "invalid" || old["requesterActorId"] != testActor {
		t.Fatalf("old card = %#v", old)
	}
	if _, summary := snapSummary(t, old); summary != "send the weekly note" {
		t.Fatalf("old snapshot = %s", summary)
	}
	if cardEffects(t, pool, sessionID).withoutTasks() != held.withoutTasks() {
		t.Fatalf("edit added a side effect: before %+v after %+v", held, cardEffects(t, pool, sessionID))
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s1ID+"/edit", editBody("send another note"))
	if status != http.StatusConflict || errCode(t, body) != "approval_not_pending" {
		t.Fatalf("edit invalid card %d %s", status, body)
	}
	if cards = approvalList(t, srv, sessionID); len(cards) != 2 {
		t.Fatalf("stale edit cards = %#v", cards)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s1ID+"/decision", decideBody("approve"))
	if status != http.StatusConflict || errCode(t, body) != "approval_not_pending" {
		t.Fatalf("approve old card %d %s", status, body)
	}
	if got := cardEffects(t, pool, sessionID); got.tasks != 0 || got.withoutTasks() != held.withoutTasks() {
		t.Fatalf("old decision stored a side effect: %+v", got)
	}

	status, body = postApproval(t, srv, otherToken, "/v1/approvals/"+s2ID+"/decision", decideBody("approve"))
	if status != http.StatusForbidden || errCode(t, body) != "permission_denied" {
		t.Fatalf("other owner %d %s", status, body)
	}
	status, body = postApproval(t, srv, "", "/v1/approvals/"+s2ID+"/decision", map[string]any{
		"decision":  "approve",
		"owner":     testOwner,
		"owner_id":  testOwner,
		"requester": testActor,
	})
	if status != http.StatusUnauthorized || errCode(t, body) != "authentication_required" {
		t.Fatalf("body owner %d %s", status, body)
	}
	cards = approvalList(t, srv, sessionID)
	if findApproval(t, cards, s2ID)["status"] != "pending" || findApproval(t, cards, s1ID)["status"] != "invalid" {
		t.Fatalf("after rejection %#v", cards)
	}
	if got := cardEffects(t, pool, sessionID); got.tasks != 0 || got.withoutTasks() != held.withoutTasks() {
		t.Fatalf("rejected decision stored a side effect: %+v", got)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s2ID+"/decision", decideBody("approve"))
	if status != http.StatusOK {
		t.Fatalf("approve %d %s", status, body)
	}
	approved := asMap(t, body)
	if approved["status"] != "approved" || approved["requesterActorId"] != testActor {
		t.Fatalf("approved = %#v", approved)
	}
	if _, summary := snapSummary(t, approved); summary != "send the revised note" {
		t.Fatalf("approve applied the body snapshot: %s", summary)
	}
	taskID := waitSessionTask(t, pool, sessionID, s2ID)
	cards = approvalList(t, srv, sessionID)
	if findApproval(t, cards, s1ID)["status"] != "invalid" {
		t.Fatalf("s1 authorized s2: %#v", cards)
	}

	widen(t, pool, taskID, "send everything")
	cards = approvalList(t, srv, sessionID)
	if findApproval(t, cards, s2ID)["status"] != "approved" || findApproval(t, cards, s2ID)["requesterActorId"] != testActor {
		t.Fatalf("worker result changed the card: %#v", cards)
	}
	if _, summary := snapSummary(t, findApproval(t, cards, s2ID)); summary != "send the revised note" {
		t.Fatalf("worker result widened the snapshot to %s", summary)
	}
	if ownersNamed(t, pool, "client-owner") != 0 || cardEffects(t, pool, sessionID).tasks != 1 {
		t.Fatalf("worker result added a grant")
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s2ID+"/edit", editBody("send the final note"))
	if status != http.StatusOK {
		t.Fatalf("edit approved %d %s", status, body)
	}
	s3 := asMap(t, body)
	s3ID := s3["id"].(string)
	if s3ID == s2ID || s3["status"] != "pending" || s3["requesterActorId"] != testActor {
		t.Fatalf("s3 = %#v", s3)
	}
	if _, summary := snapSummary(t, s3); summary != "send the final note" {
		t.Fatalf("s3 snapshot = %s", summary)
	}
	cards = approvalList(t, srv, sessionID)
	if len(cards) != 3 || findApproval(t, cards, s2ID)["status"] != "invalid" {
		t.Fatalf("after second edit %#v", cards)
	}
	if _, summary := snapSummary(t, findApproval(t, cards, s2ID)); summary != "send the revised note" {
		t.Fatalf("approved snapshot changed: %s", summary)
	}
	if taskStill(t, pool, sessionID, taskID, s2ID) != 1 {
		t.Fatal("edit retargeted the task")
	}

	widen(t, pool, taskID, "send the final note")
	cards = approvalList(t, srv, sessionID)
	if findApproval(t, cards, s3ID)["status"] != "pending" || findApproval(t, cards, s3ID)["requesterActorId"] != testActor {
		t.Fatalf("worker result approved the new card: %#v", findApproval(t, cards, s3ID))
	}
	if findApproval(t, cards, s1ID)["status"] != "invalid" || findApproval(t, cards, s2ID)["status"] != "invalid" {
		t.Fatalf("worker result restored a card: %#v", cards)
	}
	if taskStill(t, pool, sessionID, taskID, s2ID) != 1 {
		t.Fatal("worker result added a task")
	}
	if ownersNamed(t, pool, "client-owner") != 0 {
		t.Fatal("worker result stored a client owner")
	}
	if got := cardEffects(t, pool, sessionID); got.withoutTasks() != held.withoutTasks() {
		t.Fatalf("side effects changed: before %+v after %+v", held, got)
	}

	status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+s1ID+"/decision", decideBody("approve"))
	if status != http.StatusConflict || errCode(t, body) != "approval_not_pending" {
		t.Fatalf("s1 still authorizes %d %s", status, body)
	}
	if taskStill(t, pool, sessionID, taskID, s2ID) != 1 {
		t.Fatal("s1 decision created a task for the edited action")
	}
}

func TestUnsetFixtureLeavesDraftUnavailable(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	status, body := postSubmit(t, srv, testToken, conversationID, "draft-unset", "/draft send the weekly note")
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, body)
	}
	got := asMap(t, body)
	if got["execution"] != "unavailable" {
		t.Fatalf("execution = %#v", got["execution"])
	}
	if _, ok := got["assistantMessage"]; ok {
		t.Fatalf("unset fixture started a reply: %s", body)
	}
	var approvals int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE session_id = $1`, conversationID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatalf("approvals = %d", approvals)
	}
}

func editBody(summary string) map[string]any {
	return map[string]any{
		"summary":   summary,
		"owner":     "client-owner",
		"owner_id":  "client-owner",
		"requester": "actor-other",
	}
}

func decideBody(decision string) map[string]any {
	return map[string]any{
		"decision":       decision,
		"owner":          "client-owner",
		"owner_id":       "client-owner",
		"requester":      "actor-other",
		"actionSnapshot": map[string]any{"kind": "draft_action", "summary": "send everything"},
	}
}

func postApproval(t *testing.T, srv *httptest.Server, token, path string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func approvalList(t *testing.T, srv *httptest.Server, sessionID string) []map[string]any {
	t.Helper()
	status, body := do(t, srv, http.MethodGet, "/v1/approvals?sessionId="+sessionID, nil)
	if status != http.StatusOK {
		t.Fatalf("approvals %d %s", status, body)
	}
	raw := asMap(t, body)["approvals"].([]any)
	out := make([]map[string]any, len(raw))
	for i, item := range raw {
		out[i] = item.(map[string]any)
	}
	return out
}

func findApproval(t *testing.T, list []map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range list {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("missing approval %s", id)
	return nil
}

func snapSummary(t *testing.T, item map[string]any) (string, string) {
	t.Helper()
	snap, ok := item["actionSnapshot"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot = %#v", item["actionSnapshot"])
	}
	kind, _ := snap["kind"].(string)
	summary, _ := snap["summary"].(string)
	return kind, summary
}

type cardEffect struct {
	approvals int
	tasks     int
	wakes     int
	outbox    int
	messages  int
	turns     int
}

func (e cardEffect) withoutTasks() cardEffect {
	e.tasks = 0
	e.approvals = 0
	return e
}

func cardEffects(t *testing.T, pool *pgxpool.Pool, sessionID string) cardEffect {
	t.Helper()
	var got cardEffect
	err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM approvals WHERE session_id = $1),
			(SELECT count(*) FROM tasks WHERE session_id = $1),
			(SELECT count(*) FROM wake_intents WHERE conversation_id = $1),
			(SELECT count(*) FROM outbox WHERE conversation_id = $1),
			(SELECT count(*) FROM messages WHERE session_id = $1),
			(SELECT count(*) FROM turns WHERE conversation_id = $1)`,
		sessionID).Scan(&got.approvals, &got.tasks, &got.wakes, &got.outbox, &got.messages, &got.turns)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func waitSessionTask(t *testing.T, pool *pgxpool.Pool, sessionID, approvalID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var id, gotApproval string
		err := pool.QueryRow(context.Background(), `
			SELECT id, approval_id FROM tasks WHERE session_id = $1`, sessionID).Scan(&id, &gotApproval)
		if err == nil {
			if gotApproval != approvalID {
				t.Fatalf("task approval = %s, want %s", gotApproval, approvalID)
			}
			return id
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("task was not stored")
	return ""
}

func taskStill(t *testing.T, pool *pgxpool.Pool, sessionID, taskID, approvalID string) int {
	t.Helper()
	var n int
	var gotApproval string
	err := pool.QueryRow(context.Background(), `
		SELECT count(*), coalesce(min(approval_id), '')
		FROM tasks WHERE session_id = $1`, sessionID).Scan(&n, &gotApproval)
	if err != nil {
		t.Fatal(err)
	}
	if n == 1 && gotApproval != approvalID {
		t.Fatalf("task approval = %s", gotApproval)
	}
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM tasks WHERE session_id = $1`, sessionID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != taskID {
		t.Fatalf("task id = %s", stored)
	}
	return n
}

func widen(t *testing.T, pool *pgxpool.Pool, taskID, summary string) {
	t.Helper()
	snapshot, err := json.Marshal(map[string]any{"kind": "draft_action", "summary": summary})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.New(pool).RecordWorkerResult(context.Background(), taskID, store.WorkerClaim{
		Text:     "approved snapshot " + summary,
		Snapshot: snapshot,
		Decision: "approve",
		OwnerID:  "client-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func ownersNamed(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM owners WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
