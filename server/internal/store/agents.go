package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

const agentCols = `id, name, instructions, model_id, owner_id, created_at, updated_at`

func scanAgent(row scannable) (Agent, error) {
	var a Agent
	var owner *string
	if err := row.Scan(&a.ID, &a.Name, &a.Instructions, &a.ModelID, &owner, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Agent{}, mapErr(err)
	}
	if owner != nil {
		a.OwnerID = *owner
	}
	a.CreatedAt = a.CreatedAt.UTC()
	a.UpdatedAt = a.UpdatedAt.UTC()
	return a, nil
}

// CreateAgent inserts an agent owned by ownerID. The server generates the id.
func (s *Store) CreateAgent(ctx context.Context, ownerID, name, instructions, modelID string) (Agent, error) {
	ts := now()
	return scanAgent(s.pool.QueryRow(ctx, `
		INSERT INTO agents (id, name, instructions, model_id, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		RETURNING `+agentCols,
		uuid.NewString(), strings.TrimSpace(name), instructions, strings.TrimSpace(modelID), ownerID, ts,
	))
}

// ListAgents returns one owner's agents, oldest first.
func (s *Store) ListAgents(ctx context.Context, ownerID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+agentCols+`
		FROM agents
		WHERE owner_id = $1
		ORDER BY created_at ASC, id ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgent loads one agent.
func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	return scanAgent(s.pool.QueryRow(ctx, `SELECT `+agentCols+` FROM agents WHERE id = $1`, id))
}

// UpdateAgent applies a partial update. Nil fields stay unchanged.
func (s *Store) UpdateAgent(ctx context.Context, ownerID, id string, name, instructions, modelID *string) (Agent, error) {
	current, err := s.RequireAgent(ctx, ownerID, id)
	if err != nil {
		return Agent{}, err
	}
	if name != nil {
		current.Name = strings.TrimSpace(*name)
	}
	if instructions != nil {
		current.Instructions = *instructions
	}
	if modelID != nil {
		current.ModelID = strings.TrimSpace(*modelID)
	}
	ts := now()
	return scanAgent(s.pool.QueryRow(ctx, `
		UPDATE agents
		SET name = $2, instructions = $3, model_id = $4, updated_at = $5
		WHERE id = $1
		RETURNING `+agentCols,
		id, current.Name, current.Instructions, current.ModelID, ts,
	))
}
