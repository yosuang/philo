// Package makez reports whether a configured Makez runtime answers a health check.
// A missing URL or a failed check is disconnected. This package never invents a success.
package makez

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Connector is one runtime the server can see.
type Connector struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Reason    string `json:"reason"`
}

// Status checks PHILO_MAKEZ_URL. client may be nil.
func Status(ctx context.Context, rawURL string, client *http.Client) Connector {
	out := Connector{ID: "makez", Name: "Makez"}
	if strings.TrimSpace(rawURL) == "" {
		out.Reason = "PHILO_MAKEZ_URL is not set"
		return out
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		out.Reason = "Makez health check failed: " + err.Error()
		return out
	}
	resp, err := client.Do(req)
	if err != nil {
		out.Reason = "Makez health check failed: " + err.Error()
		return out
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Reason = fmt.Sprintf("Makez health check returned HTTP %d", resp.StatusCode)
		return out
	}
	out.Connected = true
	out.Reason = "Makez health check succeeded"
	return out
}

// HealthValue is the makez field on GET /v1/health.
func HealthValue(c Connector) string {
	if c.Connected {
		return "connected"
	}
	return "disconnected"
}
