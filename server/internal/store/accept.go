package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yosuang/philo/server/internal/idempotency"
)

// Scope is the public accept scope. The actor is not part of it.
// TargetRef is empty for submit. The client key is not the turn id.
type Scope struct {
	OwnerID        string
	ConversationID string
	Kind           string
	TargetRef      string
	Key            string
}

func (s Scope) lockKey() string {
	return strings.Join([]string{s.OwnerID, s.ConversationID, s.Kind, s.TargetRef, s.Key}, "\x1f")
}

// AcceptResult is the stored submit. Replayed is true when no new side effect ran.
type AcceptResult struct {
	TurnID    string
	Replayed  bool
	User      Message
	Assistant *Message
}

type sideEffect func(ctx context.Context, tx pgx.Tx) (AcceptResult, error)

// Accept binds a scope to a semantic hash. The same hash replays the stored
// result and runs no effect. A different hash conflicts and runs no effect.
// Concurrent calls serialize on the scope. Either payload may win.
func (s *Store) Accept(ctx context.Context, scope Scope, actorID, hash string, effect sideEffect) (AcceptResult, error) {
	if scope.OwnerID == "" || scope.ConversationID == "" || scope.Kind == "" || scope.Key == "" || actorID == "" || hash == "" {
		return AcceptResult{}, errors.New("incomplete accept")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AcceptResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, scope.lockKey()); err != nil {
		return AcceptResult{}, err
	}
	var storedHash string
	var version int
	var turnID *string
	err = tx.QueryRow(ctx, `
		SELECT request_hash, request_hash_version, turn_id
		FROM idempotency_accepts
		WHERE owner_id = $1 AND conversation_id = $2 AND operation_kind = $3
			AND target_ref = $4 AND idempotency_key = $5
		FOR UPDATE`,
		scope.OwnerID, scope.ConversationID, scope.Kind, scope.TargetRef, scope.Key,
	).Scan(&storedHash, &version, &turnID)
	if err == nil {
		if storedHash != hash || version != idempotency.HashVersion {
			return AcceptResult{}, ErrIdempotencyConflict
		}
		res, err := loadAccepted(ctx, tx, turnID)
		if err != nil {
			return AcceptResult{}, err
		}
		res.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return AcceptResult{}, err
		}
		return res, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AcceptResult{}, err
	}
	res, err := effect(ctx, tx)
	if err != nil {
		return AcceptResult{}, err
	}
	var storedTurn any
	if res.TurnID != "" {
		storedTurn = res.TurnID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_accepts (
			owner_id, conversation_id, operation_kind, target_ref, idempotency_key,
			request_hash, request_hash_version, turn_id, actor_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		scope.OwnerID, scope.ConversationID, scope.Kind, scope.TargetRef, scope.Key,
		hash, idempotency.HashVersion, storedTurn, actorID, now(),
	); err != nil {
		return AcceptResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AcceptResult{}, err
	}
	return res, nil
}

func loadAccepted(ctx context.Context, tx pgx.Tx, turnID *string) (AcceptResult, error) {
	if turnID == nil || *turnID == "" {
		return AcceptResult{}, nil
	}
	res := AcceptResult{TurnID: *turnID}
	user, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+messageCols+`
		FROM messages
		WHERE turn_id = $1 AND role = 'user'
		ORDER BY seq ASC
		LIMIT 1`, *turnID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return AcceptResult{}, err
	}
	if err == nil {
		res.User = user
	}
	assistant, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+messageCols+`
		FROM messages
		WHERE turn_id = $1 AND role = 'assistant'
		ORDER BY seq ASC
		LIMIT 1`, *turnID))
	if errors.Is(err, ErrNotFound) {
		return res, nil
	}
	if err != nil {
		return AcceptResult{}, err
	}
	res.Assistant = &assistant
	return res, nil
}

// SubmitInput is one user submit. Fixture selects the isolated dev reply path.
// It does not make that path the production default.
type SubmitInput struct {
	OwnerID        string
	ActorID        string
	ConversationID string
	Key            string
	Content        string
	Hash           string
	Fixture        bool
}

// AcceptSubmit stores one turn for a submit scope. A replay returns that turn
// and does not add a message, wake intent, or outbox row.
func (s *Store) AcceptSubmit(ctx context.Context, in SubmitInput) (AcceptResult, error) {
	scope := Scope{
		OwnerID:        in.OwnerID,
		ConversationID: in.ConversationID,
		Kind:           "submit",
		TargetRef:      "",
		Key:            in.Key,
	}
	return s.Accept(ctx, scope, in.ActorID, in.Hash, func(ctx context.Context, tx pgx.Tx) (AcceptResult, error) {
		return submitEffect(ctx, tx, in)
	})
}

func submitEffect(ctx context.Context, tx pgx.Tx, in SubmitInput) (AcceptResult, error) {
	sess, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM sessions WHERE id = $1 FOR UPDATE`, in.ConversationID))
	if err != nil {
		return AcceptResult{}, err
	}
	if in.Fixture {
		var streaming bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM messages WHERE session_id = $1 AND status = 'streaming')`,
			in.ConversationID).Scan(&streaming); err != nil {
			return AcceptResult{}, err
		}
		if streaming {
			return AcceptResult{}, ErrBusy
		}
	}

	ts := now()
	turnID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO turns (id, owner_id, conversation_id, created_at)
		VALUES ($1, $2, $3, $4)`,
		turnID, in.OwnerID, in.ConversationID, ts); err != nil {
		return AcceptResult{}, err
	}
	user, err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO messages (
			id, session_id, turn_id, role, content, status, created_at, updated_at
		) VALUES ($1, $2, $3, 'user', $4, 'complete', $5, $5)
		RETURNING `+messageCols,
		uuid.NewString(), in.ConversationID, turnID, in.Content, ts,
	))
	if err != nil {
		return AcceptResult{}, err
	}
	if _, err := appendBusinessEvent(ctx, tx, in.ConversationID, "message.created", user.ID, map[string]any{
		"message": user,
	}); err != nil {
		return AcceptResult{}, err
	}
	title := sess.Title
	if title == "" {
		title = clipTitle(in.Content)
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET title = $2, updated_at = $3 WHERE id = $1`, sess.ID, title, ts); err != nil {
		return AcceptResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wake_intents (id, owner_id, conversation_id, turn_id, kind, created_at)
		VALUES ($1, $2, $3, $4, 'submit', $5)`,
		uuid.NewString(), in.OwnerID, in.ConversationID, turnID, ts); err != nil {
		return AcceptResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"kind":      "submit",
		"turnId":    turnID,
		"execution": "unavailable",
	})
	if err != nil {
		return AcceptResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, owner_id, conversation_id, turn_id, kind, payload, created_at)
		VALUES ($1, $2, $3, $4, 'submit', $5, $6)`,
		uuid.NewString(), in.OwnerID, in.ConversationID, turnID, payload, ts); err != nil {
		return AcceptResult{}, err
	}

	res := AcceptResult{TurnID: turnID, User: user}
	if !in.Fixture {
		return res, nil
	}
	assistant, err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO messages (
			id, session_id, turn_id, role, content, status, adapter, reply_to_id, created_at, updated_at
		) VALUES ($1, $2, $3, 'assistant', '', 'streaming', 'dev', $4, $5, $5)
		RETURNING `+messageCols,
		uuid.NewString(), in.ConversationID, turnID, user.ID, ts.Add(1),
	))
	if err != nil {
		return AcceptResult{}, err
	}
	res.Assistant = &assistant
	return res, nil
}

// OpenDefaultConversation returns the bot's one default conversation.
// Concurrent callers lock the bot row and then the unique index.
func (s *Store) OpenDefaultConversation(ctx context.Context, ownerID, agentID string) (Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	agent, err := scanAgent(tx.QueryRow(ctx, `
		SELECT `+agentCols+` FROM agents WHERE id = $1 FOR UPDATE`, agentID))
	if err != nil {
		return Session{}, err
	}
	if agent.OwnerID != ownerID {
		return Session{}, ErrForbidden
	}
	sess, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM sessions WHERE agent_id = $1 AND is_default`, agentID))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Session{}, err
		}
		return sess, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Session{}, err
	}
	title := agent.Name
	if title == "" {
		title = "Chat"
	}
	ts := now()
	sess, err = scanSession(tx.QueryRow(ctx, `
		INSERT INTO sessions (id, agent_id, title, is_default, created_at, updated_at)
		VALUES ($1, $2, $3, TRUE, $4, $4)
		RETURNING `+sessionCols,
		uuid.NewString(), agentID, title, ts,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			sess, err = scanSession(tx.QueryRow(ctx, `
				SELECT `+sessionCols+` FROM sessions WHERE agent_id = $1 AND is_default`, agentID))
			if err != nil {
				return Session{}, err
			}
		} else {
			return Session{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// DefaultConversation returns the default conversation without creating one.
func (s *Store) DefaultConversation(ctx context.Context, ownerID, agentID string) (Session, error) {
	if _, err := s.RequireAgent(ctx, ownerID, agentID); err != nil {
		return Session{}, err
	}
	return scanSession(s.pool.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM sessions WHERE agent_id = $1 AND is_default`, agentID))
}
