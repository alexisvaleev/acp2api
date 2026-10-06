package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDisableBuiltinsServesOnlyConfiguredAgents(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	cfg.Agents = []config.AgentConfig{
		{ID: "devin", Command: "devin", Args: []string{"acp"}},
		{ID: "opencode", Command: "opencode", Args: []string{"acp"}},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(registry.List()); got != 2 {
		t.Fatalf("listed %d agents, want 2", got)
	}
	for _, id := range []string{"cursor", "claude", "codex", "kiro"} {
		if _, ok := registry.Get(id); ok {
			t.Fatalf("built-in %q survived DisableBuiltins", id)
		}
	}
}

func TestDisableBuiltinsWithNoAgentsIsAnError(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	if _, err := cfg.Registry(); err == nil {
		t.Fatal("expected an empty agent list to be an error")
	}
}

func TestBuiltinsRemainByDefault(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{{ID: "custom", Command: "/bin/echo"}}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("opencode"); !ok {
		t.Fatal("a built-in must survive an unrelated addition")
	}
	if _, ok := registry.Get("custom"); !ok {
		t.Fatal("the configured agent is missing")
	}
}

func TestProxyEnvSetsBothCases(t *testing.T) {
	proxy := config.ProxyConfig{URL: "socks5://127.0.0.1:1080", NoProxy: "localhost,.internal"}
	env := proxy.Env()

	// Tools disagree about which case they read, so both are set; setting only
	// one is a silent failure.
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		if env[name] != "socks5://127.0.0.1:1080" {
			t.Fatalf("%s = %q", name, env[name])
		}
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		if env[name] != "localhost,.internal" {
			t.Fatalf("%s = %q", name, env[name])
		}
	}
}

func TestProxyEnvPerProtocolOverride(t *testing.T) {
	proxy := config.ProxyConfig{
		URL:   "http://all:3128",
		HTTPS: "http://secure:3128",
	}
	env := proxy.Env()

	if env["HTTP_PROXY"] != "http://all:3128" {
		t.Fatalf("HTTP_PROXY = %q", env["HTTP_PROXY"])
	}
	if env["HTTPS_PROXY"] != "http://secure:3128" {
		t.Fatalf("HTTPS_PROXY = %q, the override should win", env["HTTPS_PROXY"])
	}
	if env["ALL_PROXY"] != "http://all:3128" {
		t.Fatalf("ALL_PROXY = %q", env["ALL_PROXY"])
	}
}

func TestProxyEnvEmptyWhenUnset(t *testing.T) {
	env := (config.ProxyConfig{}).Env()
	if len(env) != 0 {
		t.Fatalf("an unset proxy must produce no variables, got %v", env)
	}
	if (config.ProxyConfig{}).Configured() {
		t.Fatal("an unset proxy must not report itself configured")
	}
}

// TestProxyRedactedHidesCredentials matters because the proxy URL may carry a
// password and the value ends up in a log line.
func TestProxyRedactedHidesCredentials(t *testing.T) {
	proxy := config.ProxyConfig{URL: "http://user:secret@proxy.example:3128"}

	got := proxy.Redacted()
	if strings.Contains(got, "secret") {
		t.Fatalf("credentials leaked: %q", got)
	}
	if !strings.Contains(got, "proxy.example") {
		t.Fatalf("the host should survive redaction: %q", got)
	}
}

func TestValidateRejectsABadProxyURL(t *testing.T) {
	for _, raw := range []string{"://nope", "not-a-url", "http://"} {
		cfg := config.Default()
		cfg.Proxy = config.ProxyConfig{URL: raw}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected proxy url %q to be rejected", raw)
		}
	}

	cfg := config.Default()
	cfg.Proxy = config.ProxyConfig{URL: "socks5://127.0.0.1:1080"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid proxy url must pass: %v", err)
	}
}

func TestValidateRejectsAnUnknownCredentialSource(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{
		{ID: "devin", Command: "devin", CredentialSource: "sometimes"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an unknown credential_source to be rejected")
	}
}

func TestRegistryAppliesTheProxyToEveryAgent(t *testing.T) {
	cfg := config.Default()
	cfg.Proxy = config.ProxyConfig{URL: "http://proxy:3128"}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range registry.List() {
		if a.Env["HTTPS_PROXY"] != "http://proxy:3128" {
			t.Fatalf("agent %q did not receive the proxy: %v", a.ID, a.Env)
		}
	}
}

// TestPerAgentEnvWinsOverTheProxy covers the layering: one agent may need to
// bypass the proxy the others use.
func TestPerAgentEnvWinsOverTheProxy(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	cfg.Proxy = config.ProxyConfig{URL: "http://proxy:3128"}
	cfg.Agents = []config.AgentConfig{
		{ID: "direct", Command: "direct", Env: map[string]string{"HTTPS_PROXY": ""}},
		{ID: "proxied", Command: "proxied"},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := registry.Get("direct")
	if direct.Env["HTTPS_PROXY"] != "" {
		t.Fatalf("the agent's own setting should win, got %q", direct.Env["HTTPS_PROXY"])
	}
	proxied, _ := registry.Get("proxied")
	if proxied.Env["HTTPS_PROXY"] != "http://proxy:3128" {
		t.Fatalf("the other agent should still be proxied, got %q", proxied.Env["HTTPS_PROXY"])
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
