// Package config reads the session server's environment.
package config

import (
	"os"
	"strings"
	"time"
)

const (
	defaultAddr            = ":8080"
	defaultAdapter         = "dev"
	defaultProviderBaseURL = "https://api.openai.com/v1"
	defaultChunkDelay      = 40 * time.Millisecond
)

// Config is the process configuration. Secrets come from the environment only.
type Config struct {
	DatabaseURL     string
	HTTPAddr        string
	Adapter         string
	ProviderAPIKey  string
	ProviderBaseURL string
	MakezURL        string
	DevChunkDelay   time.Duration
}

// FromEnv loads configuration. An unset PHILO_MODEL_ADAPTER defaults to dev,
// which is the local compose default. Provider mode never falls back to dev.
func FromEnv() Config {
	cfg := Config{
		DatabaseURL:     strings.TrimSpace(os.Getenv("PHILO_DATABASE_URL")),
		HTTPAddr:        getenv("PHILO_HTTP_ADDR", defaultAddr),
		Adapter:         getenv("PHILO_MODEL_ADAPTER", defaultAdapter),
		ProviderAPIKey:  strings.TrimSpace(os.Getenv("PHILO_PROVIDER_API_KEY")),
		ProviderBaseURL: getenv("PHILO_PROVIDER_BASE_URL", defaultProviderBaseURL),
		MakezURL:        strings.TrimSpace(os.Getenv("PHILO_MAKEZ_URL")),
		DevChunkDelay:   defaultChunkDelay,
	}
	if raw := strings.TrimSpace(os.Getenv("PHILO_DEV_CHUNK_DELAY")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
			cfg.DevChunkDelay = d
		}
	}
	return cfg
}

// HealthAdapter is the adapter field on GET /v1/health.
// Provider mode without a key is unconfigured, not dev.
func (c Config) HealthAdapter() string {
	switch c.Adapter {
	case "dev":
		return "dev"
	case "provider":
		if c.ProviderAPIKey == "" {
			return "unconfigured"
		}
		return "provider"
	default:
		return "unconfigured"
	}
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
