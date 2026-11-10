package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/store"
)

// TestUserMessageStoredWhileFixtureTaskRuns posts through the session API
// while a labeled fixture task is running. The task is not a Makez run and
// not a model reply. An unset fixture stays unavailable.
func TestUserMessageStoredWhileFixtureTaskRuns(t *testing.T) {
	t.Run("running fixture task", func(t *testing.T) {
		srv, pool := startProduction(t, config.Config{BusinessFixture: true})
		assertUnavailableHealth(t, srv)
		conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))

		status, body := postFixture(t, srv, map[string]any{
			"source":         store.FixtureSource,
			"conversationId": conversationID,
			"eventKey":       "running-task-1",
			"type":           "fixture.running-task",
		})
		if status != http.StatusOK {
			t.Fatalf("fixture task %d %s", status, body)
		}
		planted := asMap(t, body)
		if planted["type"] != "fixture.running-task" || planted["source"] != store.FixtureSource {
			t.Fatalf("fixture event = %#v", planted)
		}
		plantedTask, _ := planted["task"].(map[string]any)
		taskID, _ := plantedTask["id"].(string)
		if taskID == "" || plantedTask["status"] != "running" || plantedTask["source"] != store.FixtureSource || plantedTask["result"] != "" {
			t.Fatalf("planted task = %#v", plantedTask)
		}
		againStatus, againBody := postFixture(t, srv, map[string]any{
			"source":         store.FixtureSource,
			"conversationId": conversationID,
			"eventKey":       "running-task-1",
			"type":           "fixture.running-task",
		})
		if againStatus != http.StatusOK || asMap(t, againBody)["id"] != planted["id"] {
			t.Fatalf("repeat fixture %d %s", againStatus, againBody)
		}

		before := taskList(t, srv, conversationID)
		if len(before) != 1 || before[0]["id"] != taskID || before[0]["status"] != "running" || before[0]["source"] != store.FixtureSource {
			t.Fatalf("tasks before send = %#v", before)
		}
		anchor := messageList(t, srv, conversationID)
		if streamingCount(anchor) != 0 {
			t.Fatal("fixture task left a streaming reply")
		}
		assertNoModelReply(t, anchor)
		if len(anchor) != 1 || anchor[0]["role"] != "assistant" || anchor[0]["status"] != "error" || anchor[0]["content"] != "" {
			t.Fatalf("fixture anchor = %#v", anchor)
		}
		meta, _ := anchor[0]["metadata"].(map[string]any)
		if meta["adapter"] != store.FixtureSource {
			t.Fatalf("fixture anchor adapter = %#v", anchor[0]["metadata"])
		}

		const firstKey = "while-task-1"
		const firstText = "still here"
		status, body = postSubmit(t, srv, testToken, conversationID, firstKey, firstText)
		if status == http.StatusConflict && errCode(t, body) == "reply_in_progress" {
			t.Fatalf("rejected because a task is running: %s", body)
		}
		if status != http.StatusOK {
			t.Fatalf("first send %d %s", status, body)
		}
		first := asMap(t, body)
		if first["execution"] != "unavailable" || first["replayed"] != false {
			t.Fatalf("first submit = %#v", first)
		}
		if _, ok := first["assistantMessage"]; ok {
			t.Fatalf("running task started a reply: %s", body)
		}
		user, _ := first["userMessage"].(map[string]any)
		if user["role"] != "user" || user["content"] != firstText || user["status"] != "complete" {
			t.Fatalf("user message = %#v", user)
		}
		turnID, _ := first["turnId"].(string)
		if turnID == "" || turnID == firstKey {
			t.Fatalf("turn id = %q", turnID)
		}

		const secondText = "and again"
		status, body = postSubmit(t, srv, testToken, conversationID, "while-task-2", secondText)
		if status != http.StatusOK {
			t.Fatalf("second send %d %s", status, body)
		}
		second := asMap(t, body)
		if second["execution"] != "unavailable" {
			t.Fatalf("second submit = %#v", second)
		}
		if _, ok := second["assistantMessage"]; ok {
			t.Fatalf("second send started a reply: %s", body)
		}
		secondUser, _ := second["userMessage"].(map[string]any)
		if secondUser["content"] != secondText || secondUser["status"] != "complete" {
			t.Fatalf("second user message = %#v", secondUser)
		}

		status, body = postSubmit(t, srv, testToken, conversationID, firstKey, firstText)
		if status != http.StatusOK {
			t.Fatalf("replay %d %s", status, body)
		}
		replay := asMap(t, body)
		if replay["turnId"] != turnID || replay["replayed"] != true || replay["execution"] != "unavailable" {
			t.Fatalf("replay = %#v", replay)
		}

		after := taskList(t, srv, conversationID)
		if len(after) != 1 || after[0]["id"] != taskID || after[0]["status"] != "running" || after[0]["source"] != store.FixtureSource || after[0]["result"] != "" {
			t.Fatalf("task changed = %#v", after)
		}
		assertTaskStillRunning(t, pool, conversationID, taskID)
		messages := messageList(t, srv, conversationID)
		assertNoModelReply(t, messages)
		if userCount(messages, firstText) != 1 || userCount(messages, secondText) != 1 {
			t.Fatalf("stored messages = %#v", messages)
		}
		var linked, checkpoints, wakes, outbox, unavailableOutbox int
		if err := pool.QueryRow(context.Background(), `
			SELECT
				(SELECT count(*) FROM turns
					WHERE conversation_id = $1
					AND (makez_task_id IS NOT NULL OR makez_run_id IS NOT NULL OR makez_workspace_id IS NOT NULL)),
				(SELECT count(*) FROM source_checkpoints),
				(SELECT count(*) FROM wake_intents WHERE conversation_id = $1),
				(SELECT count(*) FROM outbox WHERE conversation_id = $1),
				(SELECT count(*) FROM outbox WHERE conversation_id = $1 AND payload->>'execution' = 'unavailable')`,
			conversationID).Scan(&linked, &checkpoints, &wakes, &outbox, &unavailableOutbox); err != nil {
			t.Fatal(err)
		}
		if linked != 0 || checkpoints != 0 {
			t.Fatalf("fixture task linked a Makez run: linked=%d checkpoints=%d", linked, checkpoints)
		}
		if wakes != 2 || outbox != 2 || unavailableOutbox != 2 {
			t.Fatalf("submit rows wakes=%d outbox=%d unavailable=%d", wakes, outbox, unavailableOutbox)
		}
		assertUnavailableHealth(t, srv)
	})

	t.Run("unset fixture", func(t *testing.T) {
		srv, pool := startProduction(t, config.Config{})
		assertUnavailableHealth(t, srv)
		conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
		status, body := postFixture(t, srv, map[string]any{
			"source":         store.FixtureSource,
			"conversationId": conversationID,
			"eventKey":       "running-task-unset",
			"type":           "fixture.running-task",
		})
		if status != http.StatusNotFound {
			t.Fatalf("unset fixture route %d %s", status, body)
		}
		status, body = postSubmit(t, srv, testToken, conversationID, "unset-task", "/draft send the weekly note")
		if status != http.StatusOK {
			t.Fatalf("unset submit %d %s", status, body)
		}
		got := asMap(t, body)
		if got["execution"] != "unavailable" {
			t.Fatalf("execution = %#v", got["execution"])
		}
		if _, ok := got["assistantMessage"]; ok {
			t.Fatalf("unset fixture started a reply: %s", body)
		}
		user, _ := got["userMessage"].(map[string]any)
		if user["content"] != "/draft send the weekly note" || user["role"] != "user" || user["status"] != "complete" {
			t.Fatalf("unset user message = %#v", user)
		}
		if len(taskList(t, srv, conversationID)) != 0 {
			t.Fatal("unset fixture stored a task")
		}
		var approvals int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE session_id = $1`, conversationID).Scan(&approvals); err != nil {
			t.Fatal(err)
		}
		if approvals != 0 {
			t.Fatalf("unset fixture stored an approval: %d", approvals)
		}
		messages := messageList(t, srv, conversationID)
		assertNoModelReply(t, messages)
		if userCount(messages, "/draft send the weekly note") != 1 {
			t.Fatalf("unset message was not stored: %#v", messages)
		}
		assertUnavailableHealth(t, srv)
	})
}

func assertUnavailableHealth(t *testing.T, srv *httptest.Server) {
	t.Helper()
	health := asMap(t, mustGET(t, srv, "/v1/health"))
	if health["adapter"] != "unavailable" || health["makez"] != "disconnected" || health["ok"] != true {
		t.Fatalf("health = %#v", health)
	}
}

func taskList(t *testing.T, srv *httptest.Server, sessionID string) []map[string]any {
	t.Helper()
	status, body := do(t, srv, http.MethodGet, "/v1/tasks?sessionId="+sessionID, nil)
	if status != http.StatusOK {
		t.Fatalf("tasks %d %s", status, body)
	}
	raw := asMap(t, body)["tasks"].([]any)
	out := make([]map[string]any, len(raw))
	for i, item := range raw {
		out[i] = item.(map[string]any)
	}
	return out
}

func assertTaskStillRunning(t *testing.T, pool *pgxpool.Pool, sessionID, taskID string) {
	t.Helper()
	var status, source, result, approval string
	var tasks int
	err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT status FROM tasks WHERE id = $1),
			(SELECT source FROM tasks WHERE id = $1),
			(SELECT result FROM tasks WHERE id = $1),
			(SELECT approval_id FROM tasks WHERE id = $1),
			(SELECT count(*) FROM tasks WHERE session_id = $2)`,
		taskID, sessionID).Scan(&status, &source, &result, &approval, &tasks)
	if err != nil {
		t.Fatal(err)
	}
	if status != "running" || source != store.FixtureSource || result != "" || approval == "" || tasks != 1 {
		t.Fatalf("stored task status=%s source=%s result=%q approval=%s tasks=%d", status, source, result, approval, tasks)
	}
	var cardStatus, kind, requester string
	if err := pool.QueryRow(context.Background(), `
		SELECT status, kind, requester_actor_id
		FROM approvals WHERE id = $1`, approval).Scan(&cardStatus, &kind, &requester); err != nil {
		t.Fatal(err)
	}
	if cardStatus != "approved" || kind != "fixture-running-task" || requester != testActor {
		t.Fatalf("fixture card status=%s kind=%s requester=%s", cardStatus, kind, requester)
	}
}

func assertNoModelReply(t *testing.T, messages []map[string]any) {
	t.Helper()
	for _, msg := range messages {
		content, _ := msg["content"].(string)
		if msg["role"] == "assistant" && msg["status"] == "complete" {
			t.Fatalf("assistant success stored: %#v", msg)
		}
		if strings.HasPrefix(content, "[philo dev adapter]") {
			t.Fatalf("model reply stored: %#v", msg)
		}
	}
}

func streamingCount(messages []map[string]any) int {
	n := 0
	for _, msg := range messages {
		if msg["status"] == "streaming" {
			n++
		}
	}
	return n
}

func userCount(messages []map[string]any, content string) int {
	n := 0
	for _, msg := range messages {
		if msg["role"] == "user" && msg["content"] == content && msg["status"] == "complete" {
			n++
		}
	}
	return n
}
