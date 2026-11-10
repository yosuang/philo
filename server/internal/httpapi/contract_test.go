package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/auth"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/idempotency"
	"github.com/yosuang/philo/server/internal/store"
)

func TestT1SameDefaultConversation(t *testing.T) {
	srv, _ := startProduction(t, config.Config{})
	agentID := createAgent(t, srv, "Ada")
	historical := createSession(t, srv, agentID)

	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i] = openDefault(t, srv, agentID)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range ids {
		if ids[i] == "" || ids[i] != ids[0] || ids[i] == historical {
			t.Fatalf("open ids = %#v historical = %s", ids, historical)
		}
	}
	for i := 0; i < n; i++ {
		if got := openDefault(t, srv, agentID); got != ids[0] {
			t.Fatalf("second open = %s want %s", got, ids[0])
		}
	}
	if status, body := do(t, srv, http.MethodGet, "/v1/sessions/"+historical, nil); status != http.StatusOK {
		t.Fatalf("historical session %d %s", status, body)
	}
}

func TestT2LostResponseReplaysOneTurn(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	before := effectCounts(t, pool, conversationID)

	const key = "client-key-t2"
	status, body := postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("first %d %s", status, body)
	}
	first := asMap(t, body)
	_ = first
	status, body = postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("replay %d %s", status, body)
	}
	replay := asMap(t, body)
	if replay["replayed"] != true {
		t.Fatalf("replayed = %#v", replay["replayed"])
	}
	if replay["turnId"] == key || replay["turnId"] == "" {
		t.Fatalf("turn id = %#v", replay["turnId"])
	}
	thirdStatus, thirdBody := postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if replay["turnId"] != asMap(t, mustRead(t, thirdStatus, thirdBody))["turnId"] {
		t.Fatal("third replay changed the turn")
	}
	if replay["execution"] != "unavailable" {
		t.Fatalf("execution = %#v", replay["execution"])
	}
	after := effectCounts(t, pool, conversationID)
	if after != before.add(1, 1, 1, 1) {
		t.Fatalf("effects before %#v after %#v", before, after)
	}
	status, body = postSubmit(t, srv, testToken, conversationID, "other-key", "Hello")
	if status != http.StatusOK {
		t.Fatalf("new key %d %s", status, body)
	}
	if asMap(t, body)["turnId"] == replay["turnId"] {
		t.Fatal("new key reused the turn")
	}
	if effectCounts(t, pool, conversationID) != after.add(1, 1, 1, 1) {
		t.Fatal("new key did not add one submit")
	}
}

func TestT10ConflictAndConcurrentPayloads(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	const key = "client-key-t10"
	status, body := postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("store %d %s", status, body)
	}
	stored := asMap(t, body)
	counts := effectCounts(t, pool, conversationID)
	status, body = postSubmit(t, srv, testToken, conversationID, key, "Hello ")
	if status != http.StatusConflict || errCode(t, body) != "idempotency_conflict" {
		t.Fatalf("conflict %d %s", status, body)
	}
	if effectCounts(t, pool, conversationID) != counts {
		t.Fatal("conflict added a side effect")
	}
	status, body = postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK || asMap(t, body)["turnId"] != stored["turnId"] {
		t.Fatalf("stored turn changed %d %s", status, body)
	}
	user := asMap(t, body)["userMessage"].(map[string]any)
	if user["content"] != "Hello" {
		t.Fatalf("content = %#v", user["content"])
	}

	other := openDefault(t, srv, createAgent(t, srv, "Bea"))
	var wg sync.WaitGroup
	start := make(chan struct{})
	statuses := make([]int, 2)
	bodies := make([][]byte, 2)
	payloads := []string{"P1", "P2"}
	for i := range payloads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i], bodies[i] = postSubmit(t, srv, testToken, other, "race-key", payloads[i])
		}(i)
	}
	close(start)
	wg.Wait()
	wins, conflicts := 0, 0
	var winner string
	for i := range statuses {
		switch statuses[i] {
		case http.StatusOK:
			wins++
			winner, _ = asMap(t, bodies[i])["userMessage"].(map[string]any)["content"].(string)
		case http.StatusConflict:
			if errCode(t, bodies[i]) != "idempotency_conflict" {
				t.Fatalf("race body %s", bodies[i])
			}
			conflicts++
		default:
			t.Fatalf("race status %d %s", statuses[i], bodies[i])
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins %d conflicts %d", wins, conflicts)
	}
	if effectCounts(t, pool, other).turns != 1 || effectCounts(t, pool, other).messages != 1 {
		t.Fatalf("race effects %#v", effectCounts(t, pool, other))
	}
	if winner != "P1" && winner != "P2" {
		t.Fatalf("winner %q", winner)
	}
}

func TestScopePartsDoNotCollide(t *testing.T) {
	other := auth.Credential{Token: "other-token", ActorID: "actor-other", OwnerID: "owner-other"}
	srv, pool := startProduction(t, config.Config{Credentials: append(testCredentials(), other)})
	ada := createAgent(t, srv, "Ada")
	first := openDefault(t, srv, ada)
	second := createSession(t, srv, ada)
	const key = "shared-key"
	status, body := postSubmit(t, srv, testToken, first, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("first %d %s", status, body)
	}
	turnID, _ := asMap(t, body)["turnId"].(string)
	status, body = postSubmit(t, srv, testToken, second, key, "Hello")
	if status != http.StatusOK || asMap(t, body)["turnId"] == turnID {
		t.Fatalf("other conversation %d %s", status, body)
	}

	status, raw := doAs(t, srv, other.Token, http.MethodPost, "/v1/agents", map[string]any{
		"name": "Cara", "instructions": "Be brief.", "modelId": "local",
	})
	if status != http.StatusOK {
		t.Fatalf("other owner agent %d %s", status, raw)
	}
	otherAgent := asMap(t, raw)["id"].(string)
	otherConversation := openDefaultAs(t, srv, other.Token, otherAgent)
	status, body = postSubmit(t, srv, other.Token, otherConversation, key, "Hello")
	if status != http.StatusOK || asMap(t, body)["turnId"] == turnID {
		t.Fatalf("other owner %d %s", status, body)
	}

	st := store.New(pool)
	hash, err := idempotency.Hash(idempotency.SubmitPayload("Hello", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	noop := func(context.Context, pgx.Tx) (store.AcceptResult, error) {
		return store.AcceptResult{}, nil
	}
	if _, err := st.Accept(ctx, store.Scope{
		OwnerID: testOwner, ConversationID: first, Kind: "cancel", TargetRef: turnID, Key: key,
	}, testActor, hash, noop); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Accept(ctx, store.Scope{
		OwnerID: testOwner, ConversationID: first, Kind: "cancel", TargetRef: "other-target", Key: key,
	}, testActor, hash, noop); err != nil {
		t.Fatal(err)
	}
	var accepts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_accepts WHERE idempotency_key = $1`, key).Scan(&accepts); err != nil {
		t.Fatal(err)
	}
	if accepts != 5 {
		t.Fatalf("accepts = %d, want 5 (three submits across scope, two cancels)", accepts)
	}
	if effectCounts(t, pool, first).messages != 1 {
		t.Fatal("kind or target accept added a message")
	}
}

func TestT6SeqOrderAndStatusUpdate(t *testing.T) {
	srv, pool := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	ts := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	firstID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	secondID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO messages (id, session_id, role, content, status, created_at, updated_at)
		VALUES
			($1, $3, 'user', 'first-by-seq', 'complete', $4, $4),
			($2, $3, 'user', 'second-by-seq', 'complete', $4, $4)`,
		firstID, secondID, conversationID, ts); err != nil {
		t.Fatal(err)
	}
	messages := messageList(t, srv, conversationID)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0]["id"] != firstID || messages[1]["id"] != secondID {
		t.Fatalf("order = %#v", []any{messages[0]["id"], messages[1]["id"]})
	}
	if messages[0]["createdAt"] != messages[1]["createdAt"] {
		t.Fatalf("timestamps differ: %#v %#v", messages[0]["createdAt"], messages[1]["createdAt"])
	}
	seq0, _ := messages[0]["seq"].(float64)
	seq1, _ := messages[1]["seq"].(float64)
	if seq0 >= seq1 {
		t.Fatalf("seq = %v %v", seq0, seq1)
	}
	before := len(messages)
	status, body := postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"conversationId": conversationID,
		"eventKey":       "status-1",
		"type":           "message.updated",
		"messageId":      secondID,
		"status":         "error",
	})
	if status != http.StatusOK || asMap(t, body)["type"] != "message.updated" {
		t.Fatalf("update %d %s", status, body)
	}
	messages = messageList(t, srv, conversationID)
	if len(messages) != before {
		t.Fatalf("status update inserted a row: %#v", messages)
	}
	if messages[1]["id"] != secondID || messages[1]["seq"].(float64) != seq1 || messages[1]["status"] != "error" {
		t.Fatalf("updated row = %#v", messages[1])
	}
	if messages[0]["seq"].(float64) != seq0 {
		t.Fatal("other seq changed")
	}
}

func TestT9RestartKeepsMapping(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	const key = "client-key-t9"
	status, body := postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, body)
	}
	turnID, _ := asMap(t, body)["turnId"].(string)
	ctx := context.Background()
	messageID := "11111111-1111-4111-8111-111111111111"
	approvalID := "22222222-2222-4222-8222-222222222222"
	taskID := "33333333-3333-4333-8333-333333333333"
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO messages (id, session_id, role, content, status, created_at, updated_at)
		VALUES ($1, $2, 'assistant', '', 'streaming', $3, $3)`,
		messageID, conversationID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO approvals (id, session_id, message_id, kind, summary, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'draft_action', 'note', 'approved', $4, $4)`,
		approvalID, conversationID, messageID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tasks (id, session_id, approval_id, status, source, result, created_at, updated_at)
		VALUES ($1, $2, $3, 'running', 'philo-server', '', $4, $4)`,
		taskID, conversationID, approvalID, now); err != nil {
		t.Fatal(err)
	}
	app, err := New(pool, config.Config{Credentials: testCredentials()})
	if err != nil {
		t.Fatal(err)
	}
	app.Stop()
	var taskStatus, messageStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "running" {
		t.Fatalf("startup task status = %s", taskStatus)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM messages WHERE id = $1`, messageID).Scan(&messageStatus); err != nil {
		t.Fatal(err)
	}
	if messageStatus != "streaming" {
		t.Fatalf("startup message status = %s", messageStatus)
	}
	counts := effectCounts(t, pool, conversationID)
	status, body = postSubmit(t, srv, testToken, conversationID, key, "Hello")
	if status != http.StatusOK {
		t.Fatalf("replay %d %s", status, body)
	}
	replay := asMap(t, body)
	if replay["turnId"] != turnID || replay["replayed"] != true {
		t.Fatalf("replay = %#v", replay)
	}
	if effectCounts(t, pool, conversationID) != counts {
		t.Fatal("restart replay added a side effect")
	}
}

func TestT3ResumeFromHighWater(t *testing.T) {
	srv, _ := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	seed := fixtureOK(t, srv, conversationID, "seed", "fixture.seed", "", "", "")
	messageID, _ := seed["messageId"].(string)
	first := fixtureOK(t, srv, conversationID, "d1", "message.delta", messageID, "red", "")
	cursor, _ := first["cursor"].(float64)
	fixtureOK(t, srv, conversationID, "d2", "message.delta", messageID, "green", "")
	fixtureOK(t, srv, conversationID, "done", "message.completed", messageID, "", "")
	again := fixtureOK(t, srv, conversationID, "d2", "message.delta", messageID, "green", "")
	if again["id"] != fixtureEventID(t, srv, conversationID, int64(cursor), "d2") && again["type"] != "message.delta" {
		t.Fatalf("repeat = %#v", again)
	}

	view := newView()
	view.byID[messageID] = &viewMessage{content: "red", status: "streaming"}
	view.rows = 1
	view.applyAll(eventsAfter(t, srv, conversationID, int64(cursor)))
	view.applyAll(eventsAfter(t, srv, conversationID, int64(cursor)))
	msg := view.byID[messageID]
	if msg.content != "redgreen" || msg.status != "complete" {
		t.Fatalf("applied = %+v", msg)
	}
	if view.rows != 1 {
		t.Fatalf("rows = %d", view.rows)
	}
	stored := messageList(t, srv, conversationID)
	if len(stored) != 1 || stored[0]["content"] != "redgreen" || stored[0]["status"] != "complete" {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestT5ExpiredCursorRebuildsSnapshot(t *testing.T) {
	srv, pool := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	seed := fixtureOK(t, srv, conversationID, "seed", "fixture.seed", "", "", "")
	messageID, _ := seed["messageId"].(string)
	first := fixtureOK(t, srv, conversationID, "d1", "message.delta", messageID, "red", "")
	fixtureOK(t, srv, conversationID, "d2", "message.delta", messageID, "green", "")
	cutoff, _ := first["cursor"].(float64)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		DELETE FROM fixture_business_dedup
		WHERE business_event_id IN (
			SELECT id FROM business_events WHERE conversation_id = $1 AND cursor <= $2
		)`, conversationID, int64(cutoff)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM business_events WHERE conversation_id = $1 AND cursor <= $2`,
		conversationID, int64(cutoff)); err != nil {
		t.Fatal(err)
	}
	status, body := getEvents(t, srv, conversationID, 0)
	if status != http.StatusBadRequest || errCode(t, body) != "invalid_request" {
		t.Fatalf("expired %d %s", status, body)
	}
	payload := asMap(t, body)
	if _, ok := payload["events"]; ok {
		t.Fatalf("expired response embedded a log: %s", body)
	}
	snap := payload["snapshot"].(map[string]any)
	if _, ok := snap["events"]; ok {
		t.Fatal("snapshot embedded a log")
	}
	high, _ := snap["highWater"].(float64)
	messages := snap["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "redgreen" {
		t.Fatalf("snapshot messages = %#v", messages)
	}
	fresh := asMap(t, mustGET(t, srv, "/v1/sessions/"+conversationID+"/snapshot"))
	if fresh["highWater"] != snap["highWater"] {
		t.Fatalf("high water %#v vs %#v", fresh["highWater"], snap["highWater"])
	}
	status, body = getEvents(t, srv, conversationID, int64(high))
	if status != http.StatusOK {
		t.Fatalf("resume %d %s", status, body)
	}
	if len(asMap(t, body)["events"].([]any)) != 0 {
		t.Fatalf("events after waterline = %s", body)
	}
}

func TestT13ConcurrentSnapshotHasNoGap(t *testing.T) {
	srv, pool := startProduction(t, config.Config{BusinessFixture: true})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	seed := fixtureOK(t, srv, conversationID, "seed", "fixture.seed", "", "", "")
	messageID, _ := seed["messageId"].(string)
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; i < 25; i++ {
			select {
			case <-stop:
				return
			default:
			}
			fixtureOK(t, srv, conversationID, "c"+itoa(i), "message.delta", messageID, "x", "")
		}
	}()

	st := store.New(pool)
	var saved store.Snapshot
	var savedReflected []store.BusinessEvent
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && saved.HighWater == 0 {
		snap, reflected, err := st.ConsistentView(context.Background(), testOwner, conversationID)
		if err != nil {
			close(stop)
			writer.Wait()
			t.Fatal(err)
		}
		if !snapshotMatches(snap, reflected, messageID) {
			close(stop)
			writer.Wait()
			t.Fatalf("snapshot high %d does not match reflected events", snap.HighWater)
		}
		saved = snap
		savedReflected = reflected
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	writer.Wait()
	finalSnap, finalEvents, err := st.ConsistentView(context.Background(), testOwner, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotMatches(finalSnap, finalEvents, messageID) {
		t.Fatal("final snapshot mismatched its high water")
	}
	base := ""
	for _, message := range saved.Messages {
		if message.ID == messageID {
			base = message.Content
		}
	}
	_ = savedReflected
	extra := ""
	seen := map[string]bool{}
	apply := func() {
		for _, ev := range finalEvents {
			if ev.Cursor <= saved.HighWater || ev.Type != "message.delta" || ev.MessageID != messageID {
				continue
			}
			if seen[ev.ID] {
				continue
			}
			seen[ev.ID] = true
			var payload map[string]any
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			extra += str(payload["text"])
		}
	}
	apply()
	once := extra
	apply()
	if extra != once {
		t.Fatal("reapplying an event appended twice")
	}
	want := ""
	for _, message := range finalSnap.Messages {
		if message.ID == messageID {
			want = message.Content
		}
	}
	if base+extra != want {
		t.Fatalf("replay %q + base %q != final %q", extra, base, want)
	}
	have := map[int64]bool{}
	for _, ev := range finalEvents {
		if ev.Cursor > saved.HighWater {
			have[ev.Cursor] = true
		}
	}
	for cursor := saved.HighWater + 1; cursor <= finalSnap.HighWater; cursor++ {
		if !have[cursor] {
			t.Fatalf("missing cursor %d between %d and %d", cursor, saved.HighWater, finalSnap.HighWater)
		}
	}
}

func TestLogicalSessionSpansTurnsNotAttempts(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	conversationID := openDefault(t, srv, createAgent(t, srv, "Ada"))
	st := store.New(pool)
	ctx := context.Background()
	first, err := st.OpenPersonalSession(ctx, testOwner, conversationID, "pin-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.OpenPersonalSession(ctx, testOwner, conversationID, "pin-a")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("personal ids %s %s", first, second)
	}
	otherPin, err := st.OpenPersonalSession(ctx, testOwner, conversationID, "pin-b")
	if err != nil {
		t.Fatal(err)
	}
	if otherPin == first {
		t.Fatal("a different pin reused the personal session")
	}
	worker, err := st.OpenWorkerSession(ctx, testOwner, "worker-run-1", "pin-a")
	if err != nil {
		t.Fatal(err)
	}
	workerAgain, err := st.OpenWorkerSession(ctx, testOwner, "worker-run-1", "pin-a")
	if err != nil {
		t.Fatal(err)
	}
	if worker == "" || worker != workerAgain || worker == first {
		t.Fatalf("worker %s again %s personal %s", worker, workerAgain, first)
	}
}

func TestBusinessRoutesRequireActor(t *testing.T) {
	srv, pool := startProduction(t, config.Config{})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/agents", bytes.NewReader([]byte(`{"name":"Ada","instructions":"Be brief.","modelId":"local","ownerId":"client-owner"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || errCode(t, body) != "authentication_required" {
		t.Fatalf("unauthenticated %d %s", resp.StatusCode, body)
	}
	var agents int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM agents`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != 0 {
		t.Fatalf("agents = %d", agents)
	}
	status, raw := do(t, srv, http.MethodGet, "/v1/health", nil)
	if status != http.StatusOK || asMap(t, raw)["adapter"] != "unavailable" {
		t.Fatalf("health %d %s", status, raw)
	}
}

type effectCount struct {
	messages int
	wakes    int
	outbox   int
	turns    int
}

func (e effectCount) add(messages, wakes, outbox, turns int) effectCount {
	e.messages += messages
	e.wakes += wakes
	e.outbox += outbox
	e.turns += turns
	return e
}

func effectCounts(t *testing.T, pool *pgxpool.Pool, conversationID string) effectCount {
	t.Helper()
	var got effectCount
	err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM messages WHERE session_id = $1),
			(SELECT count(*) FROM wake_intents WHERE conversation_id = $1),
			(SELECT count(*) FROM outbox WHERE conversation_id = $1),
			(SELECT count(*) FROM turns WHERE conversation_id = $1)`,
		conversationID).Scan(&got.messages, &got.wakes, &got.outbox, &got.turns)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func openDefault(t *testing.T, srv *httptest.Server, agentID string) string {
	t.Helper()
	return openDefaultAs(t, srv, testToken, agentID)
}

func openDefaultAs(t *testing.T, srv *httptest.Server, token, agentID string) string {
	t.Helper()
	status, body := doAs(t, srv, token, http.MethodPost, "/v1/agents/"+agentID+"/conversation", nil)
	if status != http.StatusOK {
		t.Fatalf("open %d %s", status, body)
	}
	return asMap(t, body)["id"].(string)
}

func postSubmit(t *testing.T, srv *httptest.Server, token, conversationID, key, content string) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"content": content, "options": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+conversationID+"/messages", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func doAs(t *testing.T, srv *httptest.Server, token, method, path string, body any) (int, []byte) {
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
	req.Header.Set("Authorization", "Bearer "+token)
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

func postFixture(t *testing.T, srv *httptest.Server, body map[string]any) (int, []byte) {
	t.Helper()
	return do(t, srv, http.MethodPost, "/v1/fixture-business-source", body)
}

func fixtureOK(t *testing.T, srv *httptest.Server, conversationID, eventKey, typ, messageID, text, status string) map[string]any {
	t.Helper()
	code, body := postFixture(t, srv, map[string]any{
		"source":         store.FixtureSource,
		"conversationId": conversationID,
		"eventKey":       eventKey,
		"type":           typ,
		"messageId":      messageID,
		"text":           text,
		"status":         status,
	})
	if code != http.StatusOK {
		t.Fatalf("fixture %s %d %s", typ, code, body)
	}
	return asMap(t, body)
}

func getEvents(t *testing.T, srv *httptest.Server, conversationID string, after int64) (int, []byte) {
	t.Helper()
	return do(t, srv, http.MethodGet, "/v1/sessions/"+conversationID+"/business-events?after="+itoa64(after), nil)
}

func eventsAfter(t *testing.T, srv *httptest.Server, conversationID string, after int64) []map[string]any {
	t.Helper()
	status, body := getEvents(t, srv, conversationID, after)
	if status != http.StatusOK {
		t.Fatalf("events %d %s", status, body)
	}
	raw := asMap(t, body)["events"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}

func messageList(t *testing.T, srv *httptest.Server, conversationID string) []map[string]any {
	t.Helper()
	body := mustGET(t, srv, "/v1/sessions/"+conversationID+"/messages")
	raw := asMap(t, body)["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}

func mustRead(t *testing.T, status int, body []byte) []byte {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, body)
	}
	return body
}

func fixtureEventID(t *testing.T, srv *httptest.Server, conversationID string, after int64, eventKey string) string {
	t.Helper()
	for _, ev := range eventsAfter(t, srv, conversationID, after-1) {
		if ev["type"] == "message.delta" && ev["text"] == "green" {
			id, _ := ev["id"].(string)
			return id
		}
	}
	t.Fatalf("missing repeated delta for %s", eventKey)
	return ""
}

type viewMessage struct {
	content string
	status  string
	seq     float64
}

type clientView struct {
	byID map[string]*viewMessage
	seen map[string]bool
	rows int
}

func newView() *clientView {
	return &clientView{byID: map[string]*viewMessage{}, seen: map[string]bool{}}
}

func (v *clientView) applyAll(events []map[string]any) {
	for _, ev := range events {
		v.apply(ev)
	}
}

func (v *clientView) apply(ev map[string]any) {
	id, _ := ev["id"].(string)
	if id != "" && v.seen[id] {
		return
	}
	if id != "" {
		v.seen[id] = true
	}
	switch ev["type"] {
	case "message.created":
		msg, _ := ev["message"].(map[string]any)
		messageID, _ := msg["id"].(string)
		if _, ok := v.byID[messageID]; ok {
			return
		}
		v.byID[messageID] = &viewMessage{
			content: str(msg["content"]),
			status:  str(msg["status"]),
			seq:     num(msg["seq"]),
		}
		v.rows++
	case "message.delta":
		messageID := str(ev["messageId"])
		msg := v.byID[messageID]
		if msg == nil {
			msg = &viewMessage{status: "streaming"}
			v.byID[messageID] = msg
			v.rows++
		}
		msg.content += str(ev["text"])
	case "message.completed":
		messageID := str(ev["messageId"])
		msg := v.byID[messageID]
		if msg == nil {
			msg = &viewMessage{}
			v.byID[messageID] = msg
			v.rows++
		}
		msg.status = "complete"
	case "message.updated":
		messageID := str(ev["messageId"])
		msg := v.byID[messageID]
		if msg == nil {
			return
		}
		msg.status = str(ev["status"])
	}
}

func snapshotMatches(snap store.Snapshot, reflected []store.BusinessEvent, messageID string) bool {
	var content string
	var found bool
	for _, message := range snap.Messages {
		if message.ID == messageID {
			content = message.Content
			found = true
		}
	}
	if !found {
		return false
	}
	var built string
	for _, ev := range reflected {
		if ev.Cursor > snap.HighWater {
			return false
		}
		if ev.Type == "message.delta" && ev.MessageID == messageID {
			var payload map[string]any
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				return false
			}
			built += str(payload["text"])
		}
	}
	return built == content
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	n, _ := v.(float64)
	return n
}

func itoa(n int) string {
	return itoa64(int64(n))
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
