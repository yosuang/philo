package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const messageCols = `id, session_id, role, content, status, error_code, error_message, adapter, reply_to_id, created_at, updated_at`

func scanMessage(row scannable) (Message, error) {
	var m Message
	var code, emsg, adpt, reply *string
	if err := row.Scan(
		&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Status,
		&code, &emsg, &adpt, &reply, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return Message{}, mapErr(err)
	}
	m.CreatedAt = m.CreatedAt.UTC()
	m.UpdatedAt = m.UpdatedAt.UTC()
	if code != nil {
		text := ""
		if emsg != nil {
			text = *emsg
		}
		m.Error = &APIError{Code: *code, Message: text}
	}
	if adpt != nil && *adpt != "" {
		m.Metadata = &Metadata{Adapter: *adpt}
	}
	if reply != nil {
		m.ReplyToID = *reply
	}
	return m, nil
}

// ListMessages returns the transcript in conversation order.
func (s *Store) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
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
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessage loads one message.
func (s *Store) GetMessage(ctx context.Context, id string) (Message, error) {
	return scanMessage(s.pool.QueryRow(ctx, `SELECT `+messageCols+` FROM messages WHERE id = $1`, id))
}

// BeginTurn stores the user message and a streaming assistant placeholder.
// The session row is locked so two replies cannot start together.
func (s *Store) BeginTurn(ctx context.Context, sessionID, content, adapterName string) (Turn, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Turn{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sess, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM sessions WHERE id = $1 FOR UPDATE`, sessionID))
	if err != nil {
		return Turn{}, err
	}
	var streaming bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM messages WHERE session_id = $1 AND status = 'streaming')`,
		sessionID,
	).Scan(&streaming); err != nil {
		return Turn{}, err
	}
	if streaming {
		return Turn{}, ErrBusy
	}

	ts := now()
	userID := uuid.NewString()
	user, err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO messages (
			id, session_id, role, content, status, created_at, updated_at
		) VALUES ($1, $2, 'user', $3, 'complete', $4, $4)
		RETURNING `+messageCols,
		userID, sessionID, content, ts,
	))
	if err != nil {
		return Turn{}, err
	}

	assistant, err := scanMessage(tx.QueryRow(ctx, `
		INSERT INTO messages (
			id, session_id, role, content, status, adapter, reply_to_id, created_at, updated_at
		) VALUES ($1, $2, 'assistant', '', 'streaming', $3, $4, $5, $5)
		RETURNING `+messageCols,
		uuid.NewString(), sessionID, adapterName, userID, ts.Add(time.Millisecond),
	))
	if err != nil {
		return Turn{}, err
	}

	title := sess.Title
	if strings.TrimSpace(title) == "" {
		title = clipTitle(content)
	}
	sess, err = scanSession(tx.QueryRow(ctx, `
		UPDATE sessions SET title = $2, updated_at = $3 WHERE id = $1
		RETURNING `+sessionCols,
		sessionID, title, ts,
	))
	if err != nil {
		return Turn{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Turn{}, err
	}
	return Turn{User: user, Assistant: assistant, Session: sess}, nil
}

// BeginRetry resets an assistant message that ended in error or canceled.
// The session row is locked before the message row, same order as BeginTurn.
func (s *Store) BeginRetry(ctx context.Context, messageID string) (Message, Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sessionID string
	if err := tx.QueryRow(ctx, `SELECT session_id FROM messages WHERE id = $1`, messageID).Scan(&sessionID); err != nil {
		return Message{}, Session{}, mapErr(err)
	}
	if _, err := scanSession(tx.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM sessions WHERE id = $1 FOR UPDATE`, sessionID)); err != nil {
		return Message{}, Session{}, err
	}
	msg, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+messageCols+` FROM messages WHERE id = $1 FOR UPDATE`, messageID))
	if err != nil {
		return Message{}, Session{}, err
	}
	if msg.Role != "assistant" || (msg.Status != "error" && msg.Status != "canceled") {
		return Message{}, Session{}, ErrNotRetryable
	}
	var streaming bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM messages
			WHERE session_id = $1 AND status = 'streaming' AND id <> $2
		)`, msg.SessionID, messageID,
	).Scan(&streaming); err != nil {
		return Message{}, Session{}, err
	}
	if streaming {
		return Message{}, Session{}, ErrBusy
	}

	ts := now()
	msg, err = scanMessage(tx.QueryRow(ctx, `
		UPDATE messages
		SET content = '', status = 'streaming', error_code = NULL, error_message = NULL, updated_at = $2
		WHERE id = $1 AND status IN ('error', 'canceled')
		RETURNING `+messageCols,
		messageID, ts,
	))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Message{}, Session{}, ErrNotRetryable
		}
		return Message{}, Session{}, err
	}
	sess, err := scanSession(tx.QueryRow(ctx, `
		UPDATE sessions SET updated_at = $2 WHERE id = $1
		RETURNING `+sessionCols,
		msg.SessionID, ts,
	))
	if err != nil {
		return Message{}, Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, Session{}, err
	}
	return msg, sess, nil
}

// AppendContent adds a delta to a message that is still streaming.
func (s *Store) AppendContent(ctx context.Context, messageID, text string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE messages
		SET content = content || $2, updated_at = $3
		WHERE id = $1 AND status = 'streaming'`,
		messageID, text, now(),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkCanceled persists a cancel if the reply is still streaming.
func (s *Store) MarkCanceled(ctx context.Context, messageID string) (Message, bool, error) {
	msg, err := scanMessage(s.pool.QueryRow(ctx, `
		UPDATE messages
		SET status = 'canceled', updated_at = $2
		WHERE id = $1 AND status = 'streaming'
		RETURNING `+messageCols,
		messageID, now(),
	))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Message{}, false, nil
		}
		return Message{}, false, err
	}
	return msg, true, nil
}

// MarkError persists a generation failure if the reply is still streaming.
func (s *Store) MarkError(ctx context.Context, messageID, code, message string) (Message, bool, error) {
	msg, err := scanMessage(s.pool.QueryRow(ctx, `
		UPDATE messages
		SET status = 'error', error_code = $2, error_message = $3, updated_at = $4
		WHERE id = $1 AND status = 'streaming'
		RETURNING `+messageCols,
		messageID, code, message, now(),
	))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Message{}, false, nil
		}
		return Message{}, false, err
	}
	return msg, true, nil
}

// FinishStreaming marks a reply complete and, when asked, inserts one approval
// in the same transaction. A cancel that wins the race leaves no approval.
func (s *Store) FinishStreaming(ctx context.Context, messageID, kind, summary string) (Message, *Approval, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanMessage(tx.QueryRow(ctx, `
		SELECT `+messageCols+` FROM messages WHERE id = $1 FOR UPDATE`, messageID))
	if err != nil {
		return Message{}, nil, false, err
	}
	if current.Status != "streaming" {
		return current, nil, false, nil
	}
	ts := now()
	msg, err := scanMessage(tx.QueryRow(ctx, `
		UPDATE messages
		SET status = 'complete', updated_at = $2
		WHERE id = $1 AND status = 'streaming'
		RETURNING `+messageCols,
		messageID, ts,
	))
	if err != nil {
		return Message{}, nil, false, err
	}

	var approval *Approval
	if kind != "" && summary != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM approvals WHERE message_id = $1)`, messageID,
		).Scan(&exists); err != nil {
			return Message{}, nil, false, err
		}
		if !exists {
			row, err := insertApproval(ctx, tx, msg.SessionID, messageID, kind, summary, ts)
			if err != nil {
				return Message{}, nil, false, err
			}
			approval = &row
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, nil, false, err
	}
	return msg, approval, true, nil
}

// StreamingMessage returns the in-flight assistant message for a session.
func (s *Store) StreamingMessage(ctx context.Context, sessionID string) (Message, error) {
	return scanMessage(s.pool.QueryRow(ctx, `
		SELECT `+messageCols+`
		FROM messages
		WHERE session_id = $1 AND status = 'streaming' AND role = 'assistant'
		ORDER BY seq DESC
		LIMIT 1`, sessionID))
}
