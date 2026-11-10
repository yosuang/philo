// Package engine turns persisted messages into streamed replies, cancels, retries,
// and the in-server approval task.
package engine

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/yosuang/philo/server/internal/adapter"
	"github.com/yosuang/philo/server/internal/events"
	"github.com/yosuang/philo/server/internal/hub"
	"github.com/yosuang/philo/server/internal/store"
)

const taskDelay = 150 * time.Millisecond

// errStopped means the row is no longer streaming, usually because it was canceled.
var errStopped = errors.New("message is no longer streaming")

// Engine runs generations for one process.
type Engine struct {
	store *store.Store
	ad    adapter.Adapter
	hub   *hub.Hub

	mu          sync.Mutex
	runs        map[string]context.CancelFunc
	fixture     bool
	finishTasks bool
}

// New builds an engine. ad is the isolated fixture adapter when fixture is set.
// finishTasks is only the dev fixture. The worker fixture leaves the task running.
// Production does not call the adapter and does not complete tasks from this process.
func New(st *store.Store, ad adapter.Adapter, h *hub.Hub, fixture, finishTasks bool) *Engine {
	return &Engine{
		store:       st,
		ad:          ad,
		hub:         h,
		runs:        map[string]context.CancelFunc{},
		fixture:     fixture,
		finishTasks: finishTasks,
	}
}

// PostMessage persists a user turn and starts the assistant stream.
func (e *Engine) PostMessage(ctx context.Context, sessionID, content string) (store.Turn, error) {
	turn, err := e.store.BeginTurn(ctx, sessionID, content, e.ad.Name())
	if err != nil {
		return store.Turn{}, err
	}
	e.publishSession(turn.Session)
	e.start(turn.Assistant.ID)
	return turn, nil
}

// Retry regenerates an assistant message that is error or canceled.
func (e *Engine) Retry(ctx context.Context, messageID string) (store.Message, error) {
	msg, sess, err := e.store.BeginRetry(ctx, messageID)
	if err != nil {
		return store.Message{}, err
	}
	e.publishSession(sess)
	e.start(msg.ID)
	return msg, nil
}

// Cancel stops the in-flight reply for a session.
// The returned message has status canceled when the cancel lands.
func (e *Engine) Cancel(ctx context.Context, sessionID string) (store.Message, error) {
	msg, err := e.store.StreamingMessage(ctx, sessionID)
	if err != nil {
		return store.Message{}, err
	}
	e.cancelRun(msg.ID)

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		cur, err := e.store.GetMessage(ctx, msg.ID)
		if err != nil {
			return store.Message{}, err
		}
		if cur.Status == "canceled" {
			return cur, nil
		}
		if cur.Status != "streaming" {
			return store.Message{}, store.ErrNotFound
		}
		select {
		case <-ctx.Done():
			return store.Message{}, ctx.Err()
		case <-deadline.C:
			return e.forceCancel(msg.ID)
		case <-ticker.C:
		}
	}
}

// Decide records approve or reject. Approve stores a local philo-server task.
// The dev fixture may later store a result. That result does not change the
// card. The worker fixture leaves the task running. Production does not
// complete the task. Reject creates no task. This does not cancel an in-flight task.
func (e *Engine) Decide(ctx context.Context, approvalID, decision string) (store.Approval, error) {
	res, err := e.store.Decide(ctx, approvalID, decision)
	if err != nil {
		return store.Approval{}, err
	}
	e.publishApproval("approval.updated", res.Approval)
	if res.Task != nil {
		e.publishTask(*res.Task)
		if e.finishTasks {
			go e.completeTask(res.Task.ID, res.Approval.Summary)
		}
	}
	return res.Approval, nil
}

// Stop cancels every in-flight generation.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, cancel := range e.runs {
		cancel()
		delete(e.runs, id)
	}
}

// StartFixture starts the isolated dev adapter for one assistant row.
// Production calls do not use it.
func (e *Engine) StartFixture(messageID string) {
	if e == nil || !e.fixture || e.ad == nil {
		return
	}
	e.start(messageID)
}

func (e *Engine) start(messageID string) {
	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if prev, ok := e.runs[messageID]; ok {
		prev()
	}
	e.runs[messageID] = cancel
	e.mu.Unlock()
	go e.generate(ctx, messageID)
}

func (e *Engine) cancelRun(messageID string) {
	e.mu.Lock()
	cancel, ok := e.runs[messageID]
	e.mu.Unlock()
	if ok {
		cancel()
	}
}

func (e *Engine) release(messageID string) {
	e.mu.Lock()
	delete(e.runs, messageID)
	e.mu.Unlock()
}

func (e *Engine) generate(ctx context.Context, messageID string) {
	defer e.release(messageID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("generate %s panic: %v", messageID, rec)
			e.fail(messageID, "", &adapter.Failure{Code: "internal", Message: "generation panicked"})
		}
	}()

	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	msg, err := e.store.GetMessage(dbCtx, messageID)
	cancel()
	if err != nil {
		log.Printf("generate load %s: %v", messageID, err)
		return
	}
	if msg.Status != "streaming" || e.ad == nil {
		return
	}
	if msg.ReplyToID == "" {
		e.fail(messageID, msg.SessionID, &adapter.Failure{Code: "internal", Message: "assistant message is missing its user message"})
		return
	}

	dbCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	user, err := e.store.GetMessage(dbCtx, msg.ReplyToID)
	cancel()
	if err != nil {
		e.fail(messageID, msg.SessionID, err)
		return
	}
	dbCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	sess, err := e.store.GetSession(dbCtx, msg.SessionID)
	cancel()
	if err != nil {
		e.fail(messageID, msg.SessionID, err)
		return
	}
	dbCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	agent, err := e.store.GetAgent(dbCtx, sess.AgentID)
	cancel()
	if err != nil {
		e.fail(messageID, msg.SessionID, err)
		return
	}
	if ctx.Err() != nil {
		e.markCanceled(messageID)
		return
	}

	res, err := e.ad.Stream(ctx, adapter.Request{
		ModelID:      agent.ModelID,
		Instructions: agent.Instructions,
		UserContent:  user.Content,
	}, func(text string) error {
		if text == "" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		writeCtx, writeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		ok, err := e.store.AppendContent(writeCtx, messageID, text)
		writeCancel()
		if err != nil {
			return err
		}
		if !ok {
			return errStopped
		}
		e.hub.Publish(msg.SessionID, events.Delta{
			Type:      "message.delta",
			MessageID: messageID,
			SessionID: msg.SessionID,
			Text:      text,
		})
		return nil
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, errStopped) || errors.Is(ctx.Err(), context.Canceled) {
		e.markCanceled(messageID)
		return
	}
	if err != nil {
		e.fail(messageID, msg.SessionID, err)
		return
	}

	kind, summary := "", ""
	if res.Approval != nil {
		kind = res.Approval.Kind
		summary = res.Approval.Summary
	}
	dbCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	finished, approval, ok, err := e.store.FinishStreaming(dbCtx, messageID, kind, summary)
	cancel()
	if err != nil {
		log.Printf("finish %s: %v", messageID, err)
		e.fail(messageID, msg.SessionID, err)
		return
	}
	if !ok {
		return
	}
	if approval != nil {
		e.publishApproval("approval.requested", *approval)
	}
	e.hub.Publish(finished.SessionID, events.MessageRef{
		Type:      "message.completed",
		MessageID: finished.ID,
		SessionID: finished.SessionID,
	})
}

func (e *Engine) markCanceled(messageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, ok, err := e.store.MarkCanceled(ctx, messageID)
	if err != nil {
		log.Printf("cancel message %s: %v", messageID, err)
		return
	}
	if ok {
		e.publishCanceled(msg)
	}
}

func (e *Engine) forceCancel(messageID string) (store.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, ok, err := e.store.MarkCanceled(ctx, messageID)
	if err != nil {
		return store.Message{}, err
	}
	if ok {
		e.publishCanceled(msg)
		return msg, nil
	}
	cur, err := e.store.GetMessage(ctx, messageID)
	if err != nil {
		return store.Message{}, err
	}
	if cur.Status == "canceled" {
		return cur, nil
	}
	return store.Message{}, store.ErrNotFound
}

func (e *Engine) fail(messageID, sessionID string, cause error) {
	code := "internal"
	message := "generation failed"
	var failure *adapter.Failure
	if errors.As(cause, &failure) {
		if failure.Code != "" {
			code = failure.Code
		}
		if failure.Message != "" {
			message = failure.Message
		} else if failure.Code != "" {
			message = failure.Code
		}
	} else {
		log.Printf("generate %s: %v", messageID, cause)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, ok, err := e.store.MarkError(ctx, messageID, code, message)
	if err != nil {
		log.Printf("mark error %s: %v", messageID, err)
		return
	}
	if !ok {
		return
	}
	if sessionID == "" {
		sessionID = msg.SessionID
	}
	e.hub.Publish(sessionID, events.MessageError{
		Type:      "message.error",
		MessageID: msg.ID,
		SessionID: msg.SessionID,
		Message:   message,
		Code:      code,
	})
}

func (e *Engine) completeTask(taskID, summary string) {
	timer := time.NewTimer(taskDelay)
	defer timer.Stop()
	<-timer.C
	result := "philo-server stored result for draft action: " + summary
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	task, err := e.store.RecordWorkerResult(ctx, taskID, store.WorkerClaim{Text: result})
	if err != nil {
		log.Printf("complete task %s: %v", taskID, err)
		return
	}
	e.publishTask(task)
}

func (e *Engine) publishSession(sess store.Session) {
	ev, err := events.WithType("session.updated", sess)
	if err != nil {
		log.Printf("session event: %v", err)
		return
	}
	e.hub.Publish(sess.ID, ev)
}

func (e *Engine) publishApproval(eventType string, approval store.Approval) {
	ev, err := events.WithType(eventType, approval)
	if err != nil {
		log.Printf("approval event: %v", err)
		return
	}
	e.hub.Publish(approval.SessionID, ev)
}

func (e *Engine) publishTask(task store.Task) {
	ev, err := events.WithType("task.updated", task)
	if err != nil {
		log.Printf("task event: %v", err)
		return
	}
	e.hub.Publish(task.SessionID, ev)
}

func (e *Engine) publishCanceled(msg store.Message) {
	e.hub.Publish(msg.SessionID, events.MessageRef{
		Type:      "message.canceled",
		MessageID: msg.ID,
		SessionID: msg.SessionID,
	})
}
