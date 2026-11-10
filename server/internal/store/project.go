package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// FaultPoint is a place the captured-failure projector can fail on purpose.
// Points before commit roll the transaction back.
// publish and source_ack run after commit and can be retried without a second message.
type FaultPoint string

const (
	FaultNone       FaultPoint = ""
	FaultProjection FaultPoint = "projection"
	FaultDedup      FaultPoint = "dedup"
	FaultCheckpoint FaultPoint = "checkpoint"
	FaultOutbox     FaultPoint = "outbox"
	FaultCommit     FaultPoint = "commit"
	FaultPublish    FaultPoint = "publish"
	FaultSourceAck  FaultPoint = "source_ack"
)

// InjectedFault is a test fault. It is not a Makez error.
type InjectedFault struct {
	Point FaultPoint
}

func (e InjectedFault) Error() string {
	return "injected fault at " + string(e.Point)
}

var (
	// ErrUnlabeledBinding means the caller did not mark the association as a test binding.
	ErrUnlabeledBinding = errors.New("test binding label required")
	// ErrBindingMismatch means the task conversation id is not the bound captured conversation.
	ErrBindingMismatch = errors.New("captured conversation does not match the test binding")
)

// ProjectResult is the Philo effect of one captured-failure replay.
// LocalReceiptAcked is this process's receipt. Makez was not asked to ack.
type ProjectResult struct {
	Replayed          bool
	StatusHintOnly    bool
	MessageID         string
	BusinessCursor    int64
	AppliedPublicSeq  int64
	ProcessingCursor  string
	Published         bool
	LocalReceiptAcked bool
}

type observationRow struct {
	MessageID      string
	EventID        string
	OutboxID       string
	PublishedAt    *time.Time
	AckedAt        *time.Time
	BusinessCursor int64
	AppliedSeq     int64
	Processing     string
}

// ProjectCapturedFailure replays captured Makez bytes onto one Philo turn.
// It does not call Makez, does not start a worker, and does not add a public route.
// A status frame with no SSE id does not project a terminal row.
// The authoritative GET is the only terminal input.
func (s *Store) ProjectCapturedFailure(ctx context.Context, in CaptureInput, fault FaultPoint) (ProjectResult, error) {
	if err := validateFault(fault); err != nil {
		return ProjectResult{}, err
	}
	if in.Binding.Label != TestBindingLabel {
		return ProjectResult{}, ErrUnlabeledBinding
	}
	if in.Binding.OwnerID == "" || in.Binding.ConversationID == "" || in.Binding.TurnID == "" || in.Binding.CapturedConversationID == "" {
		return ProjectResult{}, fmt.Errorf("incomplete test binding")
	}
	parsed, err := parseCapture(in)
	if err != nil {
		return ProjectResult{}, err
	}
	if parsed.task != nil && parsed.task.ConversationID != in.Binding.CapturedConversationID {
		return ProjectResult{}, ErrBindingMismatch
	}
	if len(parsed.logs) == 0 && parsed.task == nil {
		return ProjectResult{StatusHintOnly: true}, nil
	}
	if fault != FaultNone && !parsed.failure() {
		return ProjectResult{}, fmt.Errorf("fault injection applies only to the FAILED/AGENT_FAILED projection")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProjectResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := lockConversation(ctx, tx, in.Binding); err != nil {
		return ProjectResult{}, err
	}
	if _, err := lockTurn(ctx, tx, in.Binding); err != nil {
		return ProjectResult{}, err
	}

	if parsed.task != nil {
		existing, found, err := loadObservation(ctx, tx, *parsed.task)
		if err != nil {
			return ProjectResult{}, err
		}
		if found {
			if err := tx.Commit(ctx); err != nil {
				return ProjectResult{}, err
			}
			return s.finishPublish(ctx, existing, fault, true)
		}
	}

	if !parsed.failure() {
		pending, err := logsBeforeTerminal(ctx, tx, parsed.logs)
		if err != nil {
			return ProjectResult{}, err
		}
		if err := writeLogs(ctx, tx, pending); err != nil {
			return ProjectResult{}, err
		}
		if parsed.task != nil && len(pending) > 0 {
			if err := upsertCheckpoint(ctx, tx, *parsed.task, maxSeq(pending)); err != nil {
				return ProjectResult{}, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return ProjectResult{}, err
		}
		return ProjectResult{Replayed: len(parsed.logs) > 0 && len(pending) == 0}, nil
	}

	if err := atFault(fault, FaultProjection); err != nil {
		return ProjectResult{}, err
	}
	text, err := failureText(ctx, tx, parsed)
	if err != nil {
		return ProjectResult{}, err
	}
	messageID, err := insertAssistantError(ctx, tx, in.Binding, text)
	if err != nil {
		return ProjectResult{}, err
	}
	if err := linkTurn(ctx, tx, in.Binding, *parsed.task); err != nil {
		return ProjectResult{}, err
	}
	if err := atFault(fault, FaultDedup); err != nil {
		return ProjectResult{}, err
	}
	if err := writeLogs(ctx, tx, parsed.logs); err != nil {
		return ProjectResult{}, err
	}
	if err := insertObservation(ctx, tx, *parsed.task, messageID); err != nil {
		return ProjectResult{}, err
	}
	if err := insertReceipt(ctx, tx, *parsed.task); err != nil {
		return ProjectResult{}, err
	}
	if err := atFault(fault, FaultCheckpoint); err != nil {
		return ProjectResult{}, err
	}
	applied, err := appliedSeq(ctx, tx, parsed)
	if err != nil {
		return ProjectResult{}, err
	}
	if err := upsertCheckpoint(ctx, tx, *parsed.task, applied); err != nil {
		return ProjectResult{}, err
	}
	if err := atFault(fault, FaultOutbox); err != nil {
		return ProjectResult{}, err
	}
	event, outboxID, err := insertFailureOutbox(ctx, tx, in.Binding, messageID, text)
	if err != nil {
		return ProjectResult{}, err
	}
	if err := attachObservation(ctx, tx, *parsed.task, event.ID, outboxID); err != nil {
		return ProjectResult{}, err
	}
	if err := atFault(fault, FaultCommit); err != nil {
		return ProjectResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if isUnique(err) {
			existing, found, loadErr := loadObservation(ctx, s.pool, *parsed.task)
			if loadErr != nil || !found {
				return ProjectResult{}, err
			}
			return s.finishPublish(ctx, existing, fault, true)
		}
		return ProjectResult{}, err
	}

	row := observationRow{
		MessageID:      messageID,
		EventID:        event.ID,
		OutboxID:       outboxID,
		BusinessCursor: event.Cursor,
		AppliedSeq:     applied,
		Processing:     parsed.task.Cursor,
	}
	result, err := s.finishPublish(ctx, row, fault, false)
	result.BusinessCursor = event.Cursor
	result.AppliedPublicSeq = applied
	result.ProcessingCursor = parsed.task.Cursor
	return result, err
}

func validateFault(fault FaultPoint) error {
	switch fault {
	case FaultNone, FaultProjection, FaultDedup, FaultCheckpoint, FaultOutbox, FaultCommit, FaultPublish, FaultSourceAck:
		return nil
	default:
		return fmt.Errorf("unknown fault point %q", fault)
	}
}

func atFault(got, point FaultPoint) error {
	if got == point {
		return InjectedFault{Point: point}
	}
	return nil
}

func failureText(ctx context.Context, tx pgx.Tx, parsed parsedCapture) (string, error) {
	if parsed.failureText != "" {
		return parsed.failureText, nil
	}
	var stored string
	err := tx.QueryRow(ctx, `
		SELECT failure_text FROM source_log_dedup
		WHERE run_id = $1 AND failure_text <> ''
		ORDER BY public_seq
		LIMIT 1`, parsed.task.RunID).Scan(&stored)
	if err == nil && stored != "" {
		return stored, nil
	}
	if err != nil && !errors.Is(mapErr(err), ErrNotFound) {
		return "", err
	}
	return "AGENT_FAILED", nil
}

func appliedSeq(ctx context.Context, tx pgx.Tx, parsed parsedCapture) (int64, error) {
	applied := maxSeq(parsed.logs)
	if applied > 0 || parsed.task == nil {
		return applied, nil
	}
	var stored int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(public_seq), 0) FROM source_log_dedup WHERE run_id = $1`,
		parsed.task.RunID).Scan(&stored); err != nil {
		return 0, err
	}
	return stored, nil
}

func logsBeforeTerminal(ctx context.Context, tx pgx.Tx, logs []publicLog) ([]publicLog, error) {
	terminal := map[string]bool{}
	pending := make([]publicLog, 0, len(logs))
	for _, log := range logs {
		known, ok := terminal[log.RunID]
		if !ok {
			exists, err := runHasTerminal(ctx, tx, log.RunID)
			if err != nil {
				return nil, err
			}
			terminal[log.RunID] = exists
			known = exists
		}
		if known {
			continue
		}
		pending = append(pending, log)
	}
	return pending, nil
}

func runHasTerminal(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM terminal_observations
			WHERE run_id = $1 AND state = 'FAILED' AND reason = 'AGENT_FAILED'
		)`, runID).Scan(&exists)
	return exists, err
}

func lockConversation(ctx context.Context, tx pgx.Tx, binding TestBinding) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id = $1 FOR UPDATE`, binding.ConversationID).Scan(&id)
	return id, mapErr(err)
}

func lockTurn(ctx context.Context, tx pgx.Tx, binding TestBinding) (string, error) {
	var owner, conversation string
	err := tx.QueryRow(ctx, `
		SELECT owner_id, conversation_id FROM turns WHERE id = $1 FOR UPDATE`,
		binding.TurnID).Scan(&owner, &conversation)
	if err != nil {
		return "", mapErr(err)
	}
	if owner != binding.OwnerID || conversation != binding.ConversationID {
		return "", ErrForbidden
	}
	return binding.TurnID, nil
}

type rowQuery interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadObservation(ctx context.Context, q rowQuery, task taskView) (observationRow, bool, error) {
	var row observationRow
	var eventID, outboxID *string
	var applied *int64
	var processing *string
	var cursor *int64
	err := q.QueryRow(ctx, `
		SELECT t.message_id, t.business_event_id, t.outbox_id,
		       o.published_at, r.acked_at,
		       e.cursor, c.applied_public_seq, c.processing_cursor
		FROM terminal_observations t
		LEFT JOIN outbox o ON o.id = t.outbox_id
		LEFT JOIN projection_receipts r
		  ON r.workspace_id = t.workspace_id AND r.task_id = t.task_id
		 AND r.run_id = t.run_id AND r.state = t.state AND r.reason = t.reason
		LEFT JOIN business_events e ON e.id = t.business_event_id
		LEFT JOIN source_checkpoints c
		  ON c.workspace_id = t.workspace_id AND c.task_id = t.task_id AND c.run_id = t.run_id
		WHERE t.workspace_id = $1 AND t.task_id = $2 AND t.run_id = $3
		  AND t.state = $4 AND t.reason = $5`,
		task.WorkspaceID, task.ID, task.RunID, "FAILED", "AGENT_FAILED",
	).Scan(&row.MessageID, &eventID, &outboxID, &row.PublishedAt, &row.AckedAt, &cursor, &applied, &processing)
	if errors.Is(mapErr(err), ErrNotFound) {
		return observationRow{}, false, nil
	}
	if err != nil {
		return observationRow{}, false, err
	}
	if eventID != nil {
		row.EventID = *eventID
	}
	if outboxID != nil {
		row.OutboxID = *outboxID
	}
	if cursor != nil {
		row.BusinessCursor = *cursor
	}
	if applied != nil {
		row.AppliedSeq = *applied
	}
	if processing != nil {
		row.Processing = *processing
	}
	return row, true, nil
}

func insertAssistantError(ctx context.Context, tx pgx.Tx, binding TestBinding, message string) (string, error) {
	var userID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM messages
		WHERE turn_id = $1 AND role = 'user'
		ORDER BY seq
		LIMIT 1`, binding.TurnID).Scan(&userID)
	if err != nil {
		return "", mapErr(err)
	}
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT id FROM messages WHERE turn_id = $1 AND role = 'assistant' LIMIT 1`,
		binding.TurnID).Scan(&existing)
	if err == nil {
		return "", fmt.Errorf("assistant row already exists for this turn")
	}
	if !errors.Is(mapErr(err), ErrNotFound) {
		return "", err
	}
	ts := now()
	msg, err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO messages (
			id, session_id, turn_id, role, content, status,
			error_code, error_message, reply_to_id, created_at, updated_at
		) VALUES ($1, $2, $3, 'assistant', '', 'error', 'AGENT_FAILED', $4, $5, $6, $6)
		RETURNING `+messageCols,
		uuid.NewString(), binding.ConversationID, binding.TurnID, message, userID, ts,
	))
	if err != nil {
		return "", err
	}
	if msg.Status != "error" {
		return "", fmt.Errorf("assistant status %s", msg.Status)
	}
	return msg.ID, nil
}

func linkTurn(ctx context.Context, tx pgx.Tx, binding TestBinding, task taskView) error {
	tag, err := tx.Exec(ctx, `
		UPDATE turns
		SET makez_workspace_id = $1, makez_task_id = $2, makez_run_id = $3
		WHERE id = $4 AND owner_id = $5 AND conversation_id = $6
		  AND (
		    makez_task_id IS NULL
		    OR (makez_workspace_id = $1 AND makez_task_id = $2 AND makez_run_id = $3)
		  )`,
		task.WorkspaceID, task.ID, task.RunID, binding.TurnID, binding.OwnerID, binding.ConversationID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("turn is already linked to a different task")
	}
	return nil
}

func writeLogs(ctx context.Context, tx pgx.Tx, logs []publicLog) error {
	for _, log := range logs {
		var stored string
		err := tx.QueryRow(ctx, `
			SELECT payload_sha256 FROM source_log_dedup
			WHERE run_id = $1 AND public_seq = $2`, log.RunID, log.Seq).Scan(&stored)
		if err == nil {
			// A different payload at the same public seq is not appended.
			continue
		}
		if !errors.Is(mapErr(err), ErrNotFound) {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_log_dedup (run_id, public_seq, payload_sha256, failure_text)
			VALUES ($1, $2, $3, $4)`,
			log.RunID, log.Seq, log.Hash, log.FailureText); err != nil {
			return err
		}
	}
	return nil
}

func insertObservation(ctx context.Context, tx pgx.Tx, task taskView, messageID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO terminal_observations (
			workspace_id, task_id, run_id, state, reason, message_id
		) VALUES ($1, $2, $3, 'FAILED', 'AGENT_FAILED', $4)`,
		task.WorkspaceID, task.ID, task.RunID, messageID,
	)
	return err
}

func insertReceipt(ctx context.Context, tx pgx.Tx, task taskView) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO projection_receipts (
			workspace_id, task_id, run_id, state, reason
		) VALUES ($1, $2, $3, 'FAILED', 'AGENT_FAILED')`,
		task.WorkspaceID, task.ID, task.RunID,
	)
	return err
}

func upsertCheckpoint(ctx context.Context, tx pgx.Tx, task taskView, applied int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO source_checkpoints (
			workspace_id, task_id, run_id, applied_public_seq, processing_cursor
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, task_id, run_id) DO UPDATE SET
			applied_public_seq = GREATEST(source_checkpoints.applied_public_seq, EXCLUDED.applied_public_seq),
			processing_cursor = CASE
				WHEN EXCLUDED.processing_cursor <> '' THEN EXCLUDED.processing_cursor
				ELSE source_checkpoints.processing_cursor
			END`,
		task.WorkspaceID, task.ID, task.RunID, applied, task.Cursor,
	)
	return err
}

func insertFailureOutbox(ctx context.Context, tx pgx.Tx, binding TestBinding, messageID, message string) (BusinessEvent, string, error) {
	event, err := appendBusinessEvent(ctx, tx, binding.ConversationID, "message.error", messageID, map[string]any{
		"status":  "error",
		"code":    "AGENT_FAILED",
		"message": message,
	})
	if err != nil {
		return BusinessEvent{}, "", err
	}
	payload := fmt.Sprintf(`{"kind":"publish_business_event","businessEventId":%q,"type":"message.error"}`, event.ID)
	outboxID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox (
			id, owner_id, conversation_id, turn_id, kind, payload, created_at
		) VALUES ($1, $2, $3, $4, 'publish_business_event', $5, $6)`,
		outboxID, binding.OwnerID, binding.ConversationID, binding.TurnID, payload, now(),
	); err != nil {
		return BusinessEvent{}, "", err
	}
	return event, outboxID, nil
}

func attachObservation(ctx context.Context, tx pgx.Tx, task taskView, eventID, outboxID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE terminal_observations
		SET business_event_id = $1, outbox_id = $2
		WHERE workspace_id = $3 AND task_id = $4 AND run_id = $5
		  AND state = 'FAILED' AND reason = 'AGENT_FAILED'`,
		eventID, outboxID, task.WorkspaceID, task.ID, task.RunID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("terminal observation was not attached")
	}
	return nil
}

func (s *Store) finishPublish(ctx context.Context, row observationRow, fault FaultPoint, replayed bool) (ProjectResult, error) {
	if row.OutboxID == "" || row.EventID == "" {
		return ProjectResult{}, fmt.Errorf("committed observation is missing its business event")
	}
	result := ProjectResult{
		Replayed:          replayed,
		MessageID:         row.MessageID,
		BusinessCursor:    row.BusinessCursor,
		AppliedPublicSeq:  row.AppliedSeq,
		ProcessingCursor:  row.Processing,
		Published:         row.PublishedAt != nil,
		LocalReceiptAcked: row.AckedAt != nil,
	}
	if row.PublishedAt == nil {
		if fault == FaultPublish {
			return result, InjectedFault{Point: FaultPublish}
		}
		tag, err := s.pool.Exec(ctx, `
			UPDATE outbox SET published_at = $2
			WHERE id = $1 AND published_at IS NULL`, row.OutboxID, now())
		if err != nil {
			return result, err
		}
		if tag.RowsAffected() > 1 {
			return result, fmt.Errorf("publish mark updated more than one outbox row")
		}
		result.Published = true
	}
	if row.AckedAt == nil {
		if fault == FaultSourceAck {
			return result, InjectedFault{Point: FaultSourceAck}
		}
		tag, err := s.pool.Exec(ctx, `
			UPDATE projection_receipts SET acked_at = $6
			WHERE workspace_id = $1 AND task_id = $2 AND run_id = $3
			  AND state = $4 AND reason = $5 AND acked_at IS NULL`,
			capturedWorkspaceID, capturedTaskID, capturedRunID, "FAILED", "AGENT_FAILED", now())
		if err != nil {
			return result, err
		}
		if tag.RowsAffected() > 1 {
			return result, fmt.Errorf("local receipt updated more than one row")
		}
		result.LocalReceiptAcked = true
	}
	return result, nil
}

func maxSeq(logs []publicLog) int64 {
	var max int64
	for _, log := range logs {
		if log.Seq > max {
			max = log.Seq
		}
	}
	return max
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
