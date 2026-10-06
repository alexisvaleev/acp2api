package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/config"
)

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

func TestPerAgentProxyReplacesTheGlobalOne(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	cfg.Proxy = config.ProxyConfig{URL: "http://global:3128"}
	cfg.Agents = []config.AgentConfig{
		{ID: "own", Command: "own", Proxy: &config.ProxyConfig{URL: "socks5://own:1080"}},
		{ID: "direct", Command: "direct", Proxy: &config.ProxyConfig{}},
		{ID: "inherits", Command: "inherits"},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}

	own, _ := registry.Get("own")
	if own.Env["HTTPS_PROXY"] != "socks5://own:1080" {
		t.Fatalf("own proxy = %q", own.Env["HTTPS_PROXY"])
	}

	// An empty proxy block means "this agent goes direct", not "inherit".
	direct, _ := registry.Get("direct")
	if _, ok := direct.Env["HTTPS_PROXY"]; ok {
		t.Fatalf("direct agent should have no proxy, got %q", direct.Env["HTTPS_PROXY"])
	}

	inherits, _ := registry.Get("inherits")
	if inherits.Env["HTTPS_PROXY"] != "http://global:3128" {
		t.Fatalf("inherited proxy = %q", inherits.Env["HTTPS_PROXY"])
	}
}

func TestPerAgentProxyOverrideIsNotMerged(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	cfg.Proxy = config.ProxyConfig{URL: "http://global:3128", NoProxy: "global.internal"}
	cfg.Agents = []config.AgentConfig{
		{ID: "own", Command: "own", Proxy: &config.ProxyConfig{URL: "http://own:3128"}},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	own, _ := registry.Get("own")

	// A half-override would leave the global no_proxy behind, pointing at a
	// proxy this agent does not use.
	if _, ok := own.Env["NO_PROXY"]; ok {
		t.Fatalf("the global no_proxy leaked into the override: %q", own.Env["NO_PROXY"])
	}
}

func TestValidateNamesTheAgentWithABadProxy(t *testing.T) {
	cfg := config.Default()
	cfg.Agents = []config.AgentConfig{
		{ID: "devin", Command: "devin", Proxy: &config.ProxyConfig{URL: "not-a-url"}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a bad per-agent proxy url to be rejected")
	}
	if !strings.Contains(err.Error(), "devin") {
		t.Fatalf("the error should name the agent: %v", err)
	}
}

func TestWorkspaceLayering(t *testing.T) {
	cfg := config.Default()
	cfg.DisableBuiltins = true
	cfg.Workspace = "/srv/global"
	cfg.Agents = []config.AgentConfig{
		{ID: "own", Command: "own", Workspace: "/srv/own"},
		{ID: "inherits", Command: "inherits"},
	}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	own, _ := registry.Get("own")
	if own.Workspace != "/srv/own" {
		t.Fatalf("own workspace = %q", own.Workspace)
	}
	inherits, _ := registry.Get("inherits")
	if inherits.Workspace != "/srv/global" {
		t.Fatalf("inherited workspace = %q", inherits.Workspace)
	}
}

func TestGlobalWorkspaceReachesBuiltins(t *testing.T) {
	cfg := config.Default()
	cfg.Workspace = "/srv/global"

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range registry.List() {
		if a.Workspace != "/srv/global" {
			t.Fatalf("agent %q workspace = %q", a.ID, a.Workspace)
		}
	}
}

func TestGlobalReadOnlyReachesBuiltinsAndIsOverridable(t *testing.T) {
	cfg := config.Default()
	cfg.ReadOnly = true
	no := false
	cfg.Agents = []config.AgentConfig{{ID: "opencode", Command: "opencode", ReadOnly: &no}}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	devin, _ := registry.Get("devin")
	if !devin.ReadOnly {
		t.Fatal("the global read_only should reach a built-in")
	}
	opencode, _ := registry.Get("opencode")
	if opencode.ReadOnly {
		t.Fatal("a per-agent override should win")
	}
}

func TestGlobalModeIsInheritedAndOverridable(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "plan"
	cfg.Agents = []config.AgentConfig{{ID: "opencode", Command: "opencode", Mode: "build"}}

	registry, err := cfg.Registry()
	if err != nil {
		t.Fatal(err)
	}
	devin, _ := registry.Get("devin")
	if devin.Mode != "plan" {
		t.Fatalf("inherited mode = %q", devin.Mode)
	}
	opencode, _ := registry.Get("opencode")
	if opencode.Mode != "build" {
		t.Fatalf("overridden mode = %q", opencode.Mode)
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
