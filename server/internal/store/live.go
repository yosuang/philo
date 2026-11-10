package store

import (
	"context"
	"fmt"
)

// ErrUnsupportedTerminal means the service GET is terminal and is not the
// no-key failure. This slice does not project a successful chat from it.
var ErrUnsupportedTerminal = fmt.Errorf("terminal state is not the no-key failure")

// LiveBinding associates one Philo turn with a Makez task under the test label.
// OwnerID is the server principal already stored on the turn.
// WorkspaceID is the Makez workspace the test client is configured to call.
// It is not OwnerID and it is not read from the client request.
type LiveBinding struct {
	Label          string
	OwnerID        string
	ConversationID string
	TurnID         string
	BotID          string
	WorkspaceID    string
}

func (b LiveBinding) test() TestBinding {
	return TestBinding{
		Label:                  b.Label,
		OwnerID:                b.OwnerID,
		ConversationID:         b.ConversationID,
		TurnID:                 b.TurnID,
		CapturedConversationID: b.ConversationID,
	}
}

func validateLive(b LiveBinding) error {
	if b.Label != TestBindingLabel {
		return ErrUnlabeledBinding
	}
	if b.OwnerID == "" || b.ConversationID == "" || b.TurnID == "" || b.BotID == "" || b.WorkspaceID == "" {
		return fmt.Errorf("incomplete test binding")
	}
	if b.WorkspaceID == b.OwnerID {
		return fmt.Errorf("test workspace is not an owner id")
	}
	return nil
}

// ServiceLink is the task and run a service response returned.
// RunID may be empty when the submit response has no run yet.
type ServiceLink struct {
	WorkspaceID string
	TaskID      string
	RunID       string
}

// ServiceRead is one consume of the public task routes.
// AuthoritativeGET is the raw GET /v1/tasks/:id body.
// SSE status frames are not that body.
type ServiceRead struct {
	SSE              []byte
	AuthoritativeGET []byte
}

// LinkServiceTask stores the task id, and the run id when the service has one.
// A different task already stored on the turn is refused.
func (s *Store) LinkServiceTask(ctx context.Context, binding LiveBinding, link ServiceLink) error {
	if err := validateLive(binding); err != nil {
		return err
	}
	if link.WorkspaceID == "" || link.TaskID == "" || link.WorkspaceID != binding.WorkspaceID {
		return fmt.Errorf("service task is missing its test workspace")
	}
	if link.WorkspaceID == binding.OwnerID {
		return fmt.Errorf("test workspace is not an owner id")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockTurn(ctx, tx, binding.test()); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE turns
		SET makez_workspace_id = $1,
		    makez_task_id = $2,
		    makez_run_id = CASE WHEN $3 = '' THEN makez_run_id ELSE $3 END
		WHERE id = $4 AND owner_id = $5 AND conversation_id = $6
		  AND (
		    makez_task_id IS NULL
		    OR (
		      makez_workspace_id = $1 AND makez_task_id = $2
		      AND (makez_run_id IS NULL OR $3 = '' OR makez_run_id = $3)
		    )
		  )`,
		link.WorkspaceID, link.TaskID, link.RunID, binding.TurnID, binding.OwnerID, binding.ConversationID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("turn is already linked to a different task")
	}
	return tx.Commit(ctx)
}

// ServiceTask loads the association stored for a turn.
// The second result is false when the turn has no task id yet.
func (s *Store) ServiceTask(ctx context.Context, binding LiveBinding) (ServiceLink, bool, error) {
	if err := validateLive(binding); err != nil {
		return ServiceLink{}, false, err
	}
	var workspace, task, run *string
	err := s.pool.QueryRow(ctx, `
		SELECT makez_workspace_id, makez_task_id, makez_run_id
		FROM turns
		WHERE id = $1 AND owner_id = $2 AND conversation_id = $3`,
		binding.TurnID, binding.OwnerID, binding.ConversationID,
	).Scan(&workspace, &task, &run)
	if err != nil {
		return ServiceLink{}, false, mapErr(err)
	}
	if task == nil || *task == "" {
		return ServiceLink{}, false, nil
	}
	link := ServiceLink{WorkspaceID: stringOrEmpty(workspace), TaskID: *task, RunID: stringOrEmpty(run)}
	return link, true, nil
}

// LinkedTurn is one turn that already stores a task in the given workspace.
// The owner and bot are the rows already stored. This list does not allocate either.
type LinkedTurn struct {
	Binding LiveBinding
	Link    ServiceLink
}

// LinkedTurns returns turns whose stored task workspace is workspaceID.
// A turn with no task id is omitted. The caller must not submit one for it.
func (s *Store) LinkedTurns(ctx context.Context, workspaceID string) ([]LinkedTurn, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.owner_id, t.conversation_id, t.id, s.agent_id,
		       t.makez_workspace_id, t.makez_task_id, t.makez_run_id
		FROM turns t
		JOIN sessions s ON s.id = t.conversation_id
		WHERE t.makez_workspace_id = $1
		  AND t.makez_task_id IS NOT NULL
		  AND t.makez_run_id IS NOT NULL
		ORDER BY t.created_at, t.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkedTurn
	for rows.Next() {
		var owner, conversation, turn, bot, workspace, task, run string
		if err := rows.Scan(&owner, &conversation, &turn, &bot, &workspace, &task, &run); err != nil {
			return nil, err
		}
		out = append(out, LinkedTurn{
			Binding: LiveBinding{
				Label:          TestBindingLabel,
				OwnerID:        owner,
				ConversationID: conversation,
				TurnID:         turn,
				BotID:          bot,
				WorkspaceID:    workspace,
			},
			Link: ServiceLink{WorkspaceID: workspace, TaskID: task, RunID: run},
		})
	}
	return out, rows.Err()
}

// AppliedPublicSeq is the Makez public log seq stored for this run.
// It is not message.seq and not the business high-water. A missing row is 0.
func (s *Store) AppliedPublicSeq(ctx context.Context, link ServiceLink) (int64, error) {
	if link.WorkspaceID == "" || link.TaskID == "" || link.RunID == "" {
		return 0, nil
	}
	var seq int64
	err := s.pool.QueryRow(ctx, `
		SELECT applied_public_seq FROM source_checkpoints
		WHERE workspace_id = $1 AND task_id = $2 AND run_id = $3`,
		link.WorkspaceID, link.TaskID, link.RunID).Scan(&seq)
	if err != nil {
		if mapErr(err) == ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	return seq, nil
}

// TurnPrompt is the user message stored for the turn.
func (s *Store) TurnPrompt(ctx context.Context, binding LiveBinding) (string, error) {
	if err := validateLive(binding); err != nil {
		return "", err
	}
	var content string
	err := s.pool.QueryRow(ctx, `
		SELECT m.content
		FROM messages m
		JOIN turns t ON t.id = m.turn_id
		WHERE m.turn_id = $1 AND m.role = 'user'
		  AND t.owner_id = $2 AND t.conversation_id = $3
		ORDER BY m.seq
		LIMIT 1`, binding.TurnID, binding.OwnerID, binding.ConversationID).Scan(&content)
	if err != nil {
		return "", mapErr(err)
	}
	if content == "" {
		return "", fmt.Errorf("turn has no user message")
	}
	return content, nil
}

// TurnAssistant loads the assistant row for a turn, if one has been projected.
func (s *Store) TurnAssistant(ctx context.Context, turnID string) (Message, error) {
	return scanMessage(s.pool.QueryRow(ctx, `
		SELECT `+messageCols+`
		FROM messages
		WHERE turn_id = $1 AND role = 'assistant'
		LIMIT 1`, turnID))
}

// ProjectServiceRead applies one live read of the public log and the task GET.
// A status frame with an SSE id is rejected. A status frame without an id is
// not the terminal fact. The GET body is.
func (s *Store) ProjectServiceRead(ctx context.Context, binding LiveBinding, read ServiceRead, fault FaultPoint) (ProjectResult, error) {
	if err := validateLive(binding); err != nil {
		return ProjectResult{}, err
	}
	if err := validateFault(fault); err != nil {
		return ProjectResult{}, err
	}
	parsed, err := parseService(read, binding)
	if err != nil {
		return ProjectResult{}, err
	}
	return s.applyProjection(ctx, binding.test(), parsed, fault)
}

func parseService(read ServiceRead, binding LiveBinding) (parsedCapture, error) {
	var out parsedCapture
	if len(read.SSE) > 0 {
		frames, err := ParseSSE(read.SSE)
		if err != nil {
			return parsedCapture{}, err
		}
		for _, frame := range frames {
			switch frame.Event {
			case "log":
				logs, err := parsePublicLogs(frame.Data)
				if err != nil {
					return parsedCapture{}, err
				}
				if len(logs) != 1 {
					return parsedCapture{}, fmt.Errorf("log frame must carry one public log")
				}
				log := logs[0]
				if frame.ID == "" {
					return parsedCapture{}, fmt.Errorf("log frame has no sse id")
				}
				if frame.ID != fmt.Sprintf("%d", log.Seq) {
					return parsedCapture{}, fmt.Errorf("sse id %s is not public seq %d", frame.ID, log.Seq)
				}
				out.logs = append(out.logs, log)
			case "status":
				if frame.ID != "" {
					return parsedCapture{}, fmt.Errorf("status frame has an sse id")
				}
				out.statusFrames++
			default:
				return parsedCapture{}, fmt.Errorf("unsupported sse event %q", frame.Event)
			}
		}
	}
	if len(read.AuthoritativeGET) > 0 {
		task, err := parseTask(read.AuthoritativeGET)
		if err != nil {
			return parsedCapture{}, err
		}
		if task.WorkspaceID != binding.WorkspaceID {
			return parsedCapture{}, fmt.Errorf("service workspace does not match the test binding")
		}
		if task.AgentID != "pi" || task.AgentVersion != "1" {
			return parsedCapture{}, fmt.Errorf("service task is not pi@1")
		}
		switch task.State {
		case "QUEUED", "RUNNING", "CANCELING":
		case "FAILED":
			if !task.noKeyFailure() {
				return parsedCapture{}, ErrUnsupportedTerminal
			}
		case "SUCCEEDED", "CANCELED":
			return parsedCapture{}, ErrUnsupportedTerminal
		default:
			return parsedCapture{}, fmt.Errorf("unsupported task state %q", task.State)
		}
		out.task = &task
	}
	out.logs = dedupeLogs(out.logs)
	for _, log := range out.logs {
		if out.task != nil && out.task.RunID != "" && log.RunID != out.task.RunID {
			return parsedCapture{}, fmt.Errorf("log run %s does not match task run %s", log.RunID, out.task.RunID)
		}
		if log.FailureText != "" && out.failureText == "" {
			out.failureText = log.FailureText
		}
	}
	if len(out.logs) == 0 && out.task == nil && out.statusFrames == 0 {
		return parsedCapture{}, fmt.Errorf("empty service read")
	}
	return out, nil
}

func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
