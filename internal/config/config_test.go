package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/config"
)

func TestDefaultIsLoopbackAndValid(t *testing.T) {
	cfg := config.Default()
	if cfg.Addr != config.DefaultAddr {
		t.Fatalf("addr = %q", cfg.Addr)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the default configuration must validate: %v", err)
	}
}

func TestValidateRefusesPublicBindWithoutToken(t *testing.T) {
	cfg := config.Default()
	cfg.Addr = "0.0.0.0:8720"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected a public bind without a token to be refused")
	}

	cfg.Token = "s3cret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a public bind with a token must validate: %v", err)
	}
}

func TestValidateRejectsUnknownPermission(t *testing.T) {
	cfg := config.Default()
	cfg.Permission = "maybe"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an unknown permission policy to be refused")
	}
}

func TestValidateRejectsAgentWithoutCommand(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{{ID: "broken"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an agent without a command to be refused")
	}
}

func TestLoadMergesFileThenEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"addr": "127.0.0.1:9999",
		"workspace": "` + t.TempDir() + `",
		"token": "from-file",
		"agents": [{"id": "custom", "command": "/bin/echo", "args": ["acp"]}]
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(config.EnvToken, "from-env")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9999" {
		t.Fatalf("addr = %q", cfg.Addr)
	}
	if cfg.Token != "from-env" {
		t.Fatalf("token = %q, want the environment to win over the file", cfg.Token)
	}
	if !filepath.IsAbs(cfg.Workspace) {
		t.Fatalf("workspace = %q, want an absolute path", cfg.Workspace)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRegistryOverridesBuiltinAndAddsCustom(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{
		{ID: "devin", Command: "devin-dev", Args: []string{"acp", "--verbose"}},
		{ID: "custom", Name: "Custom", Command: "/bin/echo"},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}

	devin, ok := registry.Get("devin")
	if !ok || devin.Command != "devin-dev" {
		t.Fatalf("devin = %+v, ok=%v", devin, ok)
	}
	if devin.Name != "Devin" {
		t.Fatalf("an override must keep the built-in display name, got %q", devin.Name)
	}

	custom, ok := registry.Get("custom")
	if !ok || custom.Command != "/bin/echo" {
		t.Fatalf("custom = %+v, ok=%v", custom, ok)
	}

	// The other built-ins survive.
	if _, ok := registry.Get("opencode"); !ok {
		t.Fatal("expected the built-in opencode agent to remain registered")
	}
}

func TestDurationAccessors(t *testing.T) {
	cfg := config.Default()
	if got := cfg.RequestTimeout(); got != config.DefaultRequestTimeout {
		t.Fatalf("RequestTimeout = %v", got)
	}
	if got := cfg.SessionTTL(); got != config.DefaultSessionTTL {
		t.Fatalf("SessionTTL = %v", got)
	}

	cfg.SessionTTLSeconds = -1
	if got := cfg.SessionTTL(); got != 0 {
		t.Fatalf("a negative TTL must disable reaping, got %v", got)
	}

	cfg.RequestTimeoutSeconds = 5
	if got := cfg.RequestTimeout(); got != 5*time.Second {
		t.Fatalf("RequestTimeout = %v", got)
	}
}
