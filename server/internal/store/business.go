package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BusinessEvent is one Philo business-log row. Cursor is not message.seq.
type BusinessEvent struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"sessionId"`
	Cursor         int64           `json:"cursor"`
	Type           string          `json:"type"`
	MessageID      string          `json:"messageId,omitempty"`
	Payload        json.RawMessage `json:"-"`
}

// Snapshot is materialized conversation state at one business high-water.
// It does not embed the historical business-event log.
type Snapshot struct {
	SessionID string     `json:"sessionId"`
	HighWater int64      `json:"highWater"`
	Messages  []Message  `json:"messages"`
	Approvals []Approval `json:"approvals"`
}

// FixtureSource is the only label the fixture business source accepts.
// It is not Makez and not a model.
const FixtureSource = "fixture-business-source"

// FixtureInput is one labeled fixture event. EventKey dedups that fixture
// input. It is not a Makez source offset and not a turn id.
type FixtureInput struct {
	Source         string
	OwnerID        string
	ConversationID string
	EventKey       string
	Type           string
	MessageID      string
	Text           string
	Status         string
}

func (e BusinessEvent) MarshalMap() (map[string]any, error) {
	payload := map[string]any{}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return nil, err
		}
	}
	payload["id"] = e.ID
	payload["cursor"] = e.Cursor
	payload["type"] = e.Type
	payload["sessionId"] = e.ConversationID
	if e.MessageID != "" {
		payload["messageId"] = e.MessageID
	}
	return payload, nil
}

func appendBusinessEvent(ctx context.Context, q queryer, conversationID, eventType, messageID string, fields map[string]any) (BusinessEvent, error) {
	var cursor int64
	err := q.QueryRow(ctx, `
		UPDATE sessions
		SET business_high_water = business_high_water + 1
		WHERE id = $1
		RETURNING business_high_water`, conversationID).Scan(&cursor)
	if err != nil {
		return BusinessEvent{}, mapErr(err)
	}
	if fields == nil {
		fields = map[string]any{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return BusinessEvent{}, err
	}
	ev := BusinessEvent{
		ID:             uuid.NewString(),
		ConversationID: conversationID,
		Cursor:         cursor,
		Type:           eventType,
		MessageID:      messageID,
		Payload:        raw,
	}
	_, err = q.Exec(ctx, `
		INSERT INTO business_events (
			id, conversation_id, cursor, event_type, message_id, payload, created_at
		) VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)`,
		ev.ID, conversationID, cursor, eventType, messageID, raw, now(),
	)
	if err != nil {
		return BusinessEvent{}, err
	}
	return ev, nil
}

func scanBusinessEvent(row scannable) (BusinessEvent, error) {
	var ev BusinessEvent
	var messageID *string
	if err := row.Scan(&ev.ID, &ev.ConversationID, &ev.Cursor, &ev.Type, &messageID, &ev.Payload); err != nil {
		return BusinessEvent{}, mapErr(err)
	}
	if messageID != nil {
		ev.MessageID = *messageID
	}
	return ev, nil
}

const businessEventCols = `id, conversation_id, cursor, event_type, message_id, payload`

// Snapshot loads messages, approvals, and the business high-water in one
// repeatable-read transaction. Events at or before that waterline are already
// in the returned state. The historical log is not included.
func (s *Store) Snapshot(ctx context.Context, ownerID, sessionID string) (Snapshot, error) {
	snap, _, err := s.ConsistentView(ctx, ownerID, sessionID)
	return snap, err
}

// ConsistentView reads the snapshot and the events at or before its high-water
// in one transaction. HTTP snapshots omit that event list. Callers use it to
// check that the waterline matches the materialized state.
func (s *Store) ConsistentView(ctx context.Context, ownerID, sessionID string) (Snapshot, []BusinessEvent, error) {
	if _, err := s.RequireSession(ctx, ownerID, sessionID); err != nil {
		return Snapshot{}, nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var high int64
	if err := tx.QueryRow(ctx, `SELECT business_high_water FROM sessions WHERE id = $1`, sessionID).Scan(&high); err != nil {
		return Snapshot{}, nil, mapErr(err)
	}
	messages, err := listMessages(ctx, tx, sessionID)
	if err != nil {
		return Snapshot{}, nil, err
	}
	approvals, err := listApprovals(ctx, tx, sessionID)
	if err != nil {
		return Snapshot{}, nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+businessEventCols+`
		FROM business_events
		WHERE conversation_id = $1 AND cursor <= $2
		ORDER BY cursor ASC`, sessionID, high)
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer rows.Close()
	reflected := []BusinessEvent{}
	for rows.Next() {
		ev, err := scanBusinessEvent(rows)
		if err != nil {
			return Snapshot{}, nil, err
		}
		reflected = append(reflected, ev)
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, nil, err
	}
	return Snapshot{SessionID: sessionID, HighWater: high, Messages: messages, Approvals: approvals}, reflected, nil
}

// BusinessEventsAfter returns events strictly after the cursor.
// An expired cursor returns ErrCursorExpired and a snapshot whose high-water
// matches that snapshot. The snapshot does not contain the log.
func (s *Store) BusinessEventsAfter(ctx context.Context, ownerID, sessionID string, after int64) ([]BusinessEvent, Snapshot, error) {
	snap, err := s.Snapshot(ctx, ownerID, sessionID)
	if err != nil {
		return nil, Snapshot{}, err
	}
	var oldest *int64
	err = s.pool.QueryRow(ctx, `
		SELECT MIN(cursor) FROM business_events WHERE conversation_id = $1`, sessionID).Scan(&oldest)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if cursorExpired(after, snap.HighWater, oldest) {
		return nil, snap, ErrCursorExpired
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+businessEventCols+`
		FROM business_events
		WHERE conversation_id = $1 AND cursor > $2
		ORDER BY cursor ASC`, sessionID, after)
	if err != nil {
		return nil, Snapshot{}, err
	}
	defer rows.Close()
	out := []BusinessEvent{}
	for rows.Next() {
		ev, err := scanBusinessEvent(rows)
		if err != nil {
			return nil, Snapshot{}, err
		}
		out = append(out, ev)
	}
	return out, snap, rows.Err()
}

func cursorExpired(after, high int64, oldest *int64) bool {
	if after >= high {
		return false
	}
	if oldest == nil {
		return true
	}
	return after < *oldest-1
}

// StageExpiredReplacement sets the latest message to content, drops the
// retained business log, and advances the high water by one. A client that
// still holds the previous high water then receives ErrCursorExpired. The
// message seq is not changed. This is the labeled fixture, not Makez.
func (s *Store) StageExpiredReplacement(ctx context.Context, ownerID, conversationID, content string) (string, int64, error) {
	if _, err := s.RequireSession(ctx, ownerID, conversationID); err != nil {
		return "", 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var messageID string
	err = tx.QueryRow(ctx, `
		UPDATE messages
		SET content = $2, status = 'complete', updated_at = $3
		WHERE id = (
			SELECT id FROM messages
			WHERE session_id = $1
			ORDER BY seq DESC
			LIMIT 1
		)
		RETURNING id`, conversationID, content, now()).Scan(&messageID)
	if err != nil {
		return "", 0, mapErr(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM fixture_business_dedup WHERE conversation_id = $1`, conversationID); err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM business_events WHERE conversation_id = $1`, conversationID); err != nil {
		return "", 0, err
	}
	var high int64
	err = tx.QueryRow(ctx, `
		UPDATE sessions
		SET business_high_water = business_high_water + 1
		WHERE id = $1
		RETURNING business_high_water`, conversationID).Scan(&high)
	if err != nil {
		return "", 0, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return messageID, high, nil
}

// ApplyFixtureBusiness applies one explicitly labeled fixture event and the
// matching business row in one transaction. Repeating the event key does not
// append again. This source is not Makez and does not complete a run.
func (s *Store) ApplyFixtureBusiness(ctx context.Context, in FixtureInput) (BusinessEvent, error) {
	if in.Source != FixtureSource || in.EventKey == "" {
		return BusinessEvent{}, errors.New("fixture source label is required")
	}
	if _, err := s.RequireSession(ctx, in.OwnerID, in.ConversationID); err != nil {
		return BusinessEvent{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BusinessEvent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE id = $1 FOR UPDATE`, in.ConversationID); err != nil {
		return BusinessEvent{}, err
	}
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT business_event_id FROM fixture_business_dedup
		WHERE conversation_id = $1 AND event_key = $2`,
		in.ConversationID, in.EventKey).Scan(&existing)
	if err == nil {
		ev, err := scanBusinessEvent(tx.QueryRow(ctx, `
			SELECT `+businessEventCols+` FROM business_events WHERE id = $1`, existing))
		if err != nil {
			return BusinessEvent{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return BusinessEvent{}, err
		}
		return ev, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BusinessEvent{}, err
	}

	ev, err := applyFixtureLocked(ctx, tx, in)
	if err != nil {
		return BusinessEvent{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO fixture_business_dedup (conversation_id, event_key, business_event_id)
		VALUES ($1, $2, $3)`,
		in.ConversationID, in.EventKey, ev.ID); err != nil {
		return BusinessEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BusinessEvent{}, err
	}
	return ev, nil
}

func applyFixtureLocked(ctx context.Context, tx pgx.Tx, in FixtureInput) (BusinessEvent, error) {
	switch in.Type {
	case "fixture.seed":
		ts := now()
		msg, err := scanMessage(tx.QueryRow(ctx, `
			INSERT INTO messages (
				id, session_id, role, content, status, adapter, created_at, updated_at
			) VALUES ($1, $2, 'assistant', '', 'streaming', $3, $4, $4)
			RETURNING `+messageCols,
			uuid.NewString(), in.ConversationID, FixtureSource, ts,
		))
		if err != nil {
			return BusinessEvent{}, err
		}
		return appendBusinessEvent(ctx, tx, in.ConversationID, "message.created", msg.ID, map[string]any{
			"message": msg,
		})
	case "message.delta":
		msg, err := scanMessage(tx.QueryRow(ctx, `
			UPDATE messages
			SET content = content || $2, updated_at = $3
			WHERE id = $1 AND session_id = $4
			RETURNING `+messageCols,
			in.MessageID, in.Text, now(), in.ConversationID,
		))
		if err != nil {
			return BusinessEvent{}, err
		}
		return appendBusinessEvent(ctx, tx, in.ConversationID, "message.delta", msg.ID, map[string]any{
			"text": in.Text,
		})
	case "message.completed":
		msg, err := scanMessage(tx.QueryRow(ctx, `
			UPDATE messages
			SET status = 'complete', updated_at = $2
			WHERE id = $1 AND session_id = $3
			RETURNING `+messageCols,
			in.MessageID, now(), in.ConversationID,
		))
		if err != nil {
			return BusinessEvent{}, err
		}
		return appendBusinessEvent(ctx, tx, in.ConversationID, "message.completed", msg.ID, map[string]any{
			"status": "complete",
		})
	case "message.updated":
		if in.Status == "" {
			return BusinessEvent{}, errors.New("status is required")
		}
		msg, err := scanMessage(tx.QueryRow(ctx, `
			UPDATE messages
			SET status = $2, updated_at = $3
			WHERE id = $1 AND session_id = $4
			RETURNING `+messageCols,
			in.MessageID, in.Status, now(), in.ConversationID,
		))
		if err != nil {
			return BusinessEvent{}, err
		}
		return appendBusinessEvent(ctx, tx, in.ConversationID, "message.updated", msg.ID, map[string]any{
			"status": msg.Status,
		})
	default:
		return BusinessEvent{}, errors.New("unsupported fixture event")
	}
}

func listMessages(ctx context.Context, q queryer, sessionID string) ([]Message, error) {
	rows, err := q.Query(ctx, `
		SELECT `+messageCols+`
		FROM messages
		WHERE session_id = $1
		ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func listApprovals(ctx context.Context, q queryer, sessionID string) ([]Approval, error) {
	rows, err := q.Query(ctx, `
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
		row, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
