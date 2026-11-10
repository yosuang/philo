package makez

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var errIncomplete = errors.New("incomplete makez client")

// Client calls the existing Makez task routes.
// It does not send an owner id and it does not treat a status frame as the terminal fact.
type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTP        *http.Client
}

// NewClient builds a client for one test workspace.
// baseURL is the API origin, not the health-probe URL.
func NewClient(baseURL, token, workspace string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	workspace = strings.TrimSpace(workspace)
	if baseURL == "" || token == "" || workspace == "" {
		return nil, errIncomplete
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("PHILO_MAKEZ_API_URL is not an absolute URL")
	}
	return &Client{
		BaseURL:     baseURL,
		Token:       token,
		WorkspaceID: workspace,
		HTTP:        &http.Client{},
	}, nil
}

// Submitted is the task id, and the run id when the response has one.
type Submitted struct {
	Status      int
	TaskID      string
	RunID       string
	WorkspaceID string
	Raw         []byte
}

type submitPayload struct {
	Prompt         string          `json:"prompt"`
	Config         json.RawMessage `json:"config"`
	AgentID        string          `json:"agent_id"`
	AgentVersion   string          `json:"agent_version"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

// Submit posts one pi@1 task. The body has no owner id.
func (c *Client) Submit(ctx context.Context, idempotencyKey, prompt string, config []byte) (Submitted, error) {
	body, err := json.Marshal(submitPayload{
		Prompt:         prompt,
		Config:         config,
		AgentID:        AgentID,
		AgentVersion:   AgentVersion,
		TimeoutSeconds: TaskTimeoutSeconds,
	})
	if err != nil {
		return Submitted{}, err
	}
	if bytes.Contains(body, []byte(`"owner_id"`)) {
		return Submitted{}, fmt.Errorf("submit body must not carry owner_id")
	}
	req, err := c.request(ctx, http.MethodPost, "/v1/tasks", body)
	if err != nil {
		return Submitted{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	status, raw, err := c.do(req)
	if err != nil {
		return Submitted{}, err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return Submitted{}, fmt.Errorf("makez submit returned HTTP %d", status)
	}
	return c.submitted(status, raw)
}

// Get reads the authoritative task record.
func (c *Client) Get(ctx context.Context, taskID string) ([]byte, error) {
	req, err := c.request(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("makez get returned HTTP %d", status)
	}
	return raw, nil
}

// Events reads the public event stream after the stored public seq.
// after 0 sends no cursor. A positive after sends Last-Event-ID and no after query.
// Makez lets Last-Event-ID win over after, so the client does not send both.
func (c *Client) Events(ctx context.Context, taskID string, after int64) ([]byte, error) {
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/events"
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if after > 0 {
		req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", after))
	}
	status, raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("makez events returned HTTP %d", status)
	}
	return raw, nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Makez-Workspace-ID", c.WorkspaceID)
	return req, nil
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *Client) submitted(status int, raw []byte) (Submitted, error) {
	var wire struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
		Run         *struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Submitted{}, fmt.Errorf("makez submit: %w", err)
	}
	if wire.ID == "" || wire.WorkspaceID == "" {
		return Submitted{}, fmt.Errorf("makez submit did not return a task")
	}
	if wire.WorkspaceID != c.WorkspaceID {
		return Submitted{}, fmt.Errorf("makez submit workspace does not match the test binding")
	}
	out := Submitted{Status: status, TaskID: wire.ID, WorkspaceID: wire.WorkspaceID, Raw: raw}
	if wire.Run != nil {
		out.RunID = wire.Run.ID
	}
	return out, nil
}
