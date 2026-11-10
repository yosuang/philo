// Package httpapi is the phase-1 HTTP and WebSocket surface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/adapter"
	"github.com/yosuang/philo/server/internal/adapter/dev"
	"github.com/yosuang/philo/server/internal/auth"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/engine"
	"github.com/yosuang/philo/server/internal/events"
	"github.com/yosuang/philo/server/internal/hub"
	"github.com/yosuang/philo/server/internal/idempotency"
	"github.com/yosuang/philo/server/internal/makez"
	"github.com/yosuang/philo/server/internal/store"
)

const (
	maxName         = 200
	maxInstructions = 20000
	maxModelID      = 200
	maxTitle        = 200
	maxContent      = 100000
)

const (
	// resumeAttemptTimeout bounds one events read, including a body that never ends.
	resumeAttemptTimeout = 2 * time.Second
	// resumeInterval is the wait between attempts inside the fast budget. The loop does not spin.
	resumeInterval = 200 * time.Millisecond
	// resumeBudget is the fast phase. When it ends the process logs startup resume stopped.
	// It is not the end of reading a source that is still linked.
	resumeBudget = 12 * time.Second
	// resumeSlowInterval is the wait after that stop. It is long enough that the loop is not busy.
	resumeSlowInterval = 2 * time.Second
)

// App is the session server handler.
type App struct {
	Handler http.Handler
	engine  *engine.Engine
	resume  func(context.Context) (int, error)
}

// Stop cancels in-flight generations.
func (a *App) Stop() {
	if a != nil && a.engine != nil {
		a.engine.Stop()
	}
}

// ResumeCommitted reads fixture sources already linked on stored turns.
// It does not submit a task. An unset binding does not dial out.
// The count is how many owned turns were eligible. A source error is logged
// and does not discard the rest of the list.
func (a *App) ResumeCommitted(ctx context.Context) (int, error) {
	if a == nil || a.resume == nil {
		return 0, nil
	}
	return a.resume(ctx)
}

// WatchCommitted reads linked fixture sources beside listen.
// The first 12 seconds use a short interval. That phase logs startup resume
// stopped and does not abandon a turn that is still linked. Later reads wait
// resumeSlowInterval, and each read still stops at resumeAttemptTimeout.
// No owned turn means stop. An unset binding never enters this loop.
func (a *App) WatchCommitted(ctx context.Context) {
	if a == nil || a.resume == nil {
		return
	}
	deadline := time.Now().Add(resumeBudget)
	slow := false
	for {
		attempt, cancel := context.WithTimeout(ctx, resumeAttemptTimeout)
		n, err := a.resume(attempt)
		cancel()
		if err != nil {
			log.Printf("startup resume: %s", err)
		}
		if n == 0 || ctx.Err() != nil {
			log.Printf("startup resume stopped linked=%d", n)
			return
		}
		interval := resumeInterval
		if !time.Now().Before(deadline) {
			if !slow {
				slow = true
				log.Printf("startup resume stopped linked=%d", n)
			}
			interval = resumeSlowInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type stagedSuffix struct {
	messageID string
	text      string
}

type server struct {
	store *store.Store
	eng   *engine.Engine
	hub   *hub.Hub
	cfg   config.Config
	authn auth.Authenticator
	http  *http.Client
	live  *makez.Client

	suffixMu      sync.Mutex
	pendingSuffix map[string]stagedSuffix

	hold *subscribeHold

	duringMu   sync.Mutex
	duringRead map[string]parkedDelta

	failMu    sync.Mutex
	failReads map[string]struct{}
}

// parkedDelta is one fixture business event applied while a read is in
// progress. The read's response was already queried, so the event is not in
// that body. It is published on the socket instead.
type parkedDelta struct {
	key       string
	messageID string
	text      string
}

// New builds the HTTP handler. The pool must already be migrated.
// An unknown PHILO_MODEL_ADAPTER is refused. Unset does not select dev.
// The dev adapter runs only when ExecutionFixture is dev.
func New(pool *pgxpool.Pool, cfg config.Config) (*App, error) {
	ad, err := newAdapter(cfg)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	if err := cfg.LiveMakez(); err != nil {
		return nil, err
	}
	var live *makez.Client
	if cfg.MakezTestBinding() {
		live, err = makez.NewClient(cfg.MakezAPIURL, cfg.MakezAPIToken, cfg.MakezWorkspaceID)
		if err != nil {
			return nil, err
		}
	}
	st := store.New(pool)
	h := hub.New()
	var resume func(context.Context) (int, error)
	if live != nil && !cfg.DevFixture() {
		workspace := cfg.MakezWorkspaceID
		client := live.WithTimeout(resumeAttemptTimeout)
		resume = func(ctx context.Context) (int, error) {
			return resumeCommitted(ctx, st, client, workspace, h)
		}
	}
	var hold *subscribeHold
	if cfg.BusinessFixture {
		hold = newSubscribeHold()
		h.SetAcker(hold)
	}
	eng := engine.New(st, ad, h, cfg.DevFixture())
	s := &server{
		store:         st,
		eng:           eng,
		hub:           h,
		cfg:           cfg,
		authn:         auth.New(cfg.Credentials),
		http:          &http.Client{Timeout: 2 * time.Second},
		live:          live,
		pendingSuffix: map[string]stagedSuffix{},
		hold:          hold,
		duringRead:    map[string]parkedDelta{},
		failReads:     map[string]struct{}{},
	}
	mux := http.NewServeMux()
	s.routes(mux)
	return &App{Handler: mux, engine: eng, resume: resume}, nil
}

// resumeCommitted continues each owned turn that already stores a task in workspace.
// The owner and the bot are the stored rows. No new owner or binding is created.
// A turn that has no task is left alone, so startup does not submit one.
// A bot owned by someone else is not dialed. A source error does not stop the others.
func resumeCommitted(ctx context.Context, st *store.Store, client *makez.Client, workspace string, h *hub.Hub) (int, error) {
	turns, err := st.LinkedTurns(ctx, workspace)
	if err != nil {
		return 0, err
	}
	log.Printf("startup resume linked=%d", len(turns))
	for _, turn := range turns {
		owned, err := st.AgentOwnedBy(ctx, turn.Binding.BotID, turn.Binding.OwnerID)
		if err != nil {
			log.Printf("startup resume owner check task=%s: %s", turn.Link.TaskID, err)
			continue
		}
		if !owned {
			log.Printf("startup resume skipped foreign bot task=%s", turn.Link.TaskID)
			continue
		}
		after, err := st.AppliedPublicSeq(ctx, turn.Link)
		if err != nil {
			log.Printf("startup resume checkpoint task=%s: %s", turn.Link.TaskID, err)
			continue
		}
		log.Printf("startup resume workspace=%s task=%s after=%d", turn.Link.WorkspaceID, turn.Link.TaskID, after)
		result, err := makez.ResumeLinked(ctx, st, client, turn.Binding)
		if err != nil {
			log.Printf("startup resume task=%s: %s", turn.Link.TaskID, err)
			continue
		}
		// The row is already committed. Publish is how a subscribed client
		// sees a log that arrived after this process was listening. A client
		// that was down reads the same row from its cursor.
		for _, ev := range result.Project.Events {
			item, err := ev.MarshalMap()
			if err != nil {
				log.Printf("startup resume publish task=%s: %s", turn.Link.TaskID, err)
				continue
			}
			if h != nil {
				h.Publish(turn.Binding.ConversationID, item)
			}
			log.Printf("startup resume published session=%s cursor=%d", turn.Binding.ConversationID, ev.Cursor)
		}
	}
	return len(turns), nil
}

func newAdapter(cfg config.Config) (adapter.Adapter, error) {
	if cfg.DevFixture() {
		return dev.New(cfg.DevChunkDelay), nil
	}
	switch cfg.Adapter {
	case "", "dev", "provider", "unavailable":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown PHILO_MODEL_ADAPTER %q", cfg.Adapter)
	}
}

func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/agents", s.createAgent)
	mux.HandleFunc("GET /v1/agents", s.listAgents)
	mux.HandleFunc("GET /v1/agents/{id}", s.getAgent)
	mux.HandleFunc("PATCH /v1/agents/{id}", s.patchAgent)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /v1/sessions/{id}/messages", s.listMessages)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", s.postMessage)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/messages/{id}/retry", s.retry)
	mux.HandleFunc("GET /v1/approvals", s.listApprovals)
	mux.HandleFunc("POST /v1/approvals/{id}/edit", s.editApproval)
	mux.HandleFunc("POST /v1/approvals/{id}/decision", s.decide)
	mux.HandleFunc("GET /v1/tasks", s.listTasks)
	mux.HandleFunc("GET /v1/connectors", s.connectors)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/agents/{id}/conversation", s.openConversation)
	mux.HandleFunc("GET /v1/agents/{id}/conversation", s.getConversation)
	mux.HandleFunc("GET /v1/sessions/{id}/snapshot", s.snapshot)
	mux.HandleFunc("GET /v1/sessions/{id}/business-events", s.businessEvents)
	if s.cfg.BusinessFixture {
		mux.HandleFunc("POST /v1/fixture-business-source", s.fixtureBusiness)
	}
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	err := s.store.Ping(ctx)
	cancel()
	status := http.StatusOK
	ok := true
	if err != nil {
		status = http.StatusServiceUnavailable
		ok = false
	}
	conn := s.makezStatus(r.Context())
	writeJSON(w, status, map[string]any{
		"ok":      ok,
		"adapter": s.cfg.HealthAdapter(),
		"makez":   makez.HealthValue(conn),
	})
}

func (s *server) connectors(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.principal(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connectors": []makez.Connector{s.makezStatus(r.Context())},
	})
}

func (s *server) makezStatus(parent context.Context) makez.Connector {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	return makez.Status(ctx, s.cfg.MakezURL, s.http)
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	s.hub.Handle(w, r, func(ctx context.Context, id string) (bool, error) {
		return s.store.SessionOwned(ctx, p.OwnerID, id)
	})
}

func (s *server) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, err := s.authn.Authenticate(r.Header.Get("Authorization"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return auth.Principal{}, false
	}
	if err := s.store.EnsurePrincipal(r.Context(), p); err != nil {
		s.internal(w, err)
		return auth.Principal{}, false
	}
	return p, true
}

func (s *server) createAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
		ModelID      string `json:"modelId"`
	}
	if !decode(w, r, &body) {
		return
	}
	name, ok := required(w, body.Name, "name", maxName)
	if !ok {
		return
	}
	instructions, ok := bounded(w, body.Instructions, "instructions", maxInstructions)
	if !ok {
		return
	}
	modelID, ok := required(w, body.ModelID, "modelId", maxModelID)
	if !ok {
		return
	}
	agent, err := s.store.CreateAgent(r.Context(), p.OwnerID, name, instructions, modelID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) listAgents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	agents, err := s.store.ListAgents(r.Context(), p.OwnerID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *server) getAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	agent, err := s.store.RequireAgent(r.Context(), p.OwnerID, id)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) patchAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name         *string `json:"name"`
		Instructions *string `json:"instructions"`
		ModelID      *string `json:"modelId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name == nil && body.Instructions == nil && body.ModelID == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "no fields to update")
		return
	}
	if body.Name != nil {
		name, ok := required(w, *body.Name, "name", maxName)
		if !ok {
			return
		}
		body.Name = &name
	}
	if body.Instructions != nil {
		instructions, ok := bounded(w, *body.Instructions, "instructions", maxInstructions)
		if !ok {
			return
		}
		body.Instructions = &instructions
	}
	if body.ModelID != nil {
		modelID, ok := required(w, *body.ModelID, "modelId", maxModelID)
		if !ok {
			return
		}
		body.ModelID = &modelID
	}
	agent, err := s.store.UpdateAgent(r.Context(), p.OwnerID, id, body.Name, body.Instructions, body.ModelID)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		AgentID string `json:"agentId"`
		Title   string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	agentID, ok := uuidParam(w, body.AgentID, "agentId")
	if !ok {
		return
	}
	title, ok := bounded(w, body.Title, "title", maxTitle)
	if !ok {
		return
	}
	sess, err := s.store.CreateSession(r.Context(), p.OwnerID, agentID, title)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	agentID, ok := uuidParam(w, r.URL.Query().Get("agentId"), "agentId")
	if !ok {
		return
	}
	sessions, err := s.store.ListSessions(r.Context(), p.OwnerID, agentID)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *server) getSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sess, err := s.store.RequireSession(r.Context(), p.OwnerID, id)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, id); err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	messages, err := s.store.ListMessages(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *server) postMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, id); err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := decodeSubmit(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content is required")
		return
	}
	if utf8.RuneCountInString(body.Content) > maxContent {
		writeError(w, http.StatusBadRequest, "invalid_request", "content is too long")
		return
	}
	options, err := idempotency.ParseOptions(body.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "options must be a JSON object")
		return
	}
	hash, err := idempotency.Hash(idempotency.SubmitPayload(body.Content, options))
	if err != nil {
		s.internal(w, err)
		return
	}
	result, err := s.store.AcceptSubmit(r.Context(), store.SubmitInput{
		OwnerID:        p.OwnerID,
		ActorID:        p.ActorID,
		ConversationID: id,
		Key:            key,
		Content:        body.Content,
		Hash:           hash,
		Fixture:        s.cfg.DevFixture(),
	})
	if errors.Is(err, store.ErrBusy) {
		writeError(w, http.StatusConflict, "reply_in_progress", "this session already has a streaming reply")
		return
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "request conflicts with the stored operation")
		return
	}
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	if !result.Replayed {
		if sess, err := s.store.GetSession(r.Context(), id); err == nil {
			if ev, err := events.WithType("session.updated", sess); err == nil {
				s.hub.Publish(sess.ID, ev)
			}
		}
	}
	if s.cfg.DevFixture() && !result.Replayed && result.Assistant != nil {
		s.eng.StartFixture(result.Assistant.ID)
	}
	execution := "unavailable"
	if s.cfg.DevFixture() {
		execution = "fixture"
	}
	var projected *store.Message
	if s.live != nil && !s.cfg.DevFixture() {
		execution = "test-binding"
		sess, err := s.store.GetSession(r.Context(), id)
		if err != nil {
			s.storeErr(w, err, "session not found")
			return
		}
		binding := store.LiveBinding{
			Label:          store.TestBindingLabel,
			OwnerID:        p.OwnerID,
			ConversationID: id,
			TurnID:         result.TurnID,
			BotID:          sess.AgentID,
			WorkspaceID:    s.cfg.MakezWorkspaceID,
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, consumeErr := makez.ConsumeNoKey(ctx, s.store, s.live, binding)
		cancel()
		if consumeErr != nil {
			log.Printf("makez test binding consume: %v", consumeErr)
		}
		if msg, err := s.store.TurnAssistant(r.Context(), result.TurnID); err == nil {
			projected = &msg
		}
	}
	out := map[string]any{
		"turnId":      result.TurnID,
		"replayed":    result.Replayed,
		"execution":   execution,
		"userMessage": result.User,
	}
	if result.Assistant != nil {
		out["assistantMessage"] = result.Assistant
	}
	if projected != nil {
		out["assistantMessage"] = projected
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, id); err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	msg, err := s.eng.Cancel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "nothing_to_cancel", "this session has no streaming reply")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (s *server) retry(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.cfg.DevFixture() {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "execution is unavailable")
		return
	}
	msg, err := s.store.GetMessage(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "message not found")
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, msg.SessionID); err != nil {
		s.storeErr(w, err, "message not found")
		return
	}
	msg, err = s.eng.Retry(r.Context(), id)
	if errors.Is(err, store.ErrNotRetryable) {
		writeError(w, http.StatusConflict, "not_retryable", "only an assistant message in error or canceled can be retried")
		return
	}
	if errors.Is(err, store.ErrBusy) {
		writeError(w, http.StatusConflict, "reply_in_progress", "this session already has a streaming reply")
		return
	}
	if err != nil {
		s.storeErr(w, err, "message not found")
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (s *server) editApproval(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetApproval(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, current.SessionID); err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	var body struct {
		Kind      string `json:"kind"`
		Summary   string `json:"summary"`
		Owner     string `json:"owner"`
		OwnerID   string `json:"owner_id"`
		Requester string `json:"requester"`
	}
	if !decode(w, r, &body) {
		return
	}
	// Owner, OwnerID, and Requester are not credentials.
	_ = body.Owner
	_ = body.OwnerID
	_ = body.Requester
	kind := strings.TrimSpace(body.Kind)
	if kind == "" {
		kind = current.Kind
	}
	kind, ok = bounded(w, kind, "kind", maxModelID)
	if !ok {
		return
	}
	if kind == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "kind is required")
		return
	}
	summary, ok := required(w, body.Summary, "summary", maxContent)
	if !ok {
		return
	}
	previous, next, changed, err := s.store.ReplaceAction(r.Context(), id, p.ActorID, kind, summary)
	if errors.Is(err, store.ErrNotPending) {
		writeError(w, http.StatusConflict, "approval_not_pending", "approval is not pending")
		return
	}
	if err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	if changed {
		s.publishApproval("approval.updated", previous)
		s.publishApproval("approval.requested", next)
	}
	writeJSON(w, http.StatusOK, next)
}

func (s *server) publishApproval(eventType string, approval store.Approval) {
	ev, err := events.WithType(eventType, approval)
	if err != nil {
		log.Printf("approval event: %v", err)
		return
	}
	s.hub.Publish(approval.SessionID, ev)
}

func (s *server) listApprovals(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r.URL.Query().Get("sessionId"), "sessionId")
	if !ok {
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, sessionID); err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	approvals, err := s.store.ListApprovals(r.Context(), sessionID)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

func (s *server) decide(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetApproval(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, current.SessionID); err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	var body struct {
		Decision  string          `json:"decision"`
		Owner     string          `json:"owner"`
		OwnerID   string          `json:"owner_id"`
		Requester string          `json:"requester"`
		Snapshot  json.RawMessage `json:"actionSnapshot"`
	}
	if !decode(w, r, &body) {
		return
	}
	// A client-supplied owner, requester, or snapshot is not authentication
	// and does not replace the card.
	_ = body.Owner
	_ = body.OwnerID
	_ = body.Requester
	_ = body.Snapshot
	decision := strings.TrimSpace(body.Decision)
	if decision != "approve" && decision != "reject" {
		writeError(w, http.StatusBadRequest, "invalid_request", "decision must be approve or reject")
		return
	}
	if current.Status == "pending" && (current.RequesterActorID == "" || len(current.ActionSnapshot) == 0) {
		writeError(w, http.StatusForbidden, "permission_denied", "permission denied")
		return
	}
	approval, err := s.eng.Decide(r.Context(), id, decision)
	if errors.Is(err, store.ErrNotPending) {
		writeError(w, http.StatusConflict, "approval_not_pending", "approval is not pending")
		return
	}
	if err != nil {
		s.storeErr(w, err, "approval not found")
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r.URL.Query().Get("sessionId"), "sessionId")
	if !ok {
		return
	}
	if _, err := s.store.RequireSession(r.Context(), p.OwnerID, sessionID); err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	tasks, err := s.store.ListTasks(r.Context(), sessionID)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *server) storeErr(w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", notFound)
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		writeError(w, http.StatusForbidden, "permission_denied", "permission denied")
		return
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "request conflicts with the stored operation")
		return
	}
	s.internal(w, err)
}

func (s *server) internal(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func (s *server) openConversation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sess, err := s.store.OpenDefaultConversation(r.Context(), p.OwnerID, id)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) getConversation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sess, err := s.store.DefaultConversation(r.Context(), p.OwnerID, id)
	if err != nil {
		s.storeErr(w, err, "conversation not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	snap, err := s.store.Snapshot(r.Context(), p.OwnerID, id)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *server) businessEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("after"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is required")
		return
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "after must be a non-negative integer")
		return
	}
	if s.readsFailed(id) {
		log.Printf("business events http 500 session=%s after=%d", id, after)
		writeError(w, http.StatusInternalServerError, "internal", "business read failed")
		return
	}
	events, snap, err := s.store.BusinessEventsAfter(r.Context(), p.OwnerID, id, after)
	if errors.Is(err, store.ErrCursorExpired) {
		// The snapshot above does not include a suffix staged by the fixture.
		// Appending it now lets the client's next read follow that suffix once.
		if staged, ok := s.takeStagedSuffix(id); ok {
			ev, applyErr := s.store.ApplyFixtureBusiness(r.Context(), store.FixtureInput{
				Source:         store.FixtureSource,
				OwnerID:        p.OwnerID,
				ConversationID: id,
				EventKey:       "staged-suffix-" + uuid.NewString(),
				Type:           "message.delta",
				MessageID:      staged.messageID,
				Text:           staged.text,
			})
			if applyErr != nil {
				s.stageSuffix(id, staged)
				s.storeErr(w, applyErr, "session not found")
				return
			}
			if item, err := ev.MarshalMap(); err == nil {
				s.hub.Publish(id, item)
			}
		}
		log.Printf("business cursor expired session=%s after=%d high=%d", id, after, snap.HighWater)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "invalid_request",
				"message": "business cursor expired",
			},
			"snapshot": snap,
		})
		return
	}
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	// A fixture event armed for this read is committed after the query, so
	// this response does not contain it. The socket publish is the copy the
	// client keeps from the live buffer.
	if parked, ok := s.takeDuringRead(id); ok {
		ev, applyErr := s.store.ApplyFixtureBusiness(r.Context(), store.FixtureInput{
			Source:         store.FixtureSource,
			OwnerID:        p.OwnerID,
			ConversationID: id,
			EventKey:       parked.key,
			Type:           "message.delta",
			MessageID:      parked.messageID,
			Text:           parked.text,
		})
		if applyErr != nil {
			log.Printf("business event during read: %v", applyErr)
			s.stageDuringRead(id, parked)
		} else if item, err := ev.MarshalMap(); err == nil {
			s.hub.Publish(id, item)
			log.Printf("business event during read session=%s cursor=%d", id, ev.Cursor)
		}
	}
	out := make([]any, 0, len(events))
	for _, ev := range events {
		item, err := ev.MarshalMap()
		if err != nil {
			s.internal(w, err)
			return
		}
		out = append(out, item)
	}
	log.Printf("business events session=%s after=%d n=%d", id, after, len(out))
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (s *server) fixtureBusiness(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Source         string `json:"source"`
		ConversationID string `json:"conversationId"`
		EventKey       string `json:"eventKey"`
		Type           string `json:"type"`
		MessageID      string `json:"messageId"`
		Text           string `json:"text"`
		Status         string `json:"status"`
		Suffix         string `json:"suffix"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Source != store.FixtureSource {
		writeError(w, http.StatusBadRequest, "invalid_request", "fixture source label is required")
		return
	}
	if s.fixtureControl(w, &body) {
		return
	}
	conversationID, ok := uuidParam(w, body.ConversationID, "conversationId")
	if !ok {
		return
	}
	if body.Type == "fixture.stage-expired" {
		if strings.TrimSpace(body.EventKey) == "" || strings.TrimSpace(body.Text) == "" || strings.TrimSpace(body.Suffix) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "eventKey, text, and suffix are required")
			return
		}
		messageID, high, err := s.store.StageExpiredReplacement(r.Context(), p.OwnerID, conversationID, body.Text)
		if err != nil {
			s.storeErr(w, err, "session not found")
			return
		}
		s.stageSuffix(conversationID, stagedSuffix{messageID: messageID, text: body.Suffix})
		writeJSON(w, http.StatusOK, map[string]any{
			"type":         body.Type,
			"sessionId":    conversationID,
			"messageId":    messageID,
			"highWater":    high,
			"source":       store.FixtureSource,
			"suffixStaged": true,
		})
		return
	}
	ev, err := s.store.ApplyFixtureBusiness(r.Context(), store.FixtureInput{
		Source:         body.Source,
		OwnerID:        p.OwnerID,
		ConversationID: conversationID,
		EventKey:       body.EventKey,
		Type:           body.Type,
		MessageID:      body.MessageID,
		Text:           body.Text,
		Status:         body.Status,
	})
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	item, err := ev.MarshalMap()
	if err != nil {
		s.internal(w, err)
		return
	}
	// A subscribed socket learns about the committed row. An unsubscribed
	// socket does not; the client has to read the log again.
	s.hub.Publish(conversationID, item)
	writeJSON(w, http.StatusOK, item)
}

// fixtureControl handles fixture ack and delivery commands. True means the
// response was written. These commands do not append a business row, except
// staging one to be committed on the next read.
func (s *server) fixtureControl(w http.ResponseWriter, body *struct {
	Source         string `json:"source"`
	ConversationID string `json:"conversationId"`
	EventKey       string `json:"eventKey"`
	Type           string `json:"type"`
	MessageID      string `json:"messageId"`
	Text           string `json:"text"`
	Status         string `json:"status"`
	Suffix         string `json:"suffix"`
}) bool {
	if s.hold == nil {
		return false
	}
	switch body.Type {
	case "fixture.hold-subscribe":
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		s.hold.Hold(id)
		log.Printf("fixture hold subscribe session=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "sessionId": id, "held": true})
		return true
	case "fixture.hold-next-subscribe":
		s.hold.ArmNext()
		log.Printf("fixture hold next subscribe")
		writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "held": true})
		return true
	case "fixture.fail-reads":
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		s.failMu.Lock()
		if s.failReads == nil {
			s.failReads = map[string]struct{}{}
		}
		s.failReads[id] = struct{}{}
		s.failMu.Unlock()
		log.Printf("fixture fail reads session=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "sessionId": id, "failing": true})
		return true
	case "fixture.release-reads":
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		s.failMu.Lock()
		delete(s.failReads, id)
		s.failMu.Unlock()
		log.Printf("fixture release reads session=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "sessionId": id, "failing": false})
		return true
	case "fixture.release-subscribe":
		if strings.TrimSpace(body.ConversationID) == "" {
			s.hold.Release("")
			log.Printf("fixture release subscribe all")
			writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "released": true})
			return true
		}
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		s.hold.Release(id)
		log.Printf("fixture release subscribe session=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{"type": body.Type, "sessionId": id, "released": true})
		return true
	case "fixture.engine-delta":
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		text := strings.TrimSpace(body.Text)
		if text == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "text is required")
			return true
		}
		// Same shape as the engine stream delta: no business cursor and no
		// business row. The cursor is not invented here.
		s.hub.Publish(id, events.Delta{
			Type:      "message.delta",
			MessageID: strings.TrimSpace(body.MessageID),
			SessionID: id,
			Text:      text,
		})
		log.Printf("fixture engine delta session=%s cursorless=true", id)
		writeJSON(w, http.StatusOK, map[string]any{
			"type":       "message.delta",
			"sessionId":  id,
			"messageId":  strings.TrimSpace(body.MessageID),
			"text":       text,
			"cursorless": true,
			"source":     store.FixtureSource,
		})
		return true
	case "fixture.stage-during-read":
		id, ok := uuidParam(w, body.ConversationID, "conversationId")
		if !ok {
			return true
		}
		if strings.TrimSpace(body.EventKey) == "" || strings.TrimSpace(body.Text) == "" || strings.TrimSpace(body.MessageID) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "eventKey, messageId, and text are required")
			return true
		}
		s.stageDuringRead(id, parkedDelta{
			key:       strings.TrimSpace(body.EventKey),
			messageID: strings.TrimSpace(body.MessageID),
			text:      body.Text,
		})
		log.Printf("fixture stage during read session=%s", id)
		writeJSON(w, http.StatusOK, map[string]any{
			"type":      body.Type,
			"sessionId": id,
			"staged":    true,
			"source":    store.FixtureSource,
		})
		return true
	default:
		return false
	}
}

func (s *server) readsFailed(sessionID string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	_, ok := s.failReads[sessionID]
	return ok
}

func (s *server) stageDuringRead(conversationID string, parked parkedDelta) {
	s.duringMu.Lock()
	defer s.duringMu.Unlock()
	if s.duringRead == nil {
		s.duringRead = map[string]parkedDelta{}
	}
	s.duringRead[conversationID] = parked
}

func (s *server) takeDuringRead(conversationID string) (parkedDelta, bool) {
	s.duringMu.Lock()
	defer s.duringMu.Unlock()
	parked, ok := s.duringRead[conversationID]
	if ok {
		delete(s.duringRead, conversationID)
	}
	if !ok || parked.text == "" || parked.messageID == "" || parked.key == "" {
		return parkedDelta{}, false
	}
	return parked, true
}

func (s *server) stageSuffix(conversationID string, staged stagedSuffix) {
	s.suffixMu.Lock()
	defer s.suffixMu.Unlock()
	if s.pendingSuffix == nil {
		s.pendingSuffix = map[string]stagedSuffix{}
	}
	s.pendingSuffix[conversationID] = staged
}

func (s *server) takeStagedSuffix(conversationID string) (stagedSuffix, bool) {
	s.suffixMu.Lock()
	defer s.suffixMu.Unlock()
	staged, ok := s.pendingSuffix[conversationID]
	if ok {
		delete(s.pendingSuffix, conversationID)
	}
	if !ok || staged.text == "" || staged.messageID == "" {
		return stagedSuffix{}, false
	}
	return staged, true
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return uuidParam(w, r.PathValue("id"), "id")
}

func uuidParam(w http.ResponseWriter, value, field string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" is required")
		return "", false
	}
	id, err := uuid.Parse(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", field+" must be a UUID")
		return "", false
	}
	return id.String(), true
}

func required(w http.ResponseWriter, value, field string, max int) (string, bool) {
	value, ok := bounded(w, value, field, max)
	if !ok {
		return "", false
	}
	if value == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" is required")
		return "", false
	}
	return value, true
}

func bounded(w http.ResponseWriter, value, field string, max int) (string, bool) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > max {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" is too long")
		return "", false
	}
	return value, true
}

type submitBody struct {
	Content string          `json:"content"`
	Options json.RawMessage `json:"options"`
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) >= 2 && key[0] == '"' && key[len(key)-1] == '"' {
		key = strings.TrimSpace(key[1 : len(key)-1])
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key is required")
		return "", false
	}
	if len(key) > 128 || strings.ContainsAny(key, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key is invalid")
		return "", false
	}
	return key, true
}

func decodeSubmit(w http.ResponseWriter, r *http.Request) (submitBody, bool) {
	var body submitBody
	if !decodeStrict(w, r, &body) {
		return submitBody{}, false
	}
	return body, true
}

func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body has an unknown field or is not JSON")
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
