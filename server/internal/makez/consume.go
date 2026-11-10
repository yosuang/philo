package makez

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yosuang/philo/server/internal/store"
)

// ConsumeResult is one association and one read of the public routes.
// LocalReceiptAcked is Philo's own receipt column. It is not a Makez ack.
type ConsumeResult struct {
	Submitted bool
	TaskID    string
	RunID     string
	Project   store.ProjectResult
}

// ConsumeNoKey submits pi@1 when the turn has no task yet, then reads the
// public log and the authoritative GET. A status frame is not passed to the
// projector as the task body. The GET is. Restart reads the stored public
// seq and sends it as Last-Event-ID.
func ConsumeNoKey(ctx context.Context, st *store.Store, client *Client, binding store.LiveBinding) (ConsumeResult, error) {
	if st == nil || client == nil {
		return ConsumeResult{}, errIncomplete
	}
	if client.WorkspaceID != binding.WorkspaceID {
		return ConsumeResult{}, fmt.Errorf("client workspace does not match the test binding")
	}
	link, found, err := st.ServiceTask(ctx, binding)
	if err != nil {
		return ConsumeResult{}, err
	}
	var result ConsumeResult
	if !found {
		prompt, err := st.TurnPrompt(ctx, binding)
		if err != nil {
			return ConsumeResult{}, err
		}
		config, err := executionConfig(binding.BotID, binding.ConversationID)
		if err != nil {
			return ConsumeResult{}, err
		}
		key, err := SubmitKey(binding.TurnID)
		if err != nil {
			return ConsumeResult{}, err
		}
		submitted, err := client.Submit(ctx, key, prompt, config)
		if err != nil {
			return ConsumeResult{}, err
		}
		link = store.ServiceLink{WorkspaceID: submitted.WorkspaceID, TaskID: submitted.TaskID, RunID: submitted.RunID}
		if err := st.LinkServiceTask(ctx, binding, link); err != nil {
			return ConsumeResult{}, err
		}
		result.Submitted = true
	}
	result.TaskID = link.TaskID
	return readLinked(ctx, st, client, binding, link, result, false)
}

// ResumeLinked reads the public log from the stored seq for a task the turn
// already has. It does not submit a task. A missing link is not a new submit.
func ResumeLinked(ctx context.Context, st *store.Store, client *Client, binding store.LiveBinding) (ConsumeResult, error) {
	if st == nil || client == nil {
		return ConsumeResult{}, errIncomplete
	}
	if client.WorkspaceID != binding.WorkspaceID {
		return ConsumeResult{}, fmt.Errorf("client workspace does not match the test binding")
	}
	link, found, err := st.ServiceTask(ctx, binding)
	if err != nil {
		return ConsumeResult{}, err
	}
	if !found || link.TaskID == "" || link.RunID == "" {
		return ConsumeResult{}, fmt.Errorf("startup resume has no linked task")
	}
	return readLinked(ctx, st, client, binding, link, ConsumeResult{TaskID: link.TaskID, RunID: link.RunID}, true)
}

func readLinked(ctx context.Context, st *store.Store, client *Client, binding store.LiveBinding, link store.ServiceLink, result ConsumeResult, surfaceLogs bool) (ConsumeResult, error) {
	after, err := st.AppliedPublicSeq(ctx, link)
	if err != nil {
		return result, err
	}
	events, err := client.Events(ctx, link.TaskID, after)
	if err != nil {
		return result, err
	}
	// The status frame, if present, is inside events. It is not the GET body.
	body, err := client.Get(ctx, link.TaskID)
	if err != nil {
		return result, err
	}
	got, err := decodeTask(body)
	if err != nil {
		return result, err
	}
	if got.TaskID != link.TaskID || got.WorkspaceID != binding.WorkspaceID {
		return result, fmt.Errorf("task GET does not match the linked task")
	}
	if got.RunID != "" && got.RunID != link.RunID {
		link.RunID = got.RunID
		if err := st.LinkServiceTask(ctx, binding, link); err != nil {
			return result, err
		}
	}
	result.RunID = link.RunID
	projected, err := st.ProjectServiceRead(ctx, binding, store.ServiceRead{
		SSE:              events,
		AuthoritativeGET: body,
		SurfaceLogs:      surfaceLogs,
	}, store.FaultNone)
	if err != nil {
		return result, err
	}
	result.Project = projected
	return result, nil
}

func decodeTask(raw []byte) (store.ServiceLink, error) {
	var wire struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
		Run         *struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return store.ServiceLink{}, err
	}
	link := store.ServiceLink{WorkspaceID: wire.WorkspaceID, TaskID: wire.ID}
	if wire.Run != nil {
		link.RunID = wire.Run.ID
	}
	if link.TaskID == "" || link.WorkspaceID == "" {
		return store.ServiceLink{}, fmt.Errorf("task GET has no id")
	}
	return link, nil
}
