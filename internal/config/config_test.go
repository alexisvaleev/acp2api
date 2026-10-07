package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quonaro/acp2api/internal/agent"
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

// TestDefaultFilesystemIsFull: the gateway has always given agents the
// filesystem, and nothing about the mode should change that by omission.
func TestDefaultFilesystemIsFull(t *testing.T) {
	if got := config.Default().Filesystem; got != agent.FilesystemFull {
		t.Fatalf("filesystem = %q, want %q", got, agent.FilesystemFull)
	}
}

func TestValidateRejectsUnknownFilesystem(t *testing.T) {
	cfg := config.Default()
	cfg.Filesystem = "read-only"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an unknown filesystem mode to be refused")
	}
}

func TestValidateRejectsUnknownAgentFilesystem(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{{ID: "x", Command: "x", Filesystem: "read-only"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an unknown per-agent filesystem mode to be refused")
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
