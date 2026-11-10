package store

import (
	"context"

	"github.com/yosuang/philo/server/internal/auth"
)

// EnsurePrincipal stores the server-configured actor and owner if they are new.
// It does not read an owner from a client payload.
func (s *Store) EnsurePrincipal(ctx context.Context, principal auth.Principal) error {
	ts := now()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO owners (id, created_at) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, principal.OwnerID, ts); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO actors (id, owner_id, created_at) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`, principal.ActorID, principal.OwnerID, ts)
	return err
}

// RequireAgent loads an agent owned by ownerID.
func (s *Store) RequireAgent(ctx context.Context, ownerID, agentID string) (Agent, error) {
	agent, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return Agent{}, err
	}
	if agent.OwnerID != ownerID {
		return Agent{}, ErrForbidden
	}
	return agent, nil
}

// RequireSession loads a conversation whose bot is owned by ownerID.
func (s *Store) RequireSession(ctx context.Context, ownerID, sessionID string) (Session, error) {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if _, err := s.RequireAgent(ctx, ownerID, sess.AgentID); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// SessionOwned reports whether ownerID can access the session.
func (s *Store) SessionOwned(ctx context.Context, ownerID, sessionID string) (bool, error) {
	_, err := s.RequireSession(ctx, ownerID, sessionID)
	if err == ErrNotFound || err == ErrForbidden {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
