// Package httpapi is the phase-1 HTTP and WebSocket surface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yosuang/philo/server/internal/adapter"
	"github.com/yosuang/philo/server/internal/adapter/dev"
	"github.com/yosuang/philo/server/internal/adapter/provider"
	"github.com/yosuang/philo/server/internal/config"
	"github.com/yosuang/philo/server/internal/engine"
	"github.com/yosuang/philo/server/internal/hub"
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

// App is the session server handler.
type App struct {
	Handler http.Handler
	engine  *engine.Engine
}

// Stop cancels in-flight generations.
func (a *App) Stop() {
	if a != nil && a.engine != nil {
		a.engine.Stop()
	}
}

type server struct {
	store *store.Store
	eng   *engine.Engine
	hub   *hub.Hub
	cfg   config.Config
	http  *http.Client
}

// New builds the HTTP handler. The pool must already be migrated.
// An unknown PHILO_MODEL_ADAPTER is refused so it cannot fall through to dev.
func New(pool *pgxpool.Pool, cfg config.Config) (*App, error) {
	ad, err := newAdapter(cfg)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	st := store.New(pool)
	h := hub.New()
	eng := engine.New(st, ad, h)
	s := &server{
		store: st,
		eng:   eng,
		hub:   h,
		cfg:   cfg,
		http:  &http.Client{Timeout: 2 * time.Second},
	}
	mux := http.NewServeMux()
	s.routes(mux)
	return &App{Handler: mux, engine: eng}, nil
}

func newAdapter(cfg config.Config) (adapter.Adapter, error) {
	switch cfg.Adapter {
	case "dev":
		return dev.New(cfg.DevChunkDelay), nil
	case "provider":
		return provider.New(cfg.ProviderAPIKey, cfg.ProviderBaseURL), nil
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
	mux.HandleFunc("POST /v1/approvals/{id}/decision", s.decide)
	mux.HandleFunc("GET /v1/tasks", s.listTasks)
	mux.HandleFunc("GET /v1/connectors", s.connectors)
	mux.HandleFunc("GET /v1/events", s.events)
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
	s.hub.Handle(w, r, s.store.SessionExists)
}

func (s *server) createAgent(w http.ResponseWriter, r *http.Request) {
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
	agent, err := s.store.CreateAgent(r.Context(), name, instructions, modelID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *server) getAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) patchAgent(w http.ResponseWriter, r *http.Request) {
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
	agent, err := s.store.UpdateAgent(r.Context(), id, body.Name, body.Instructions, body.ModelID)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
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
	sess, err := s.store.CreateSession(r.Context(), agentID, title)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) {
	agentID, ok := uuidParam(w, r.URL.Query().Get("agentId"), "agentId")
	if !ok {
		return
	}
	sessions, err := s.store.ListSessions(r.Context(), agentID)
	if err != nil {
		s.storeErr(w, err, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *server) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sess, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	content, ok := required(w, body.Content, "content", maxContent)
	if !ok {
		return
	}
	turn, err := s.eng.PostMessage(r.Context(), id, content)
	if errors.Is(err, store.ErrBusy) {
		writeError(w, http.StatusConflict, "reply_in_progress", "this session already has a streaming reply")
		return
	}
	if err != nil {
		s.storeErr(w, err, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userMessage":      turn.User,
		"assistantMessage": turn.Assistant,
	})
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	msg, err := s.eng.Retry(r.Context(), id)
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

func (s *server) listApprovals(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := uuidParam(w, r.URL.Query().Get("sessionId"), "sessionId")
	if !ok {
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if !decode(w, r, &body) {
		return
	}
	decision := strings.TrimSpace(body.Decision)
	if decision != "approve" && decision != "reject" {
		writeError(w, http.StatusBadRequest, "invalid_request", "decision must be approve or reject")
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
	sessionID, ok := uuidParam(w, r.URL.Query().Get("sessionId"), "sessionId")
	if !ok {
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
	s.internal(w, err)
}

func (s *server) internal(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
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
