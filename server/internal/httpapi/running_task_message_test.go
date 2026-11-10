package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/engine"
	"github.com/yosuang/philo/server/internal/store"
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
		workerPID := waitFixtureWorker(t)
		if sessions := fixtureWorkerSessions(t, pool, taskID); sessions != 1 {
			t.Fatalf("fixture worker sessions = %d", sessions)
		}
		startedAt := taskUpdatedAt(t, pool, taskID)
		if !startedAt.After(taskCreatedAt(t, pool, taskID)) {
			t.Fatal("fixture worker did not touch the running task")
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
		if !fixtureWorkerAlive(workerPID) {
			t.Fatalf("fixture worker %d exited before the task finished", workerPID)
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
		finished := waitTaskStatus(t, srv, willa, taskID, "completed")
		if finished["result"] != engine.FixtureWorkerResult {
			t.Fatalf("fixture end = %#v", finished["result"])
		}
		if finished["source"] != "philo-server" || finished["approvalId"] != cardID {
			t.Fatalf("finished task = %#v", finished)
		}
		if fixtureWorkerAlive(workerPID) {
			t.Fatalf("fixture worker %d still alive after the task finished", workerPID)
		}
		cards = approvalList(t, srv, willa)
		if len(cards) != 1 || cards[0]["id"] != cardID || cards[0]["status"] != "approved" || cards[0]["requesterActorId"] != testActor {
			t.Fatalf("card after fixture end = %#v", cards)
		}
		if kind, summary := snapSummary(t, cards[0]); kind != "draft_action" || summary != "send the weekly note" {
			t.Fatalf("snapshot after fixture end = %s %s", kind, summary)
		}
		if storedSnapshot(t, pool, cardID) != snapshotBefore {
			t.Fatal("fixture end changed the snapshot")
		}
		if sessions := fixtureWorkerSessions(t, pool, taskID); sessions != 1 {
			t.Fatalf("fixture worker session disappeared: %d", sessions)
		}
		if cardEffects(t, pool, hugo) != beforeHugo {
			t.Fatalf("other bot changed after fixture end: %+v", cardEffects(t, pool, hugo))
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

func waitTaskStatus(t *testing.T, srv *httptest.Server, sessionID, taskID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range taskList(t, srv, sessionID) {
			if task["id"] == taskID && task["status"] == want {
				return task
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s did not become %s", taskID, want)
	return nil
}

func fixtureWorkerSessions(t *testing.T, pool *pgxpool.Pool, taskID string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM logical_native_sessions
		WHERE scope = 'worker' AND worker_run_ref = $1 AND harness_pin = $2`,
		taskID, store.FixtureWorkerPin).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func taskCreatedAt(t *testing.T, pool *pgxpool.Pool, taskID string) time.Time {
	t.Helper()
	var created time.Time
	if err := pool.QueryRow(context.Background(), `SELECT created_at FROM tasks WHERE id = $1`, taskID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func taskUpdatedAt(t *testing.T, pool *pgxpool.Pool, taskID string) time.Time {
	t.Helper()
	var updated time.Time
	if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM tasks WHERE id = $1`, taskID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func waitFixtureWorker(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pid := fixtureWorkerPID(); pid != 0 && fixtureWorkerAlive(pid) {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fixture worker process did not start")
	return 0
}

func fixtureWorkerPID() int {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		status, err := os.ReadFile("/proc/" + entry.Name() + "/status")
		if err != nil || !strings.Contains(string(status), "PPid:\t"+strconv.Itoa(self)+"\n") {
			continue
		}
		cmd, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil || !strings.Contains(string(cmd), "philo-fixture-worker") {
			continue
		}
		return pid
	}
	return 0
}

func fixtureWorkerAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	return err == nil
}

func hasUser(messages []map[string]any, content string) bool {
	for _, msg := range messages {
		if msg["role"] == "user" && msg["content"] == content && msg["status"] == "complete" {
			return true
		}
	}
	return false
}
