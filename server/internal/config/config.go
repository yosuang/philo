// Package config reads the session server's environment.
package config

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/yosuang/philo/server/internal/auth"
)

const (
	defaultAddr            = ":8080"
	defaultProviderBaseURL = "https://api.openai.com/v1"
	defaultChunkDelay      = 40 * time.Millisecond
	// ExecutionFixtureDev is the only value that runs the isolated dev adapter.
	ExecutionFixtureDev = "dev"
	// TestMakezBinding is the only label that may call Makez.
	// It is not a production owner-to-workspace identity.
	TestMakezBinding = "test-binding"
)

// Config is the process configuration. Secrets come from the environment only.
type Config struct {
	DatabaseURL      string
	HTTPAddr         string
	Adapter          string
	ExecutionFixture string
	BusinessFixture  bool
	Credentials      []auth.Credential
	ProviderAPIKey   string
	ProviderBaseURL  string
	MakezURL         string
	MakezAPIURL      string
	MakezAPIToken    string
	MakezWorkspaceID string
	MakezBinding     string
	DevChunkDelay    time.Duration
}

// FromEnv loads configuration. An unset adapter does not select dev.
// PHILO_EXECUTION_FIXTURE=dev is the explicit isolated fixture.
// Provider settings do not become an execution path.
func FromEnv() (Config, error) {
	creds, err := auth.ParseCredentials(os.Getenv("PHILO_AUTH_CREDENTIALS"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		DatabaseURL:      strings.TrimSpace(os.Getenv("PHILO_DATABASE_URL")),
		HTTPAddr:         getenv("PHILO_HTTP_ADDR", defaultAddr),
		Adapter:          strings.TrimSpace(os.Getenv("PHILO_MODEL_ADAPTER")),
		ExecutionFixture: strings.TrimSpace(os.Getenv("PHILO_EXECUTION_FIXTURE")),
		BusinessFixture:  strings.TrimSpace(os.Getenv("PHILO_BUSINESS_FIXTURE")) == "fixture-business-source",
		Credentials:      creds,
		ProviderAPIKey:   strings.TrimSpace(os.Getenv("PHILO_PROVIDER_API_KEY")),
		ProviderBaseURL:  getenv("PHILO_PROVIDER_BASE_URL", defaultProviderBaseURL),
		MakezURL:         strings.TrimSpace(os.Getenv("PHILO_MAKEZ_URL")),
		MakezAPIURL:      strings.TrimSpace(os.Getenv("PHILO_MAKEZ_API_URL")),
		MakezAPIToken:    strings.TrimSpace(os.Getenv("PHILO_MAKEZ_API_TOKEN")),
		MakezWorkspaceID: strings.TrimSpace(os.Getenv("PHILO_MAKEZ_WORKSPACE_ID")),
		MakezBinding:     strings.TrimSpace(os.Getenv("PHILO_MAKEZ_BINDING")),
		DevChunkDelay:    defaultChunkDelay,
	}
	if err := cfg.LiveMakez(); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("PHILO_DEV_CHUNK_DELAY")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			cfg.DevChunkDelay = d
		}
	}
	return cfg, nil
}

// DevFixture reports whether this process opted into the isolated dev adapter.
func (c Config) DevFixture() bool {
	return c.ExecutionFixture == ExecutionFixtureDev
}

// HealthAdapter is the adapter field on GET /v1/health.
// The production value is unavailable. dev is only the explicit fixture.
func (c Config) HealthAdapter() string {
	if c.DevFixture() {
		return "dev"
	}
	return "unavailable"
}

// LiveMakez rejects a partial client and any binding other than the test label.
// An empty client is disabled. Production owner-to-workspace binding is not configured.
func (c Config) LiveMakez() error {
	n := 0
	if c.MakezAPIURL != "" {
		n++
	}
	if c.MakezAPIToken != "" {
		n++
	}
	if c.MakezWorkspaceID != "" {
		n++
	}
	if c.MakezBinding != "" {
		n++
	}
	if n == 0 {
		return nil
	}
	if c.MakezBinding != TestMakezBinding {
		return errors.New("PHILO_MAKEZ_BINDING must be test-binding; a production owner-to-workspace binding is not configured")
	}
	if c.MakezAPIURL == "" || c.MakezAPIToken == "" || c.MakezWorkspaceID == "" {
		return errors.New("a test Makez binding needs PHILO_MAKEZ_API_URL, PHILO_MAKEZ_API_TOKEN, and PHILO_MAKEZ_WORKSPACE_ID")
	}
	return nil
}

// MakezTestBinding reports whether this process may call Makez under the test label.
func (c Config) MakezTestBinding() bool {
	return c.LiveMakez() == nil && c.MakezBinding == TestMakezBinding
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
