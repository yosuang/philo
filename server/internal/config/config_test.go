package config

import (
	"testing"
	"time"
)

func TestHealthAdapter(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "dev", cfg: Config{Adapter: "dev"}, want: "dev"},
		{name: "provider with key", cfg: Config{Adapter: "provider", ProviderAPIKey: "k"}, want: "provider"},
		{name: "provider without key", cfg: Config{Adapter: "provider"}, want: "unconfigured"},
		{name: "unknown", cfg: Config{Adapter: "other"}, want: "unconfigured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.HealthAdapter(); got != tc.want {
				t.Fatalf("HealthAdapter() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PHILO_DATABASE_URL", "postgres://example")
	t.Setenv("PHILO_HTTP_ADDR", "")
	t.Setenv("PHILO_MODEL_ADAPTER", "")
	t.Setenv("PHILO_PROVIDER_API_KEY", "")
	t.Setenv("PHILO_PROVIDER_BASE_URL", "")
	t.Setenv("PHILO_MAKEZ_URL", "")
	t.Setenv("PHILO_DEV_CHUNK_DELAY", "")

	cfg := FromEnv()
	if cfg.Adapter != "dev" {
		t.Fatalf("adapter = %q, want dev", cfg.Adapter)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("addr = %q", cfg.HTTPAddr)
	}
	if cfg.ProviderBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base url = %q", cfg.ProviderBaseURL)
	}
	if cfg.DevChunkDelay != 40*time.Millisecond {
		t.Fatalf("delay = %s", cfg.DevChunkDelay)
	}
	if cfg.HealthAdapter() != "dev" {
		t.Fatalf("health adapter = %q", cfg.HealthAdapter())
	}
}

func TestFromEnvProviderDoesNotBecomeDev(t *testing.T) {
	t.Setenv("PHILO_DATABASE_URL", "postgres://example")
	t.Setenv("PHILO_MODEL_ADAPTER", "provider")
	t.Setenv("PHILO_PROVIDER_API_KEY", "")
	cfg := FromEnv()
	if cfg.Adapter != "provider" {
		t.Fatalf("adapter = %q", cfg.Adapter)
	}
	if cfg.HealthAdapter() != "unconfigured" {
		t.Fatalf("health = %q, want unconfigured", cfg.HealthAdapter())
	}
}
