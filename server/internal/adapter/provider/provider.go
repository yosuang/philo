// Package provider calls an OpenAI-compatible chat completions API.
// It does not import the dev adapter and never falls back to it.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yosuang/philo/server/internal/adapter"
)

const name = "provider"

// Adapter is the real-provider stream. A missing key fails the generation.
type Adapter struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// New returns a provider adapter. apiKey may be empty; Stream then fails
// with provider_not_configured.
func New(apiKey, baseURL string) *Adapter {
	return &Adapter{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{},
	}
}

func (a *Adapter) Name() string { return name }

// Stream calls the provider. /draft is ordinary user text here. This adapter
// does not create approvals and does not invent a local reply.
func (a *Adapter) Stream(ctx context.Context, req adapter.Request, emit func(text string) error) (adapter.Result, error) {
	if a.apiKey == "" {
		return adapter.Result{}, &adapter.Failure{
			Code:    "provider_not_configured",
			Message: "PHILO_PROVIDER_API_KEY is not set. The provider adapter does not fall back to the dev adapter.",
		}
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return adapter.Result{}, &adapter.Failure{
			Code:    "provider_error",
			Message: "agent modelId is empty",
		}
	}
	if a.baseURL == "" {
		return adapter.Result{}, &adapter.Failure{
			Code:    "provider_error",
			Message: "PHILO_PROVIDER_BASE_URL is empty",
		}
	}

	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(req.Instructions) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.Instructions})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.UserContent})
	body, err := json.Marshal(map[string]any{
		"model":    req.ModelID,
		"stream":   true,
		"messages": messages,
	})
	if err != nil {
		return adapter.Result{}, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: err.Error()}
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("User-Agent", "philo-session-server/phase1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return adapter.Result{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: "provider request timed out"}
		}
		return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		text := strings.TrimSpace(string(msg))
		if text == "" {
			text = resp.Status
		}
		return adapter.Result{}, &adapter.Failure{
			Code:    "provider_error",
			Message: fmt.Sprintf("provider returned HTTP %d: %s", resp.StatusCode, text),
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return adapter.Result{}, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return adapter.Result{}, nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: "invalid provider stream"}
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: chunk.Error.Message}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		text := chunk.Choices[0].Delta.Content
		if text == "" {
			continue
		}
		if err := emit(text); err != nil {
			return adapter.Result{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return adapter.Result{}, context.Canceled
		}
		return adapter.Result{}, &adapter.Failure{Code: "provider_error", Message: err.Error()}
	}
	return adapter.Result{}, nil
}
