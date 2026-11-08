package store

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const sessionCols = `id, agent_id, title, created_at, updated_at`

func scanSession(row scannable) (Session, error) {
	var sess Session
	if err := row.Scan(&sess.ID, &sess.AgentID, &sess.Title, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
		return Session{}, mapErr(err)
	}
	sess.CreatedAt = sess.CreatedAt.UTC()
	sess.UpdatedAt = sess.UpdatedAt.UTC()
	return sess, nil
}

// CreateSession inserts a session for an existing agent.
func (s *Store) CreateSession(ctx context.Context, agentID, title string) (Session, error) {
	if _, err := s.GetAgent(ctx, agentID); err != nil {
		return Session{}, err
	}
	ts := now()
	return scanSession(s.pool.QueryRow(ctx, `
		INSERT INTO sessions (id, agent_id, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		RETURNING `+sessionCols,
		uuid.NewString(), agentID, strings.TrimSpace(title), ts,
	))
}

// ListSessions returns one agent's sessions, most recently updated first.
func (s *Store) ListSessions(ctx context.Context, agentID string) ([]Session, error) {
	if _, err := s.GetAgent(ctx, agentID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sessionCols+`
		FROM sessions
		WHERE agent_id = $1
		ORDER BY updated_at DESC, id ASC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetSession loads one session.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = $1`, id))
}

// SessionExists reports whether the id is a session.
func (s *Store) SessionExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`, id).Scan(&ok)
	return ok, err
}

func clipTitle(content string) string {
	content = strings.TrimSpace(content)
	if i := strings.IndexAny(content, "\r\n"); i >= 0 {
		content = strings.TrimSpace(content[:i])
	}
	content = strings.Join(strings.Fields(content), " ")
	if utf8.RuneCountInString(content) <= 80 {
		return content
	}
	runes := []rune(content)
	return string(runes[:80])
}
