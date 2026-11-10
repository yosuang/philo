// Package store is the Postgres source of truth for agents, sessions,
// messages, approvals, and tasks.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound means the row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrBusy means the session already has a streaming reply.
	ErrBusy = errors.New("reply in progress")
	// ErrNotRetryable means the message cannot be regenerated.
	ErrNotRetryable = errors.New("not retryable")
	// ErrNotPending means the approval was already decided.
	ErrNotPending = errors.New("not pending")
	// ErrForbidden means the authenticated owner does not own the record.
	ErrForbidden = errors.New("forbidden")
	// ErrIdempotencyConflict means the scope is already bound to a different payload.
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	// ErrCursorExpired means the business cursor is older than the retained log.
	ErrCursorExpired = errors.New("business cursor expired")
)

// APIError is the persisted generation error.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Metadata identifies which adapter produced an assistant message.
type Metadata struct {
	Adapter string `json:"adapter"`
}

// Agent is a configured assistant. OwnerID is the server tenant, not a
// client-supplied credential and not a Makez workspace id.
type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Instructions string    `json:"instructions"`
	ModelID      string    `json:"modelId"`
	OwnerID      string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Session is one conversation with an agent.
type Session struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agentId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Message is one transcript row. Seq orders messages in one conversation.
// A status change does not allocate another seq.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Seq       int64     `json:"seq"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Error     *APIError `json:"error,omitempty"`
	Metadata  *Metadata `json:"metadata,omitempty"`
	ReplyToID string    `json:"-"`
	TurnID    string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Approval is a draft action waiting for an explicit decision.
type Approval struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	MessageID string    `json:"messageId"`
	Kind      string    `json:"kind"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Task is in-server work created only after an approval.
type Task struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	ApprovalID string    `json:"approvalId"`
	Status     string    `json:"status"`
	Source     string    `json:"source"`
	Result     string    `json:"result"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Turn is the user message and the streaming assistant placeholder.
type Turn struct {
	User      Message
	Assistant Message
	Session   Session
}

// Decision is an approval transition and, on approve, the running task.
type Decision struct {
	Approval Approval
	Task     *Task
}

// Store talks to Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// Connect opens a pool. The caller migrates before serving.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// New wraps a pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

type scannable interface {
	Scan(dest ...any) error
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
