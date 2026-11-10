package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
)

// TestUserMessageStoredWhileFixtureTaskRuns approves a draft through the
// decision route while PHILO_EXECUTION_FIXTURE=worker, then posts another
// message. The task stays running. The card keeps its snapshot and requester.
// The fixture is not Makez. An unset fixture stays unavailable.
func TestUserMessageStoredWhileFixtureTaskRuns(t *testing.T) {
	t.Run("worker fixture", func(t *testing.T) {
		srv, pool := startConfigured(t, config.Config{
			ExecutionFixture: config.ExecutionFixtureWorker,
			DevChunkDelay:    time.Millisecond,
		}, false)
		health := asMap(t, mustGET(t, srv, "/v1/health"))
		if health["adapter"] != "worker" || health["makez"] != "disconnected" || health["ok"] != true {
			t.Fatalf("health = %#v", health)
		}
		willa := openDefault(t, srv, createAgent(t, srv, "Willa"))
		hugo := openDefault(t, srv, createAgent(t, srv, "Hugo"))
		beforeHugo := cardEffects(t, pool, hugo)

		status, body := postSubmit(t, srv, testToken, willa, "draft-weekly", "/draft send the weekly note")
		if status != http.StatusOK {
			t.Fatalf("draft %d %s", status, body)
		}
		card := waitPendingCard(t, srv, willa)
		cardID := card["id"].(string)
		if card["requesterActorId"] != testActor {
			t.Fatalf("requester = %#v", card["requesterActorId"])
		}
		if kind, summary := snapSummary(t, card); kind != "draft_action" || summary != "send the weekly note" {
			t.Fatalf("snapshot = %s %s", kind, summary)
		}
		snapshotBefore := storedSnapshot(t, pool, cardID)

		status, body = do(t, srv, http.MethodPost, "/v1/approvals/"+cardID+"/decision", map[string]any{"decision": "approve"})
		if status != http.StatusOK {
			t.Fatalf("approve %d %s", status, body)
		}
		approved := asMap(t, body)
		if approved["id"] != cardID || approved["status"] != "approved" || approved["requesterActorId"] != testActor {
			t.Fatalf("approved = %#v", approved)
		}
		if kind, summary := snapSummary(t, approved); kind != "draft_action" || summary != "send the weekly note" {
			t.Fatalf("approve changed the snapshot: %s %s", kind, summary)
		}
		taskID := waitRunningTask(t, srv, willa, cardID)
		time.Sleep(200 * time.Millisecond)
		if got := taskList(t, srv, willa); len(got) != 1 || got[0]["id"] != taskID || got[0]["status"] != "running" || got[0]["source"] != "philo-server" || got[0]["result"] != "" {
			t.Fatalf("task finished or changed: %#v", got)
		}

		const follow = "Willa sends while the task runs"
		status, body = postSubmit(t, srv, testToken, willa, "while-running", follow)
		if status != http.StatusOK {
			t.Fatalf("send %d %s", status, body)
		}
		sent := asMap(t, body)
		if code, ok := sent["error"].(map[string]any); ok && code["code"] == "reply_in_progress" {
			t.Fatalf("rejected because the task is running: %s", body)
		}
		user, _ := sent["userMessage"].(map[string]any)
		if user["role"] != "user" || user["content"] != follow || user["status"] != "complete" {
			t.Fatalf("user message = %#v", user)
		}
		if !hasUser(messageList(t, srv, willa), follow) {
			t.Fatal("user message was not stored")
		}

		cards := approvalList(t, srv, willa)
		if len(cards) != 1 || cards[0]["id"] != cardID || cards[0]["status"] != "approved" || cards[0]["requesterActorId"] != testActor {
			t.Fatalf("card after send = %#v", cards)
		}
		if kind, summary := snapSummary(t, cards[0]); kind != "draft_action" || summary != "send the weekly note" {
			t.Fatalf("snapshot after send = %s %s", kind, summary)
		}
		if storedSnapshot(t, pool, cardID) != snapshotBefore {
			t.Fatalf("stored snapshot changed: %s", storedSnapshot(t, pool, cardID))
		}
		tasks := taskList(t, srv, willa)
		if len(tasks) != 1 || tasks[0]["id"] != taskID || tasks[0]["approvalId"] != cardID || tasks[0]["status"] != "running" || tasks[0]["source"] != "philo-server" || tasks[0]["result"] != "" {
			t.Fatalf("task after send = %#v", tasks)
		}
		if cardEffects(t, pool, hugo) != beforeHugo {
			t.Fatalf("other bot changed: before %+v after %+v", beforeHugo, cardEffects(t, pool, hugo))
		}
		var linked int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM turns
			WHERE conversation_id = $1
			AND (makez_task_id IS NOT NULL OR makez_run_id IS NOT NULL OR makez_workspace_id IS NOT NULL)`,
			willa).Scan(&linked); err != nil {
			t.Fatal(err)
		}
		if linked != 0 {
			t.Fatal("worker fixture linked a Makez run")
		}
	})

	t.Run("unset fixture", func(t *testing.T) {
		srv, pool := startProduction(t, config.Config{})
		health := asMap(t, mustGET(t, srv, "/v1/health"))
		if health["adapter"] != "unavailable" || health["makez"] != "disconnected" {
			t.Fatalf("health = %#v", health)
		}
		conversationID := openDefault(t, srv, createAgent(t, srv, "Una"))
		status, body := postSubmit(t, srv, testToken, conversationID, "unset-draft", "/draft send the weekly note")
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
		if user["content"] != "/draft send the weekly note" || user["status"] != "complete" {
			t.Fatalf("unset user message = %#v", user)
		}
		if len(taskList(t, srv, conversationID)) != 0 || len(approvalList(t, srv, conversationID)) != 0 {
			t.Fatal("unset fixture stored a card or a task")
		}
		var approvals int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE session_id = $1`, conversationID).Scan(&approvals); err != nil {
			t.Fatal(err)
		}
		if approvals != 0 {
			t.Fatalf("approvals = %d", approvals)
		}
	})
}

func waitPendingCard(t *testing.T, srv *httptest.Server, sessionID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cards := approvalList(t, srv, sessionID)
		if len(cards) == 1 && cards[0]["status"] == "pending" {
			return cards[0]
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("pending card was not stored")
	return nil
}

func waitRunningTask(t *testing.T, srv *httptest.Server, sessionID, approvalID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks := taskList(t, srv, sessionID)
		if len(tasks) == 1 && tasks[0]["approvalId"] == approvalID && tasks[0]["status"] == "running" && tasks[0]["source"] == "philo-server" {
			id, _ := tasks[0]["id"].(string)
			if id != "" {
				return id
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("running task was not stored")
	return ""
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

func storedSnapshot(t *testing.T, pool *pgxpool.Pool, approvalID string) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(context.Background(), `SELECT action_snapshot::text FROM approvals WHERE id = $1`, approvalID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func hasUser(messages []map[string]any, content string) bool {
	for _, msg := range messages {
		if msg["role"] == "user" && msg["content"] == content && msg["status"] == "complete" {
			return true
		}
	}
	return false
}
