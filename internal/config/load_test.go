package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/config"
)

func TestLoadYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
# A comment, which JSON does not allow.
addr: 127.0.0.1:9999
workspace: ` + t.TempDir() + `
token: from-yaml
permission: deny

proxy:
  url: http://proxy:3128

agents:
  - id: devin
    command: devin
    args: [acp]
    api_key_env: DEVIN_API_KEY
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9999" || cfg.Token != "from-yaml" || cfg.Permission != "deny" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Proxy.URL != "http://proxy:3128" {
		t.Fatalf("proxy url = %q", cfg.Proxy.URL)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].APIKeyEnv != "DEVIN_API_KEY" {
		t.Fatalf("agents = %+v", cfg.Agents)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestLoadAcceptsAProxyURLString covers the shorthand: a bare string is the url
// of an otherwise empty proxy block, globally and per agent.
func TestLoadAcceptsAProxyURLString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
proxy: http://proxy:3128

agents:
  - id: devin
    command: devin
    proxy: socks5://127.0.0.1:1080
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Proxy.URL != "http://proxy:3128" {
		t.Fatalf("global proxy url = %q", cfg.Proxy.URL)
	}
	if cfg.Agents[0].Proxy == nil || cfg.Agents[0].Proxy.URL != "socks5://127.0.0.1:1080" {
		t.Fatalf("agent proxy = %+v", cfg.Agents[0].Proxy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestLoadProxyStringStillNeedsAScheme: the shorthand is convenience, not a
// licence to skip validation — a bare host:port is rejected, not guessed at.
func TestLoadProxyStringStillNeedsAScheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("proxy: 127.0.0.1:2080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected a scheme-less proxy url to be rejected")
	}
}

// TestLoadAcceptsJSON is the migration story: YAML is a superset of JSON, so an
// existing config keeps working through the same parser.
func TestLoadAcceptsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
  "addr": "127.0.0.1:9998",
  "token": "from-json",
  "agents": [{"id": "devin", "command": "devin", "args": ["acp"]}]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9998" || cfg.Token != "from-json" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadExpandsEnvironmentReferences(t *testing.T) {
	t.Setenv("ACP2API_TEST_PROXY", "socks5://from-env:1080")

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
proxy:
  url: ${ACP2API_TEST_PROXY}
  no_proxy: ${ACP2API_TEST_ABSENT:-localhost,127.0.0.1}
agents:
  - id: devin
    command: devin
    env:
      EXTRA: ${ACP2API_TEST_ABSENT:-fallback}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Proxy.URL != "socks5://from-env:1080" {
		t.Fatalf("proxy url = %q", cfg.Proxy.URL)
	}
	if cfg.Proxy.NoProxy != "localhost,127.0.0.1" {
		t.Fatalf("no_proxy = %q, the default should apply", cfg.Proxy.NoProxy)
	}
	if cfg.Agents[0].Env["EXTRA"] != "fallback" {
		t.Fatalf("nested substitution failed: %+v", cfg.Agents[0].Env)
	}
}

// TestLoadFailsOnAnUnsetReference is the deliberate difference from a plain
// ${VAR} expander: a typo must stop the process, not leave a silent empty value.
func TestLoadFailsOnAnUnsetReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("token: ${ACP2API_TEST_DEFINITELY_ABSENT}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an unset reference with no default to fail")
	}
	if !strings.Contains(err.Error(), "ACP2API_TEST_DEFINITELY_ABSENT") {
		t.Fatalf("the error should name the variable: %v", err)
	}
	if !strings.Contains(err.Error(), ":-") {
		t.Fatalf("the error should say how to allow empty: %v", err)
	}
}

func TestLoadAllowsAnExplicitlyEmptyReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("token: ${ACP2API_TEST_DEFINITELY_ABSENT:-}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "" {
		t.Fatalf("token = %q, want empty", cfg.Token)
	}
}

func TestLoadKeepsEnvironmentOverridingTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("token: from-file\naddr: 127.0.0.1:1111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvToken, "from-env")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "from-env" {
		t.Fatalf("token = %q, the environment should win", cfg.Token)
	}
	if cfg.Addr != "127.0.0.1:1111" {
		t.Fatalf("addr = %q, the file should still apply", cfg.Addr)
	}
}

// TestLoadIgnoresReferencesInComments covers a real trap: substitution runs on
// the raw text, so a commented-out example would otherwise demand that the
// variable be set.
func TestLoadIgnoresReferencesInComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
# token: ${ACP2API_TEST_DEFINITELY_ABSENT}
#   proxy:
#     url: ${ALSO_ABSENT:-}
token: real
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("a comment must not be expanded: %v", err)
	}
	if cfg.Token != "real" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("addr: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected malformed YAML to be rejected")
	}
}

func TestLoadReadsFilesystemMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
filesystem: none
agents:
  - id: devin
    command: devin
    args: [acp]
    filesystem: readonly
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Filesystem != agent.FilesystemNone {
		t.Fatalf("filesystem = %q, want %q", cfg.Filesystem, agent.FilesystemNone)
	}
	if cfg.Agents[0].Filesystem != agent.FilesystemReadOnly {
		t.Fatalf("agent filesystem = %q, want %q", cfg.Agents[0].Filesystem, agent.FilesystemReadOnly)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestLoadRejectsAnUnknownKey is what makes the rename safe. `read_only` no
// longer exists, and a configuration still carrying it must stop the process:
// ignoring it would start the gateway with the filesystem wide open while the
// operator believes it is shut.
func TestLoadRejectsAnUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("read_only: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected the removed read_only key to be rejected")
	}
	if !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("the error should name the offending key: %v", err)
	}
}

// TestShippedExampleConfigParses keeps config.example.yaml honest. It is what an
// operator copies to config.yaml, so a rename that updates the code but not the
// example hands them a file the strict parser rejects.
func TestShippedExampleConfigParses(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestLoadAcceptsAnEmptyFile: a file of nothing but comments is not a mistake,
// so the strict decoder must not turn the end of the document into an error.
func TestLoadAcceptsAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("# nothing configured yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Filesystem != agent.FilesystemFull {
		t.Fatalf("filesystem = %q, want the default", cfg.Filesystem)
	}
}

func TestLoadKeepsEnvironmentFilesystemOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("filesystem: full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvFilesystem, "none")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Filesystem != agent.FilesystemNone {
		t.Fatalf("filesystem = %q, the environment should win", cfg.Filesystem)
	}
}

func TestLoadReadsConversationHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("conversation_header: X-OpenWebUI-Chat-Id\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConversationHeader != "X-OpenWebUI-Chat-Id" {
		t.Fatalf("conversation_header = %q", cfg.ConversationHeader)
	}
}

// TestEmptyConversationHeaderDisablesIt: naming no header is how a deployment
// turns the mechanism off, so the default must not be filled back in — that
// would take away the off switch while looking like a harmless tidy-up.
func TestEmptyConversationHeaderDisablesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("conversation_header: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConversationHeader != "" {
		t.Fatalf("conversation_header = %q, want it to stay empty", cfg.ConversationHeader)
	}
}
