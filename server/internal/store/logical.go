package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OpenPersonalSession returns the one personal logical session for a
// conversation and harness pin. A second call returns the same id.
// Attempt is not an input, so a new attempt does not create a session.
func (s *Store) OpenPersonalSession(ctx context.Context, ownerID, conversationID, harnessPin string) (string, error) {
	if harnessPin == "" {
		return "", errors.New("harness pin is required")
	}
	if _, err := s.RequireSession(ctx, ownerID, conversationID); err != nil {
		return "", err
	}
	return openLogical(ctx, s, ownerID, "personal", conversationID, "", harnessPin)
}

// FixtureWorkerPin labels the dev worker fixture's logical session.
// It is not a Makez harness pin and not a model.
const FixtureWorkerPin = "fixture-worker"

// OpenWorkerSession returns the one worker logical session for a worker run
// and harness pin. It is not the personal session.
func (s *Store) OpenWorkerSession(ctx context.Context, ownerID, workerRunRef, harnessPin string) (string, error) {
	if harnessPin == "" || workerRunRef == "" {
		return "", errors.New("worker run and harness pin are required")
	}
	return openLogical(ctx, s, ownerID, "worker", "", workerRunRef, harnessPin)
}

// OpenFixtureWorkerSession opens the worker session for one running
// philo-server task. The caller has already started the fixture process.
// It does not complete the task and does not change the approval.
func (s *Store) OpenFixtureWorkerSession(ctx context.Context, taskID string) (string, error) {
	if taskID == "" {
		return "", errors.New("task is required")
	}
	var owner string
	err := s.pool.QueryRow(ctx, `
		SELECT a.owner_id
		FROM tasks t
		JOIN sessions sess ON sess.id = t.session_id
		JOIN agents a ON a.id = sess.agent_id
		WHERE t.id = $1 AND t.source = 'philo-server' AND t.status = 'running'`, taskID).Scan(&owner)
	if err != nil {
		return "", mapErr(err)
	}
	if owner == "" {
		return "", errors.New("fixture worker has no owner")
	}
	return s.OpenWorkerSession(ctx, owner, taskID, FixtureWorkerPin)
}

func openLogical(ctx context.Context, s *Store, ownerID, scope, conversationID, workerRunRef, harnessPin string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.NewString()
	var conversation any
	var worker any
	if conversationID != "" {
		conversation = conversationID
	}
	if workerRunRef != "" {
		worker = workerRunRef
	}
	var stored string
	err = tx.QueryRow(ctx, `
		INSERT INTO logical_native_sessions (
			id, owner_id, scope, conversation_id, worker_run_ref, harness_pin, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		id, ownerID, scope, conversation, worker, harnessPin, now(),
	).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		if scope == "personal" {
			err = tx.QueryRow(ctx, `
				SELECT id FROM logical_native_sessions
				WHERE owner_id = $1 AND scope = 'personal' AND conversation_id = $2 AND harness_pin = $3`,
				ownerID, conversationID, harnessPin).Scan(&stored)
		} else {
			err = tx.QueryRow(ctx, `
				SELECT id FROM logical_native_sessions
				WHERE owner_id = $1 AND scope = 'worker' AND worker_run_ref = $2 AND harness_pin = $3`,
				ownerID, workerRunRef, harnessPin).Scan(&stored)
		}
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return stored, nil
}
