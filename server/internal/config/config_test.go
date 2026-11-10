package config

import (
	"strings"
	"testing"
	"time"
)

func TestHealthAdapter(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "explicit fixture", cfg: Config{ExecutionFixture: ExecutionFixtureDev}, want: "dev"},
		{name: "adapter dev without fixture", cfg: Config{Adapter: "dev"}, want: "unavailable"},
		{name: "provider with key", cfg: Config{Adapter: "provider", ProviderAPIKey: "k"}, want: "unavailable"},
		{name: "provider without key", cfg: Config{Adapter: "provider"}, want: "unavailable"},
		{name: "unknown", cfg: Config{Adapter: "other"}, want: "unavailable"},
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
	t.Setenv("PHILO_EXECUTION_FIXTURE", "")
	t.Setenv("PHILO_AUTH_CREDENTIALS", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Adapter != "" {
		t.Fatalf("adapter = %q, want empty", cfg.Adapter)
	}
	if cfg.DevFixture() {
		t.Fatal("unset fixture selected dev")
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
	if cfg.HealthAdapter() != "unavailable" {
		t.Fatalf("health adapter = %q", cfg.HealthAdapter())
	}
}

func TestExplicitFixtureStaysDev(t *testing.T) {
	t.Setenv("PHILO_EXECUTION_FIXTURE", "dev")
	t.Setenv("PHILO_MODEL_ADAPTER", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DevFixture() || cfg.HealthAdapter() != "dev" {
		t.Fatalf("fixture = %q health = %q", cfg.ExecutionFixture, cfg.HealthAdapter())
	}
}

func TestLiveMakezBindingGate(t *testing.T) {
	if err := (Config{}).LiveMakez(); err != nil {
		t.Fatal(err)
	}
	if (Config{}).MakezTestBinding() {
		t.Fatal("empty config enabled a Makez call")
	}
	full := Config{
		MakezAPIURL:      "http://127.0.0.1:9",
		MakezAPIToken:    "machine-token-test",
		MakezWorkspaceID: "ws-test",
		MakezBinding:     TestMakezBinding,
	}
	if err := full.LiveMakez(); err != nil || !full.MakezTestBinding() {
		t.Fatal(err)
	}
	partial := full
	partial.MakezAPIToken = ""
	if err := partial.LiveMakez(); err == nil {
		t.Fatal("partial test binding was accepted")
	}
	production := full
	production.MakezBinding = "owner-workspace"
	if err := production.LiveMakez(); err == nil || !strings.Contains(err.Error(), "test-binding") {
		t.Fatal(err)
	}
}

func TestFromEnvProviderDoesNotBecomeDev(t *testing.T) {
	t.Setenv("PHILO_DATABASE_URL", "postgres://example")
	t.Setenv("PHILO_MODEL_ADAPTER", "provider")
	t.Setenv("PHILO_PROVIDER_API_KEY", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Adapter != "provider" {
		t.Fatalf("adapter = %q", cfg.Adapter)
	}
	if cfg.DevFixture() {
		t.Fatal("provider became the dev fixture")
	}
	if cfg.HealthAdapter() == "dev" {
		t.Fatalf("health = %q", cfg.HealthAdapter())
	}
}
