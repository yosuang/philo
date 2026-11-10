package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yosuang/philo/server/internal/idempotency"
)

const approvalCols = `id, session_id, message_id, kind, summary, status, requester_actor_id, action_snapshot::text, created_at, updated_at`
const taskCols = `id, session_id, approval_id, status, source, result, created_at, updated_at`

func scanApproval(row scannable) (Approval, error) {
	var a Approval
	var requester *string
	var snapshot *string
	if err := row.Scan(
		&a.ID, &a.SessionID, &a.MessageID, &a.Kind, &a.Summary, &a.Status,
		&requester, &snapshot, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return Approval{}, mapErr(err)
	}
	if requester != nil {
		a.RequesterActorID = *requester
	}
	if snapshot != nil {
		a.ActionSnapshot = json.RawMessage(*snapshot)
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	return a, nil
}

func scanTask(row scannable) (Task, error) {
	var task Task
	if err := row.Scan(&task.ID, &task.SessionID, &task.ApprovalID, &task.Status, &task.Source, &task.Result, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return Task{}, mapErr(err)
	}
	task.CreatedAt = task.CreatedAt.UTC()
	task.UpdatedAt = task.UpdatedAt.UTC()
	return task, nil
}

func actionSnapshot(kind, summary string) ([]byte, error) {
	return idempotency.Canonical(map[string]any{
		"kind":    kind,
		"summary": summary,
	})
}

func snapshotsEqual(a, b json.RawMessage) bool {
	if len(bytes.TrimSpace(a)) == 0 && len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	if len(bytes.TrimSpace(a)) == 0 || len(bytes.TrimSpace(b)) == 0 {
		return false
	}
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	lb, err := idempotency.Canonical(left)
	if err != nil {
		return false
	}
	rb, err := idempotency.Canonical(right)
	if err != nil {
		return false
	}
	return bytes.Equal(lb, rb)
}

func requesterForMessage(ctx context.Context, q queryer, msg Message) (string, error) {
	if msg.TurnID == "" {
		return "", nil
	}
	var actor string
	err := q.QueryRow(ctx, `
		SELECT actor_id FROM idempotency_accepts WHERE turn_id = $1 LIMIT 1`, msg.TurnID,
	).Scan(&actor)
	if errors.Is(mapErr(err), ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return actor, nil
}

func insertApproval(ctx context.Context, q queryer, sessionID, messageID, kind, summary, requester string, snapshot []byte, ts time.Time) (Approval, error) {
	if requester == "" || len(snapshot) == 0 {
		return Approval{}, errors.New("approval requires a requester and a snapshot")
	}
	return scanApproval(q.QueryRow(ctx, `
		INSERT INTO approvals (
			id, session_id, message_id, kind, summary, status,
			requester_actor_id, action_snapshot, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8, $8)
		RETURNING `+approvalCols,
		uuid.NewString(), sessionID, messageID, kind, summary, requester, snapshot, ts,
	))
}

// ListApprovals returns every approval for a session, oldest first.
func (s *Store) ListApprovals(ctx context.Context, sessionID string) ([]Approval, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+approvalCols+`
		FROM approvals
		WHERE session_id = $1
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetApproval loads one approval.
func (s *Store) GetApproval(ctx context.Context, id string) (Approval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = $1`, id))
}

// ReplaceAction binds a new card when the action snapshot changes.
// actorID is the bearer actor. The previous card becomes invalid and does
// not authorize the new snapshot. The same snapshot writes nothing.
// An invalid card cannot be edited.
func (s *Store) ReplaceAction(ctx context.Context, id, actorID, kind, summary string) (Approval, Approval, bool, error) {
	kind = strings.TrimSpace(kind)
	summary = strings.TrimSpace(summary)
	if id == "" || actorID == "" || kind == "" || summary == "" {
		return Approval{}, Approval{}, false, errors.New("approval edit requires an actor, kind, and summary")
	}
	nextSnap, err := actionSnapshot(kind, summary)
	if err != nil {
		return Approval{}, Approval{}, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Approval{}, Approval{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanApproval(tx.QueryRow(ctx, `
		SELECT `+approvalCols+` FROM approvals WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return Approval{}, Approval{}, false, err
	}
	switch current.Status {
	case "pending", "approved", "rejected":
	default:
		return Approval{}, Approval{}, false, ErrNotPending
	}
	if snapshotsEqual(current.ActionSnapshot, nextSnap) {
		if err := tx.Commit(ctx); err != nil {
			return Approval{}, Approval{}, false, err
		}
		return current, current, false, nil
	}

	ts := now()
	previous, err := scanApproval(tx.QueryRow(ctx, `
		UPDATE approvals SET status = 'invalid', updated_at = $2 WHERE id = $1
		RETURNING `+approvalCols,
		id, ts,
	))
	if err != nil {
		return Approval{}, Approval{}, false, err
	}
	next, err := insertApproval(ctx, tx, previous.SessionID, previous.MessageID, kind, summary, actorID, nextSnap, ts)
	if err != nil {
		return Approval{}, Approval{}, false, err
	}
	if _, err := appendBusinessEvent(ctx, tx, previous.SessionID, "approval.updated", previous.MessageID, map[string]any{
		"approval": previous,
	}); err != nil {
		return Approval{}, Approval{}, false, err
	}
	if _, err := appendBusinessEvent(ctx, tx, next.SessionID, "approval.requested", next.MessageID, map[string]any{
		"approval": next,
	}); err != nil {
		return Approval{}, Approval{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Approval{}, Approval{}, false, err
	}
	return previous, next, true, nil
}

// Decide approves or rejects a pending approval.
// Approve also inserts a running philo-server task in the same transaction.
// Reject does not create a task. Neither writes a wake or an outbox row.
func (s *Store) Decide(ctx context.Context, id, decision string) (Decision, error) {
	status := ""
	switch decision {
	case "approve":
		status = "approved"
	case "reject":
		status = "rejected"
	default:
		return Decision{}, errors.New("invalid decision")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanApproval(tx.QueryRow(ctx, `
		SELECT `+approvalCols+` FROM approvals WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return Decision{}, err
	}
	if current.Status != "pending" {
		return Decision{}, ErrNotPending
	}
	if current.RequesterActorID == "" || len(bytes.TrimSpace(current.ActionSnapshot)) == 0 {
		return Decision{}, ErrForbidden
	}
	ts := now()
	approval, err := scanApproval(tx.QueryRow(ctx, `
		UPDATE approvals SET status = $2, updated_at = $3 WHERE id = $1
		RETURNING `+approvalCols,
		id, status, ts,
	))
	if err != nil {
		return Decision{}, err
	}

	var task *Task
	if decision == "approve" {
		row, err := scanTask(tx.QueryRow(ctx, `
			INSERT INTO tasks (id, session_id, approval_id, status, source, result, created_at, updated_at)
			VALUES ($1, $2, $3, 'running', 'philo-server', '', $4, $4)
			RETURNING `+taskCols,
			uuid.NewString(), approval.SessionID, approval.ID, ts,
		))
		if err != nil {
			return Decision{}, err
		}
		task = &row
	}
	if _, err := appendBusinessEvent(ctx, tx, approval.SessionID, "approval.updated", approval.MessageID, map[string]any{
		"approval": approval,
	}); err != nil {
		return Decision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Decision{}, err
	}
	return Decision{Approval: approval, Task: task}, nil
}

// ListTasks returns tasks for a session, oldest first.
func (s *Store) ListTasks(ctx context.Context, sessionID string) ([]Task, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+taskCols+`
		FROM tasks
		WHERE session_id = $1
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// RecordWorkerResult stores text on a running task. The claim's snapshot,
// decision, and owner are ignored. The approval's snapshot, requester, and
// status stay as they were, and no other approval or task is inserted.
func (s *Store) RecordWorkerResult(ctx context.Context, taskID string, claim WorkerClaim) (Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskCols+` FROM tasks WHERE id = $1 FOR UPDATE`, taskID))
	if err != nil {
		return Task{}, err
	}
	before, err := scanApproval(tx.QueryRow(ctx, `
		SELECT `+approvalCols+` FROM approvals WHERE id = $1 FOR UPDATE`, task.ApprovalID))
	if err != nil {
		return Task{}, err
	}
	if task.Status == "running" {
		task, err = scanTask(tx.QueryRow(ctx, `
			UPDATE tasks
			SET status = 'completed', result = $2, updated_at = $3
			WHERE id = $1 AND status = 'running'
			RETURNING `+taskCols,
			taskID, claim.Text, now(),
		))
		if err != nil {
			return Task{}, err
		}
	}
	after, err := scanApproval(tx.QueryRow(ctx, `
		SELECT `+approvalCols+` FROM approvals WHERE id = $1`, before.ID))
	if err != nil {
		return Task{}, err
	}
	if after.Status != before.Status || after.RequesterActorID != before.RequesterActorID || !snapshotsEqual(after.ActionSnapshot, before.ActionSnapshot) {
		return Task{}, errors.New("worker result changed the approval")
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return task, nil
}

// CompleteTask stores the result of a running in-server task.
// The result does not change the approval.
func (s *Store) CompleteTask(ctx context.Context, id, result string) (Task, error) {
	return s.RecordWorkerResult(ctx, id, WorkerClaim{Text: result})
}
