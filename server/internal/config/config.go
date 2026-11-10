// Package config reads the session server's environment.
package config

import (
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
		DevChunkDelay:    defaultChunkDelay,
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

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
