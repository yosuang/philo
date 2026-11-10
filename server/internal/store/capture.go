package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	_ "embed"
)

// Captured bytes from the Makez handoff at
// ebeb89e34a69e992692628a688d55949bfaaa595.
// They are a replay fixture for Philo. They are not a live Makez call.

//go:embed testdata/public-log.json
var capturedPublicLog []byte

//go:embed testdata/task-get.json
var capturedTaskGET []byte

//go:embed testdata/task-preparing.json
var capturedTaskPreparing []byte

//go:embed testdata/sse-after-0.txt
var capturedSSEAfter0 []byte

//go:embed testdata/sse-last-event-id-1.txt
var capturedSSELastEvent []byte

// CapturedFailureBytes are the raw files this slice replays.
// PublicLog is makez-philo-03 line 14. TaskGET is makez-philo-04 line 18.
// TaskPreparing is makez-philo-01 line 70. SSEAfter0 is makez-philo-03
// lines 34–39. SSELastEvent is makez-philo-03 lines 52–53.
type CapturedFailureBytes struct {
	PublicLog     []byte
	TaskGET       []byte
	TaskPreparing []byte
	SSEAfter0     []byte
	SSELastEvent  []byte
}

// CapturedNoKeyFailure returns copies of the embedded capture.
func CapturedNoKeyFailure() CapturedFailureBytes {
	return CapturedFailureBytes{
		PublicLog:     append([]byte(nil), capturedPublicLog...),
		TaskGET:       append([]byte(nil), capturedTaskGET...),
		TaskPreparing: append([]byte(nil), capturedTaskPreparing...),
		SSEAfter0:     append([]byte(nil), capturedSSEAfter0...),
		SSELastEvent:  append([]byte(nil), capturedSSELastEvent...),
	}
}

// TestBindingLabel is the only association label this projector accepts.
// It marks a test fixture. It is not a production owner-to-workspace identity.
const TestBindingLabel = "test-binding"

// Captured ids from section 6.8. This projector refuses any other task.
const (
	capturedWorkspaceID    = "api-test-88c8390626d97c5e675871cc"
	capturedTaskID         = "ddeda2f7e3423d3e40d43f69a126afe7"
	capturedRunID          = "5a1ac4ffa210a3ba5d805343e1b0b1a7"
	capturedConversationID = "conv-wire"
)

// TestBinding associates one Philo turn with the captured conversation id.
// OwnerID is the server owner already stored on that turn. It is not read
// from the Makez body and it is not the Makez workspace id.
type TestBinding struct {
	Label                  string
	OwnerID                string
	ConversationID         string
	TurnID                 string
	CapturedConversationID string
}

// CaptureInput is one replay of captured bytes.
// AuthoritativeGET is the raw GET /v1/tasks/:id body.
// A status frame inside SSE is not that body, even when the JSON matches.
type CaptureInput struct {
	Binding          TestBinding
	DurableLogs      []byte
	SSE              []byte
	AuthoritativeGET []byte
}

// SSEFrame is one Server-Sent Event. ID is the SSE id line.
// An empty ID means the frame had no id line. A JSON field named id is not ID.
type SSEFrame struct {
	ID    string
	Event string
	Data  []byte
}

// ParseSSE splits a captured event stream. A blank line ends a frame.
func ParseSSE(raw []byte) ([]SSEFrame, error) {
	var frames []SSEFrame
	var cur SSEFrame
	var data [][]byte
	flush := func() {
		if cur.Event == "" && cur.ID == "" && len(data) == 0 {
			return
		}
		if len(data) > 0 {
			cur.Data = bytes.Join(data, []byte("\n"))
		}
		frames = append(frames, cur)
		cur = SSEFrame{}
		data = nil
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			flush()
			continue
		}
		if line[0] == ':' {
			continue
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			return nil, fmt.Errorf("sse line has no colon")
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(name) {
		case "id":
			cur.ID = string(value)
		case "event":
			cur.Event = string(value)
		case "data":
			data = append(data, append([]byte(nil), value...))
		default:
			return nil, fmt.Errorf("unsupported sse field %q", name)
		}
	}
	flush()
	return frames, nil
}

type publicLog struct {
	Seq         int64
	RunID       string
	Stream      string
	Text        string
	Hash        string
	FailureText string
}

type taskView struct {
	ID             string
	WorkspaceID    string
	State          string
	Reason         string
	AgentID        string
	AgentVersion   string
	ConversationID string
	RunID          string
	RunState       string
	Cursor         string
	ExitCode       int
	Signal         string
	HasExit        bool
}

func (t taskView) capturedFailure() bool {
	return t.ID == capturedTaskID &&
		t.WorkspaceID == capturedWorkspaceID &&
		t.RunID == capturedRunID &&
		t.ConversationID == capturedConversationID &&
		t.AgentID == "pi" &&
		t.AgentVersion == "1" &&
		t.State == "FAILED" &&
		t.Reason == "AGENT_FAILED" &&
		t.RunState == "FAILED" &&
		t.HasExit &&
		t.ExitCode == 1 &&
		t.Signal == ""
}

type parsedCapture struct {
	logs         []publicLog
	task         *taskView
	statusFrames int
	failureText  string
}

func (p parsedCapture) failure() bool {
	return p.task != nil && p.task.capturedFailure()
}

func parseCapture(in CaptureInput) (parsedCapture, error) {
	var out parsedCapture
	if len(bytes.TrimSpace(in.DurableLogs)) > 0 {
		logs, err := parsePublicLogs(in.DurableLogs)
		if err != nil {
			return parsedCapture{}, err
		}
		out.logs = append(out.logs, logs...)
	}
	if len(bytes.TrimSpace(in.SSE)) > 0 {
		frames, err := ParseSSE(in.SSE)
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
				if frame.ID != strconv.FormatInt(log.Seq, 10) {
					return parsedCapture{}, fmt.Errorf("sse id %s is not public seq %d", frame.ID, log.Seq)
				}
				out.logs = append(out.logs, log)
			case "status":
				// The frame id is the replay cursor. This frame has none.
				// Its JSON id is the task id and is not applied here.
				out.statusFrames++
			default:
				return parsedCapture{}, fmt.Errorf("unsupported sse event %q", frame.Event)
			}
		}
	}
	if len(bytes.TrimSpace(in.AuthoritativeGET)) > 0 {
		task, err := parseTask(in.AuthoritativeGET)
		if err != nil {
			return parsedCapture{}, err
		}
		if task.ID != capturedTaskID || task.WorkspaceID != capturedWorkspaceID || task.RunID != capturedRunID {
			return parsedCapture{}, fmt.Errorf("task is not the captured no-key failure")
		}
		out.task = &task
	}
	out.logs = dedupeLogs(out.logs)
	for _, log := range out.logs {
		if out.task != nil && log.RunID != out.task.RunID {
			return parsedCapture{}, fmt.Errorf("log run %s does not match task run %s", log.RunID, out.task.RunID)
		}
		if log.FailureText != "" && out.failureText == "" {
			out.failureText = log.FailureText
		}
	}
	if len(out.logs) == 0 && out.task == nil && out.statusFrames == 0 {
		return parsedCapture{}, fmt.Errorf("empty capture")
	}
	return out, nil
}

func dedupeLogs(logs []publicLog) []publicLog {
	seen := map[string]string{}
	out := make([]publicLog, 0, len(logs))
	for _, log := range logs {
		key := log.RunID + ":" + strconv.FormatInt(log.Seq, 10)
		if prev, ok := seen[key]; ok {
			if prev != log.Hash {
				continue
			}
			continue
		}
		seen[key] = log.Hash
		out = append(out, log)
	}
	return out
}

func parsePublicLogs(raw []byte) ([]publicLog, error) {
	raw = bytes.TrimSpace(raw)
	var rows []logWire
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("public log: %w", err)
		}
	} else {
		var one logWire
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("public log: %w", err)
		}
		rows = []logWire{one}
	}
	out := make([]publicLog, 0, len(rows))
	for _, row := range rows {
		log, err := row.public()
		if err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	return out, nil
}

type logWire struct {
	Seq     int64  `json:"seq"`
	RunID   string `json:"run_id"`
	Type    string `json:"type"`
	Payload struct {
		Text   string `json:"text"`
		Stream string `json:"stream"`
	} `json:"payload"`
	CreatedAt string `json:"created_at"`
}

func (w logWire) public() (publicLog, error) {
	if w.Type != "log" {
		return publicLog{}, fmt.Errorf("public event type %q is not a log", w.Type)
	}
	if w.Seq <= 0 || w.RunID == "" || w.Payload.Stream == "" {
		return publicLog{}, fmt.Errorf("public log is missing seq, run_id, or stream")
	}
	sum := sha256.Sum256([]byte(w.Payload.Stream + "\n" + w.Payload.Text))
	return publicLog{
		Seq:         w.Seq,
		RunID:       w.RunID,
		Stream:      w.Payload.Stream,
		Text:        w.Payload.Text,
		Hash:        hex.EncodeToString(sum[:]),
		FailureText: piFailureText(w.Payload.Text),
	}, nil
}

func piFailureText(text string) string {
	var line struct {
		Kind   string `json:"kind"`
		Record struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		} `json:"record"`
	}
	if err := json.Unmarshal([]byte(text), &line); err != nil {
		return ""
	}
	if line.Kind == "pi" && !line.Record.Success && line.Record.Error != "" {
		return line.Record.Error
	}
	return ""
}

func parseTask(raw []byte) (taskView, error) {
	var wire struct {
		ID           string `json:"id"`
		WorkspaceID  string `json:"workspace_id"`
		AgentID      string `json:"agent_id"`
		AgentVersion string `json:"agent_version"`
		State        string `json:"state"`
		Reason       string `json:"reason"`
		Config       struct {
			Execution struct {
				ConversationID string `json:"conversation_id"`
			} `json:"execution"`
		} `json:"config"`
		Run struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Cursor string `json:"cursor"`
			Exit   *struct {
				Code   int    `json:"Code"`
				Signal string `json:"Signal"`
			} `json:"exit"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return taskView{}, fmt.Errorf("task: %w", err)
	}
	view := taskView{
		ID:             wire.ID,
		WorkspaceID:    wire.WorkspaceID,
		State:          wire.State,
		Reason:         wire.Reason,
		AgentID:        wire.AgentID,
		AgentVersion:   wire.AgentVersion,
		ConversationID: wire.Config.Execution.ConversationID,
		RunID:          wire.Run.ID,
		RunState:       wire.Run.State,
		Cursor:         wire.Run.Cursor,
	}
	if wire.Run.Exit != nil {
		view.HasExit = true
		view.ExitCode = wire.Run.Exit.Code
		view.Signal = wire.Run.Exit.Signal
	}
	return view, nil
}
