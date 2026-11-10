//go:build linux

package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/makez"
	"github.com/yosuang/philo/server/internal/store"
)

// fixtureSourceLabel names the TCP fixture this file serves.
// It is not Makez, not a model, and not a launcher ack.
const (
	fixtureSourceLabel      = "test-fixture-source"
	fixtureSourceToken      = "test-fixture-token"
	fixtureProcessingCursor = "stdout:2,stderr:0"
	fixtureSourceLogOne     = "test-fixture-source:seq:1"
	fixtureSourceLogTwo     = "test-fixture-source:seq:2"
	osKillClientKey         = "os-kill-submit"
	osKillContent           = "Remember the cedar"
)

// TestOSKillResumesFixtureSource kills one Philo OS process and reads the
// same database from a second process. Resume is the new process starting.
// A later POST /v1/sessions/.../messages is not that path. SIGKILL after
// commit, SIGKILL during an open transaction, SIGTERM, and an injected
// fault are separate cases.
func TestOSKillResumesFixtureSource(t *testing.T) {
	bin := buildPhiloServer(t)
	t.Run("sigkill-after-commit", func(t *testing.T) {
		resumeFixtureSource(t, bin, syscall.SIGKILL)
	})
	t.Run("sigkill-during-transaction", func(t *testing.T) {
		killDuringTransaction(t, bin)
	})
	t.Run("graceful-sigterm", func(t *testing.T) {
		resumeFixtureSource(t, bin, syscall.SIGTERM)
	})
	t.Run("fixture-unset", func(t *testing.T) {
		killWithExecutionUnavailable(t, bin)
	})
	t.Run("injected-fault", func(t *testing.T) {
		injectedFaultIsNotAKill(t)
	})
}

func resumeFixtureSource(t *testing.T, bin string, sig syscall.Signal) {
	t.Helper()
	dsn := newProcessDSN(t)
	src := startFixtureSource(t)
	extra := map[string]string{
		"PHILO_MAKEZ_API_URL":      src.URL,
		"PHILO_MAKEZ_API_TOKEN":    src.Token,
		"PHILO_MAKEZ_WORKSPACE_ID": src.Workspace,
		"PHILO_MAKEZ_BINDING":      store.TestBindingLabel,
	}
	first := startPhiloOS(t, bin, dsn, extra, "test-binding")
	agent := apiCreateAgent(t, first.url, "Cedar")
	conv := apiOpenConversation(t, first.url, agent)
	ws := dialLive(t, first.url, conv)
	posted := apiSubmit(t, first.url, conv, osKillClientKey, osKillContent)
	if posted["execution"] != "test-binding" || posted["replayed"] != false {
		t.Fatalf("submit = %#v", posted)
	}
	if _, ok := posted["assistantMessage"]; ok {
		t.Fatalf("submit stored an assistant: %#v", posted)
	}
	turn := posted["turnId"].(string)
	user := posted["userMessage"].(map[string]any)
	if user["content"] != osKillContent || user["role"] != "user" || user["status"] != "complete" {
		t.Fatalf("user = %#v", user)
	}
	assertNoEngineCursor(t, ws.drain(t, 750*time.Millisecond), conv, true)
	assertFixtureUnsetSurface(t, first.url)
	assertSourceLinked(t, src, agent, conv, osKillContent)

	pool := connectDSN(t, dsn)
	before := durableView(t, pool, conv, src)
	if before.applied != 2 || before.processing != fixtureProcessingCursor || before.logs != 2 || before.high != 1 {
		t.Fatalf("committed view = %+v", before)
	}
	if before.turnID != turn || before.userID != user["id"] || before.taskID != src.TaskID || !before.outboxUnsent {
		t.Fatalf("committed identity = %+v", before)
	}
	if before.high == before.applied || strconv.FormatInt(before.high, 10) == before.processing {
		t.Fatalf("business high-water collapsed into the source checkpoint: %+v", before)
	}
	waitResult := stopPhilo(t, first, sig)

	second := startPhiloOS(t, bin, dsn, extra, "test-binding")
	assertStartupResume(t, second, src)
	if n := messagePostsTo(second.url); n != 0 {
		t.Fatalf("replay ran because the harness posted %d messages to the new process", n)
	}
	ws2 := dialLive(t, second.url, conv)
	assertNoEngineCursor(t, ws2.drain(t, 300*time.Millisecond), conv, false)

	after := durableView(t, pool, conv, src)
	if after != before {
		t.Fatalf("replay changed durable state\nbefore %+v\nafter  %+v", before, after)
	}
	cursors := src.eventCursors()
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "2" {
		t.Fatalf("startup resume cursors = %#v", cursors)
	}
	if src.posts() != 1 {
		t.Fatalf("startup submitted another task, posts=%d", src.posts())
	}
	for _, path := range src.paths() {
		if strings.Contains(path, "/ack") || strings.Contains(path, "launcher") {
			t.Fatalf("fixture saw %s", path)
		}
	}
	assertNoAssistantSuccess(t, second.url, pool, conv)
	snap := apiSnapshot(t, second.url, conv)
	if snap["sessionId"] != conv || number(t, snap["highWater"]) != before.high {
		t.Fatalf("snapshot = %#v", snap)
	}
	messages := snap["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["id"] != user["id"] || messages[0].(map[string]any)["content"] != osKillContent {
		t.Fatalf("messages = %#v", messages)
	}
	if n := messagePostsTo(second.url); n != 0 {
		t.Fatalf("replay ran because the harness posted %d messages to the new process", n)
	}
	writeCaseSummary(t, map[string]string{
		"case":                  t.Name(),
		"phase":                 "after-commit",
		"signal":                signalName(sig),
		"wait":                  waitResult,
		"pid1":                  strconv.Itoa(first.pid),
		"pid2":                  strconv.Itoa(second.pid),
		"source":                fixtureSourceLabel,
		"checkpoint_applied":    strconv.FormatInt(after.applied, 10),
		"checkpoint_processing": after.processing,
		"event_cursors":         strings.Join(cursors, ","),
		"source_rows":           strconv.Itoa(after.logs),
		"high_water":            strconv.FormatInt(after.high, 10),
		"assistant_rows":        "0",
	})
	t.Logf("os-kill case=%s phase=after-commit signal=%s wait=%s pid1=%d pid2=%d source=%s checkpoint=%d processing=%s cursors=%s high=%d turn=%s user=%s outbox=%s",
		t.Name(), signalName(sig), waitResult, first.pid, second.pid, fixtureSourceLabel, after.applied, after.processing, strings.Join(cursors, ","), after.high, after.turnID, after.userID, after.outboxID)
}

func killWithExecutionUnavailable(t *testing.T, bin string) {
	t.Helper()
	dsn := newProcessDSN(t)
	decoy := startFixtureSource(t)
	first := startPhiloOS(t, bin, dsn, nil, "unavailable")
	agent := apiCreateAgent(t, first.url, "Cedar")
	conv := apiOpenConversation(t, first.url, agent)
	ws := dialLive(t, first.url, conv)
	posted := apiSubmit(t, first.url, conv, osKillClientKey, osKillContent)
	if posted["execution"] != "unavailable" || posted["replayed"] != false {
		t.Fatalf("submit = %#v", posted)
	}
	if _, ok := posted["assistantMessage"]; ok {
		t.Fatalf("unavailable submit stored an assistant: %#v", posted)
	}
	user := posted["userMessage"].(map[string]any)
	assertNoEngineCursor(t, ws.drain(t, 750*time.Millisecond), conv, true)
	assertFixtureUnsetSurface(t, first.url)
	pool := connectDSN(t, dsn)
	before := businessView(t, pool, conv)
	if before.high < 1 || before.userContent != osKillContent || before.assistants != 0 || before.completedTasks != 0 {
		t.Fatalf("before kill = %+v", before)
	}
	waitResult := stopPhilo(t, first, syscall.SIGKILL)

	second := startPhiloOS(t, bin, dsn, nil, "unavailable")
	assertFixtureUnsetSurface(t, second.url)
	secondLog, err := os.ReadFile(second.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secondLog), "startup resume") {
		t.Fatalf("unset binding resumed a source\n%s", secondLog)
	}
	if decoy.posts() != 0 || len(decoy.paths()) != 0 || len(decoy.eventCursors()) != 0 {
		t.Fatalf("unset process used the network posts=%d paths=%v cursors=%v", decoy.posts(), decoy.paths(), decoy.eventCursors())
	}
	if n := messagePostsTo(second.url); n != 0 {
		t.Fatalf("harness posted %d messages to the new process", n)
	}
	after := businessView(t, pool, conv)
	if after != before {
		t.Fatalf("kill changed business state\nbefore %+v\nafter  %+v", before, after)
	}
	snap := apiSnapshot(t, second.url, conv)
	if snap["sessionId"] != conv || number(t, snap["highWater"]) != before.high {
		t.Fatalf("snapshot = %#v", snap)
	}
	messages := snap["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["id"] != user["id"] {
		t.Fatalf("messages = %#v", messages)
	}
	writeCaseSummary(t, map[string]string{
		"case":           t.Name(),
		"signal":         signalName(syscall.SIGKILL),
		"wait":           waitResult,
		"pid1":           strconv.Itoa(first.pid),
		"pid2":           strconv.Itoa(second.pid),
		"source":         "unset",
		"execution":      "unavailable",
		"high_water":     strconv.FormatInt(after.high, 10),
		"assistant_rows": "0",
	})
	t.Logf("os-kill case=%s signal=%s wait=%s pid1=%d pid2=%d source=unset execution=unavailable high=%d network=0",
		t.Name(), signalName(syscall.SIGKILL), waitResult, first.pid, second.pid, after.high)
}

// killDuringTransaction sends SIGKILL while the submit transaction is waiting
// on a row lock. That transaction must not commit. Startup must not invent a
// source for it. This is not the after-commit resume.
func killDuringTransaction(t *testing.T, bin string) {
	t.Helper()
	dsn := newProcessDSN(t)
	src := startFixtureSource(t)
	extra := map[string]string{
		"PHILO_MAKEZ_API_URL":      src.URL,
		"PHILO_MAKEZ_API_TOKEN":    src.Token,
		"PHILO_MAKEZ_WORKSPACE_ID": src.Workspace,
		"PHILO_MAKEZ_BINDING":      store.TestBindingLabel,
	}
	first := startPhiloOS(t, bin, dsn, extra, "test-binding")
	agent := apiCreateAgent(t, first.url, "Cedar")
	conv := apiOpenConversation(t, first.url, agent)
	pool := connectDSN(t, dsn)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rolled := false
	t.Cleanup(func() {
		if !rolled {
			_ = tx.Rollback(ctx)
		}
	})
	var sessionID string
	if err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id = $1 FOR UPDATE`, conv).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	var holder int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		body := strings.NewReader(`{"content":"Remember the cedar","options":{}}`)
		req, err := http.NewRequest(http.MethodPost, first.url+"/v1/sessions/"+conv+"/messages", body)
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Idempotency-Key", "os-kill-open-tx")
		noteMessagePost(req.URL.String())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		done <- fmt.Errorf("status %d %s", resp.StatusCode, raw)
	}()
	waitUntil(t, func() bool {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> $1
			  AND wait_event_type = 'Lock'`, holder).Scan(&n)
		return err == nil && n >= 1
	})
	select {
	case err := <-done:
		t.Fatalf("submit finished before SIGKILL: %v", err)
	default:
	}
	if src.posts() != 0 || len(src.eventCursors()) != 0 {
		t.Fatalf("open transaction reached the fixture posts=%d cursors=%v", src.posts(), src.eventCursors())
	}
	var messages, checkpoints int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatalf("uncommitted submit is visible, messages=%d", messages)
	}
	waitResult := stopPhilo(t, first, syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not return after SIGKILL")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rolled = true
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || checkpoints != 0 {
		t.Fatalf("killed transaction committed messages=%d checkpoints=%d", messages, checkpoints)
	}

	second := startPhiloOS(t, bin, dsn, extra, "test-binding")
	secondLog, err := os.ReadFile(second.logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(secondLog)
	resumeAt := strings.Index(text, "startup resume linked=0")
	listenAt := strings.Index(text, "listening on")
	if resumeAt < 0 || listenAt < 0 || resumeAt > listenAt {
		t.Fatalf("startup scan was not before listen\n%s", text)
	}
	if strings.Contains(text, "after=") {
		t.Fatalf("startup resumed a source that was not committed\n%s", text)
	}
	if src.posts() != 0 || len(src.paths()) != 0 {
		t.Fatalf("startup called the fixture posts=%d paths=%v", src.posts(), src.paths())
	}
	if n := messagePostsTo(second.url); n != 0 {
		t.Fatalf("harness posted %d messages to the new process", n)
	}
	snap := apiSnapshot(t, second.url, conv)
	if snap["sessionId"] != conv || number(t, snap["highWater"]) != 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
	if msgs, ok := snap["messages"].([]any); ok && len(msgs) != 0 {
		t.Fatalf("messages = %#v", msgs)
	}
	writeCaseSummary(t, map[string]string{
		"case":           t.Name(),
		"phase":          "during-transaction",
		"signal":         signalName(syscall.SIGKILL),
		"wait":           waitResult,
		"pid1":           strconv.Itoa(first.pid),
		"pid2":           strconv.Itoa(second.pid),
		"source":         fixtureSourceLabel,
		"checkpoint":     "0",
		"assistant_rows": "0",
		"fixture_posts":  "0",
	})
	t.Logf("os-kill case=%s phase=during-transaction signal=%s wait=%s pid1=%d pid2=%d source=%s checkpoint=0 fixture_posts=0",
		t.Name(), signalName(syscall.SIGKILL), waitResult, first.pid, second.pid, fixtureSourceLabel)
}

func assertStartupResume(t *testing.T, p *philoProc, src *fixtureSource) {
	t.Helper()
	raw, err := os.ReadFile(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	mark := fmt.Sprintf("startup resume workspace=%s task=%s after=2", fixtureSourceLabel, src.TaskID)
	resumeAt := strings.Index(text, mark)
	listenAt := strings.Index(text, "listening on")
	if resumeAt < 0 || listenAt < 0 || resumeAt > listenAt {
		t.Fatalf("startup resume was not before listen\n%s", text)
	}
	if !strings.Contains(text, "startup resume linked=1") {
		t.Fatalf("startup did not find the linked turn\n%s", text)
	}
	cursors := src.eventCursors()
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "2" {
		t.Fatalf("startup resume cursors = %#v", cursors)
	}
	if src.posts() != 1 {
		t.Fatalf("startup submitted another task, posts=%d", src.posts())
	}
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the open transaction")
}

// injectedFaultIsNotAKill rolls a projection back inside this process.
// It does not start or signal a Philo process. The captured bytes are the
// existing no-key fixture, not a live Makez call.
func injectedFaultIsNotAKill(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conv, turn := seedUnavailableTurn(t, srv)
	_, err := store.New(pool).ProjectCapturedFailure(context.Background(), fullCapture(conv, turn), store.FaultCommit)
	var injected store.InjectedFault
	if !errors.As(err, &injected) || injected.Point != store.FaultCommit {
		t.Fatalf("err = %v", err)
	}
	if scalar(t, pool, `SELECT count(*) FROM source_checkpoints`) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM source_log_dedup`) != 0 ||
		scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 ||
		assistants(t, srv, conv) != 0 {
		t.Fatal("injected fault left a committed projection or an assistant")
	}
	writeCaseSummary(t, map[string]string{
		"case":       t.Name(),
		"signal":     "none",
		"pid1":       "none",
		"pid2":       "none",
		"fault":      string(store.FaultCommit),
		"source":     "captured-no-key-failure",
		"checkpoint": "0",
	})
	t.Logf("os-kill case=%s signal=none fault=%s processes=0 checkpoint=0", t.Name(), store.FaultCommit)
}

type durableState struct {
	applied        int64
	processing     string
	logs           int
	high           int64
	events         int
	assistants     int
	completed      int
	terminals      int
	userID         string
	userContent    string
	hashOne        string
	hashTwo        string
	workspace      string
	runID          string
	turnID         string
	taskID         string
	eventID        string
	outboxID       string
	checkpointXmin string
	outboxXmin     string
	outboxUnsent   bool
}

func durableView(t *testing.T, pool *pgxpool.Pool, conv string, src *fixtureSource) durableState {
	t.Helper()
	ctx := context.Background()
	var got durableState
	err := pool.QueryRow(ctx, `
		SELECT c.applied_public_seq, c.processing_cursor, c.task_id, c.xmin::text,
			(SELECT count(*) FROM source_log_dedup WHERE run_id = c.run_id),
			s.business_high_water,
			(SELECT count(*) FROM business_events WHERE conversation_id = s.id),
			(SELECT count(*) FROM messages WHERE session_id = s.id AND role = 'assistant'),
			(SELECT count(*) FROM tasks WHERE session_id = s.id AND status = 'completed'),
			(SELECT count(*) FROM terminal_observations),
			u.id, u.content, t.id, t.makez_workspace_id, t.makez_run_id,
			(SELECT id FROM business_events WHERE conversation_id = s.id AND event_type = 'message.created'),
			o.id, o.xmin::text, (o.published_at IS NULL)
		FROM sessions s
		JOIN turns t ON t.conversation_id = s.id
		JOIN messages u ON u.turn_id = t.id AND u.role = 'user'
		JOIN source_checkpoints c
		  ON c.workspace_id = t.makez_workspace_id
		 AND c.task_id = t.makez_task_id
		 AND c.run_id = t.makez_run_id
		JOIN outbox o ON o.turn_id = t.id AND o.kind = 'submit'
		WHERE s.id = $1`, conv).Scan(
		&got.applied, &got.processing, &got.taskID, &got.checkpointXmin, &got.logs, &got.high, &got.events,
		&got.assistants, &got.completed, &got.terminals,
		&got.userID, &got.userContent, &got.turnID, &got.workspace, &got.runID,
		&got.eventID, &got.outboxID, &got.outboxXmin, &got.outboxUnsent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.workspace != fixtureSourceLabel || got.runID != src.RunID || got.taskID != src.TaskID {
		t.Fatalf("link workspace=%s task=%s run=%s", got.workspace, got.taskID, got.runID)
	}
	if !got.outboxUnsent || got.outboxID == "" || got.eventID == "" || got.checkpointXmin == "" {
		t.Fatalf("identity = %+v", got)
	}
	rows, err := pool.Query(ctx, `
		SELECT public_seq, payload_sha256, failure_text
		FROM source_log_dedup WHERE run_id = $1 ORDER BY public_seq`, src.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var seq int64
		var hash, failure string
		if err := rows.Scan(&seq, &hash, &failure); err != nil {
			t.Fatal(err)
		}
		if failure != "" {
			t.Fatalf("plain fixture log stored a failure: %q", failure)
		}
		seqs = append(seqs, seq)
		switch seq {
		case 1:
			got.hashOne = hash
		case 2:
			got.hashTwo = hash
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 ||
		got.hashOne != fixtureLogHash(fixtureSourceLogOne) ||
		got.hashTwo != fixtureLogHash(fixtureSourceLogTwo) {
		t.Fatalf("logs seqs=%v hashes=%s %s", seqs, got.hashOne, got.hashTwo)
	}
	if got.userContent != osKillContent || got.assistants != 0 || got.completed != 0 || got.terminals != 0 {
		t.Fatalf("view = %+v", got)
	}
	var kinds []string
	kindRows, err := pool.Query(ctx, `
		SELECT event_type FROM business_events WHERE conversation_id = $1 ORDER BY cursor`, conv)
	if err != nil {
		t.Fatal(err)
	}
	defer kindRows.Close()
	for kindRows.Next() {
		var kind string
		if err := kindRows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	if err := kindRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 || kinds[0] != "message.created" {
		t.Fatalf("business events = %#v", kinds)
	}
	return got
}

type businessState struct {
	high           int64
	events         int
	assistants     int
	completedTasks int
	userID         string
	userContent    string
	conversationID string
}

func businessView(t *testing.T, pool *pgxpool.Pool, conv string) businessState {
	t.Helper()
	var got businessState
	err := pool.QueryRow(context.Background(), `
		SELECT s.id, s.business_high_water,
			(SELECT count(*) FROM business_events WHERE conversation_id = s.id),
			(SELECT count(*) FROM messages WHERE session_id = s.id AND role = 'assistant'),
			(SELECT count(*) FROM tasks WHERE session_id = s.id AND status = 'completed'),
			u.id, u.content
		FROM sessions s
		JOIN messages u ON u.session_id = s.id AND u.role = 'user'
		WHERE s.id = $1`, conv).Scan(
		&got.conversationID, &got.high, &got.events, &got.assistants, &got.completedTasks,
		&got.userID, &got.userContent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertNoAssistantSuccess(t *testing.T, base string, pool *pgxpool.Pool, conv string) {
	t.Helper()
	for _, item := range apiMessages(t, base, conv) {
		if item["role"] == "assistant" {
			t.Fatalf("assistant = %#v", item)
		}
		if item["role"] == "user" && (item["content"] != osKillContent || item["status"] != "complete") {
			t.Fatalf("user = %#v", item)
		}
	}
	if scalar(t, pool, `SELECT count(*) FROM messages WHERE session_id = $1 AND role = 'assistant'`, conv) != 0 {
		t.Fatal("assistant row")
	}
	if scalar(t, pool, `SELECT count(*) FROM tasks WHERE status = 'completed'`) != 0 {
		t.Fatal("a task was marked completed")
	}
	if scalar(t, pool, `SELECT count(*) FROM terminal_observations`) != 0 {
		t.Fatal("a run was marked terminal")
	}
	for _, ev := range apiEvents(t, base, conv) {
		if ev["type"] == "message.completed" || ev["type"] == "message.error" {
			t.Fatalf("business event = %#v", ev)
		}
	}
}

func assertFixtureUnsetSurface(t *testing.T, base string) {
	t.Helper()
	health := apiGet(t, base, "/v1/health")
	if health["ok"] != true || health["adapter"] != "unavailable" || health["makez"] != "disconnected" {
		t.Fatalf("health = %#v", health)
	}
	status, body := apiCall(t, base, http.MethodPost, "/v1/fixture-business-source", "", map[string]any{
		"source": fixtureSourceLabel,
	})
	if status != http.StatusNotFound {
		t.Fatalf("business fixture route %d %s", status, body)
	}
}

func assertSourceLinked(t *testing.T, src *fixtureSource, agent, conv, content string) {
	t.Helper()
	if src.posts() != 1 || len(src.eventCursors()) != 1 || src.eventCursors()[0] != "" {
		t.Fatalf("first read posts=%d cursors=%v", src.posts(), src.eventCursors())
	}
	var sent struct {
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
	}
	raw := src.postBody()
	if strings.Contains(string(raw), `"owner_id"`) {
		t.Fatalf("submit body carried owner_id: %s", raw)
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Prompt != content || sent.Config.Execution.BotID != agent || sent.Config.Execution.ConversationID != conv ||
		sent.Config.Execution.Contract != makez.ExecutionContract || sent.AgentID != makez.AgentID {
		t.Fatalf("fixture submit = %#v", sent)
	}
	key := src.idempotencyKey()
	if key == osKillClientKey || !strings.HasPrefix(key, "turn/") || !strings.HasSuffix(key, "/submit") {
		t.Fatalf("fixture idempotency key = %q", key)
	}
	if !src.labeled() {
		t.Fatal("fixture response was not labeled")
	}
}

func assertNoEngineCursor(t *testing.T, frames []map[string]any, conv string, wantSessionUpdate bool) {
	t.Helper()
	sawUpdate := false
	for _, ev := range frames {
		if _, ok := ev["cursor"]; ok {
			t.Fatalf("engine frame has a cursor: %#v", ev)
		}
		if _, ok := ev["highWater"]; ok {
			t.Fatalf("engine frame has a business high-water: %#v", ev)
		}
		switch ev["type"] {
		case "message.delta", "message.completed", "message.error":
			t.Fatalf("engine streamed an assistant frame: %#v", ev)
		case "session.updated":
			sawUpdate = true
			if ev["id"] != conv {
				t.Fatalf("session update = %#v", ev)
			}
		}
	}
	if wantSessionUpdate && !sawUpdate {
		t.Fatalf("frames = %#v", frames)
	}
}

type fixtureSource struct {
	URL       string
	Workspace string
	Token     string
	TaskID    string
	RunID     string

	server *http.Server

	mu         sync.Mutex
	postCount  int
	cursors    []string
	seen       []string
	body       []byte
	key        string
	states     []string
	labeledAll bool
}

func startFixtureSource(t *testing.T) *fixtureSource {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fixtureSource{
		URL:        "http://" + ln.Addr().String(),
		Workspace:  fixtureSourceLabel,
		Token:      fixtureSourceToken,
		TaskID:     strings.ReplaceAll(uuid.NewString(), "-", ""),
		RunID:      strings.ReplaceAll(uuid.NewString(), "-", ""),
		labeledAll: true,
	}
	if s.TaskID == "ddeda2f7e3423d3e40d43f69a126afe7" {
		t.Fatal("fixture reused the captured task id")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tasks", s.submit)
	mux.HandleFunc("GET /v1/tasks/{id}", s.get)
	mux.HandleFunc("GET /v1/tasks/{id}/events", s.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.note(r.Method + " " + r.URL.RequestURI())
		http.NotFound(w, r)
	})
	s.server = &http.Server{Handler: mux}
	go func() { _ = s.server.Serve(ln) }()
	t.Cleanup(func() { _ = s.server.Close() })
	return s
}

func (s *fixtureSource) submit(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	if !s.authorized(w, r) {
		return
	}
	if strings.Contains(string(raw), `"owner_id"`) {
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
	if body.AgentID != makez.AgentID || body.AgentVersion != makez.AgentVersion ||
		body.Config.Execution.Contract != makez.ExecutionContract ||
		body.Config.Execution.HarnessPin != makez.HarnessPin ||
		body.Config.Execution.Scope != "personal" || body.Timeout != makez.TaskTimeoutSeconds ||
		body.Config.Execution.ConversationID == "" || body.Config.Execution.BotID == "" || body.Prompt == "" {
		http.Error(w, "contract", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || !strings.HasPrefix(key, "turn/") || !strings.HasSuffix(key, "/submit") {
		http.Error(w, "idempotency", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.postCount++
	s.key = key
	s.body = append([]byte(nil), raw...)
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	s.label(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(s.taskBody("RUNNING"))
}

func (s *fixtureSource) get(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	if r.PathValue("id") != s.TaskID {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.states = append(s.states, "RUNNING")
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	s.label(w)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.taskBody("RUNNING"))
}

func (s *fixtureSource) events(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	if r.PathValue("id") != s.TaskID {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("after") != "" {
		http.Error(w, "after query is not the resume cursor", http.StatusBadRequest)
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	s.mu.Lock()
	s.cursors = append(s.cursors, cursor)
	s.seen = append(s.seen, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()
	after := int64(0)
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "cursor", http.StatusBadRequest)
			return
		}
		after = n
	}
	s.label(w)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, row := range []struct {
		seq  int64
		text string
	}{
		{1, fixtureSourceLogOne},
		{2, fixtureSourceLogTwo},
	} {
		if row.seq <= after {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"seq":    row.seq,
			"run_id": s.RunID,
			"type":   "log",
			"payload": map[string]any{
				"stream": "stdout",
				"text":   row.text,
			},
			"created_at": "2026-11-10T12:00:00Z",
		})
		if err != nil {
			http.Error(w, "log", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", row.seq, payload)
	}
}

func (s *fixtureSource) taskBody(state string) []byte {
	raw, err := json.Marshal(map[string]any{
		"id":            s.TaskID,
		"workspace_id":  s.Workspace,
		"agent_id":      makez.AgentID,
		"agent_version": makez.AgentVersion,
		"state":         state,
		"config": map[string]any{
			"execution": map[string]any{
				"conversation_id": s.conversationID(),
			},
		},
		"run": map[string]any{
			"id":     s.RunID,
			"state":  "RUNNING",
			"cursor": fixtureProcessingCursor,
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func (s *fixtureSource) conversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body struct {
		Config struct {
			Execution struct {
				ConversationID string `json:"conversation_id"`
			} `json:"execution"`
		} `json:"config"`
	}
	_ = json.Unmarshal(s.body, &body)
	return body.Config.Execution.ConversationID
}

func (s *fixtureSource) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+s.Token || r.Header.Get("X-Makez-Workspace-ID") != s.Workspace {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *fixtureSource) label(w http.ResponseWriter) {
	w.Header().Set("X-Test-Source", fixtureSourceLabel)
	s.mu.Lock()
	if w.Header().Get("X-Test-Source") != fixtureSourceLabel {
		s.labeledAll = false
	}
	s.mu.Unlock()
}

func (s *fixtureSource) note(path string) {
	s.mu.Lock()
	s.seen = append(s.seen, path)
	s.mu.Unlock()
}

func (s *fixtureSource) posts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.postCount
}

func (s *fixtureSource) eventCursors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cursors...)
}

func (s *fixtureSource) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *fixtureSource) postBody() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.body...)
}

func (s *fixtureSource) idempotencyKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

func (s *fixtureSource) labeled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.labeledAll && s.postCount > 0
}

func fixtureLogHash(text string) string {
	sum := sha256.Sum256([]byte("stdout\n" + text))
	return hex.EncodeToString(sum[:])
}

type philoProc struct {
	cmd      *exec.Cmd
	url      string
	pid      int
	logPath  string
	log      *os.File
	waitOnce sync.Once
	waitErr  error
}

func startPhiloOS(t *testing.T, bin, dsn string, extra map[string]string, execution string) *philoProc {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	dir := caseLogDir(t)
	logPath := filepath.Join(dir, fmt.Sprintf("philo-%d.log", time.Now().UnixNano()))
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = philoEnv(dsn, addr, extra)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, fmt.Sprintf("pid-%d.log", cmd.Process.Pid))
	if err := os.Rename(logPath, pidPath); err != nil {
		t.Fatal(err)
	}
	p := &philoProc{cmd: cmd, url: "http://" + addr, pid: cmd.Process.Pid, logPath: pidPath, log: f}
	t.Cleanup(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.wait()
		_ = p.log.Close()
	})
	waitHealthy(t, p, execution)
	if p.pid == os.Getpid() {
		t.Fatal("server pid is the test process")
	}
	return p
}

func (p *philoProc) wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
	})
	return p.waitErr
}

func stopPhilo(t *testing.T, p *philoProc, sig syscall.Signal) string {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %s pid %d: %v", signalName(sig), p.pid, err)
	}
	err := p.wait()
	var result string
	switch sig {
	case syscall.SIGKILL:
		status, werr := unixStatus(err)
		if werr != nil || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("SIGKILL pid %d wait=%v status=%v", p.pid, err, status)
		}
		result = "signaled:SIGKILL"
	case syscall.SIGTERM:
		if err != nil {
			raw, _ := os.ReadFile(p.logPath)
			t.Fatalf("SIGTERM pid %d exited %v\n%s", p.pid, err, raw)
		}
		result = "exited:0"
	default:
		t.Fatalf("signal %s", sig)
	}
	if alive := syscall.Kill(p.pid, 0); alive == nil {
		t.Fatalf("pid %d still running after %s", p.pid, signalName(sig))
	}
	return result
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	default:
		return sig.String()
	}
}

func unixStatus(err error) (syscall.WaitStatus, error) {
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, err
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, fmt.Errorf("status type %T", exit.Sys())
	}
	return status, nil
}

func waitHealthy(t *testing.T, p *philoProc, execution string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+"/v1/health", nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"ok":true`) {
				raw, readErr := os.ReadFile(p.logPath)
				if readErr == nil && strings.Contains(string(raw), "execution="+execution) {
					return
				}
				last = fmt.Errorf("log missing execution=%s", execution)
			} else {
				last = fmt.Errorf("health %d %s", resp.StatusCode, body)
			}
		} else {
			last = err
		}
		time.Sleep(30 * time.Millisecond)
	}
	raw, _ := os.ReadFile(p.logPath)
	t.Fatalf("pid %d did not serve: %v\n%s", p.pid, last, raw)
}

func buildPhiloServer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "philo-server")
	cmd := exec.Command("go", "build", "-o", out, "./server")
	cmd.Dir = moduleRoot(t)
	cmd.Env = os.Environ()
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build server: %v\n%s", err, raw)
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func newProcessDSN(t *testing.T) string {
	t.Helper()
	name := "t" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := adminPool.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	return databaseDSN(name)
}

func connectDSN(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := store.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func philoEnv(dsn, addr string, extra map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "PHILO_") {
			continue
		}
		env = append(env, item)
	}
	env = append(env,
		"PHILO_DATABASE_URL="+dsn,
		"PHILO_HTTP_ADDR="+addr,
		"PHILO_AUTH_CREDENTIALS="+testToken+"|"+testActor+"|"+testOwner,
		"NO_PROXY=127.0.0.1,localhost",
	)
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

var osKillLogDirs sync.Map

func caseLogDir(t *testing.T) string {
	t.Helper()
	if v, ok := osKillLogDirs.Load(t.Name()); ok {
		return v.(string)
	}
	dir := os.Getenv("PHILO_OS_KILL_LOG_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		dir = filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "-"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	osKillLogDirs.Store(t.Name(), dir)
	return dir
}

func writeCaseSummary(t *testing.T, fields map[string]string) {
	t.Helper()
	dir := caseLogDir(t)
	var b strings.Builder
	for _, key := range []string{
		"case", "phase", "signal", "wait", "pid1", "pid2", "source", "execution", "fault",
		"checkpoint_applied", "checkpoint_processing", "checkpoint",
		"event_cursors", "source_rows", "high_water", "assistant_rows", "fixture_posts",
	} {
		if value, ok := fields[key]; ok {
			fmt.Fprintf(&b, "%s=%s\n", key, value)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

type liveSocket struct {
	conn *websocket.Conn
}

func dialLive(t *testing.T, base, sessionID string) *liveSocket {
	t.Helper()
	rawURL := "ws" + strings.TrimPrefix(base, "http") + "/v1/events"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(rawURL, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(map[string]string{"type": "subscribe", "sessionId": sessionID}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack map[string]any
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if ack["type"] != "subscribed" || ack["sessionId"] != sessionID {
		t.Fatalf("ack = %#v", ack)
	}
	if _, ok := ack["cursor"]; ok {
		t.Fatalf("subscribe ack invented a cursor: %#v", ack)
	}
	return &liveSocket{conn: conn}
}

func (s *liveSocket) drain(t *testing.T, d time.Duration) []map[string]any {
	t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(d))
	var out []map[string]any
	for {
		var msg map[string]any
		if err := s.conn.ReadJSON(&msg); err != nil {
			return out
		}
		out = append(out, msg)
	}
}

func apiCreateAgent(t *testing.T, base, name string) string {
	t.Helper()
	body := apiCallOK(t, base, http.MethodPost, "/v1/agents", map[string]any{
		"name":         name,
		"instructions": "Be brief.",
		"modelId":      "dev-local",
	})
	return body["id"].(string)
}

func apiOpenConversation(t *testing.T, base, agentID string) string {
	t.Helper()
	body := apiCallOK(t, base, http.MethodPost, "/v1/agents/"+agentID+"/conversation", nil)
	return body["id"].(string)
}

func apiSubmit(t *testing.T, base, conv, key, content string) map[string]any {
	t.Helper()
	status, raw := apiCall(t, base, http.MethodPost, "/v1/sessions/"+conv+"/messages", key, map[string]any{
		"content": content,
		"options": map[string]any{},
	})
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, raw)
	}
	return asMap(t, raw)
}

func apiSnapshot(t *testing.T, base, conv string) map[string]any {
	t.Helper()
	return apiGet(t, base, "/v1/sessions/"+conv+"/snapshot")
}

func apiMessages(t *testing.T, base, conv string) []map[string]any {
	t.Helper()
	body := apiGet(t, base, "/v1/sessions/"+conv+"/messages")
	return mapsOf(t, body["messages"])
}

func apiEvents(t *testing.T, base, conv string) []map[string]any {
	t.Helper()
	status, raw := apiCall(t, base, http.MethodGet, "/v1/sessions/"+conv+"/business-events?after=0", "", nil)
	if status != http.StatusOK {
		t.Fatalf("events %d %s", status, raw)
	}
	return mapsOf(t, asMap(t, raw)["events"])
}

func apiGet(t *testing.T, base, path string) map[string]any {
	t.Helper()
	return apiCallOK(t, base, http.MethodGet, path, nil)
}

func apiCallOK(t *testing.T, base, method, path string, body any) map[string]any {
	t.Helper()
	status, raw := apiCall(t, base, method, path, "", body)
	if status != http.StatusOK {
		t.Fatalf("%s %s %d %s", method, path, status, raw)
	}
	return asMap(t, raw)
}

var seenMessagePosts struct {
	sync.Mutex
	urls []string
}

func noteMessagePost(rawURL string) {
	seenMessagePosts.Lock()
	seenMessagePosts.urls = append(seenMessagePosts.urls, rawURL)
	seenMessagePosts.Unlock()
}

func messagePostsTo(base string) int {
	seenMessagePosts.Lock()
	defer seenMessagePosts.Unlock()
	n := 0
	for _, rawURL := range seenMessagePosts.urls {
		if strings.HasPrefix(rawURL, base) {
			n++
		}
	}
	return n
}

func apiCall(t *testing.T, base, method, path, idem string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !strings.Contains(path, "/health") {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if method == http.MethodPost && strings.Contains(path, "/messages") {
		noteMessagePost(base + path)
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

func mapsOf(t *testing.T, v any) []map[string]any {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("list = %#v", v)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any))
	}
	return out
}
