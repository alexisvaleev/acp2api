// Package config loads the gateway's configuration.
//
// The format is JSON: the standard library already speaks it, so the gateway
// keeps a zero-dependency build. Precedence is defaults, then the file, then
// environment variables, then command-line flags.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/agent"
)

// Environment variables that override file values.
const (
	EnvAddr       = "ACP2API_ADDR"
	EnvToken      = "ACP2API_TOKEN"
	EnvWorkspace  = "ACP2API_WORKSPACE"
	EnvPermission = "ACP2API_PERMISSION"
)

// Defaults for the optional numeric fields.
const (
	DefaultAddr           = "127.0.0.1:8720"
	DefaultRequestTimeout = 120 * time.Second
	DefaultSessionTTL     = 30 * time.Minute
)

// Config is the gateway's runtime configuration.
type Config struct {
	// Addr is the listen address. Defaults to loopback only.
	Addr string `json:"addr"`
	// Workspace is the default agent working directory. Empty means the
	// process's working directory.
	Workspace string `json:"workspace"`
	// Token is the bearer token required on /v1/* routes. An empty token is
	// only accepted together with AllowNoAuth.
	Token string `json:"token"`
	// Permission is the permission policy: "allow" or "deny".
	Permission string `json:"permission"`
	// RequestTimeoutSeconds bounds one ACP request. Zero uses the default.
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
	// SessionTTLSeconds closes idle sessions and agent processes. Zero uses the
	// default; a negative value disables reaping.
	SessionTTLSeconds int `json:"session_ttl_seconds"`
	// Agents overrides built-in agents by id and adds new ones.
	Agents []AgentConfig `json:"agents"`
	// DisableBuiltins removes the built-in agents, so only those listed in
	// Agents are served. Without it the Agents list can only add and override,
	// which makes /v1/models advertise agents the host cannot run.
	DisableBuiltins bool `json:"disable_builtins"`
	// Proxy routes the agents' outbound traffic. The gateway itself makes no
	// outbound requests, so this exists for the agent CLIs.
	Proxy ProxyConfig `json:"proxy"`
}

// ProxyConfig is the proxy the agent CLIs should use.
//
// It is applied as environment, not by interception: every agent CLI reaches
// its own API over HTTP, and the standard proxy variables are how that is
// routed — for a corporate egress, or to reach an API that is not served in the
// host's region.
type ProxyConfig struct {
	// URL applies to every protocol. A scheme such as socks5:// is honoured by
	// most CLIs.
	URL string `json:"url"`
	// HTTP and HTTPS override URL for a single protocol.
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	// NoProxy lists hosts that bypass the proxy, comma separated.
	NoProxy string `json:"no_proxy"`
}

// Env renders the proxy as the environment an agent CLI expects.
//
// Both cases of each name are set: tools disagree about which they read, and
// setting only one is a silent failure. The values may carry credentials, so
// this is never logged verbatim.
func (p ProxyConfig) Env() map[string]string {
	env := map[string]string{}

	all := p.URL
	httpURL := firstNonEmpty(p.HTTP, p.URL)
	httpsURL := firstNonEmpty(p.HTTPS, p.URL)

	for _, pair := range []struct{ name, value string }{
		{"HTTP_PROXY", httpURL}, {"http_proxy", httpURL},
		{"HTTPS_PROXY", httpsURL}, {"https_proxy", httpsURL},
		{"ALL_PROXY", all}, {"all_proxy", all},
		{"NO_PROXY", p.NoProxy}, {"no_proxy", p.NoProxy},
	} {
		if pair.value != "" {
			env[pair.name] = pair.value
		}
	}
	return env
}

// Configured reports whether any proxy is set.
func (p ProxyConfig) Configured() bool {
	return p.URL != "" || p.HTTP != "" || p.HTTPS != ""
}

// Redacted renders the proxy for a log line, with any credentials removed.
func (p ProxyConfig) Redacted() string {
	if !p.Configured() {
		return ""
	}
	return redactURL(firstNonEmpty(p.HTTPS, p.HTTP, p.URL))
}

// redactURL strips userinfo credentials from a URL.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.User("***")
	return parsed.String()
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mergeEnv layers environment maps, later ones winning.
func mergeEnv(layers ...map[string]string) map[string]string {
	total := 0
	for _, layer := range layers {
		total += len(layer)
	}
	if total == 0 {
		return nil
	}
	out := make(map[string]string, total)
	for _, layer := range layers {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

// AgentConfig describes one agent CLI, overriding or extending the built-ins.
type AgentConfig struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	// AuthMethod overrides which advertised ACP auth method is selected.
	AuthMethod string `json:"auth_method"`
	// APIKeyEnv names an environment variable holding an API key for a headless
	// authenticate. Leave it empty for an agent already logged in on this host.
	APIKeyEnv string `json:"api_key_env"`
	// CredentialSource is "auto" (default), "env", "interactive" or "none".
	// See agent.Agent.CredentialSource.
	CredentialSource string `json:"credential_source"`
}

// Default returns the configuration used when nothing is specified.
func Default() Config {
	return Config{
		Addr:                  DefaultAddr,
		Permission:            "allow",
		RequestTimeoutSeconds: int(DefaultRequestTimeout / time.Second),
		SessionTTLSeconds:     int(DefaultSessionTTL / time.Second),
	}
}

// Load returns the default configuration merged with the file at path (when
// non-empty) and the environment. It does not validate.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}

	applyEnv(&cfg)
	cfg.normalise()
	return cfg, nil
}

// applyEnv overlays environment variables onto the configuration.
func applyEnv(cfg *Config) {
	if v := os.Getenv(EnvAddr); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv(EnvToken); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv(EnvWorkspace); v != "" {
		cfg.Workspace = v
	}
	if v := os.Getenv(EnvPermission); v != "" {
		cfg.Permission = v
	}
}

// normalise fills in defaults and cleans paths.
func (c *Config) normalise() {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.Permission == "" {
		c.Permission = "allow"
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = int(DefaultRequestTimeout / time.Second)
	}
	if c.SessionTTLSeconds == 0 {
		c.SessionTTLSeconds = int(DefaultSessionTTL / time.Second)
	}
	if c.Workspace != "" {
		if abs, err := filepath.Abs(c.Workspace); err == nil {
			c.Workspace = abs
		}
	}
}

// RequestTimeout returns the per-request ACP timeout.
func (c Config) RequestTimeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}

// SessionTTL returns the idle-reap window. A negative value disables reaping.
func (c Config) SessionTTL() time.Duration {
	if c.SessionTTLSeconds < 0 {
		return 0
	}
	return time.Duration(c.SessionTTLSeconds) * time.Second
}

// Validate reports configuration that cannot be used.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("config: addr is required")
	}
	if c.Token == "" && !isLoopback(c.Addr) {
		return fmt.Errorf("config: refusing to listen on %s without a token; set token or bind to loopback", c.Addr)
	}
	switch c.Permission {
	case "allow", "deny":
	default:
		return fmt.Errorf("config: permission must be \"allow\" or \"deny\", got %q", c.Permission)
	}
	for _, a := range c.Agents {
		if strings.TrimSpace(a.ID) == "" {
			return errors.New("config: every agent needs an id")
		}
		if strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("config: agent %q needs a command", a.ID)
		}
		switch a.CredentialSource {
		case "", agent.CredentialAuto, agent.CredentialEnv,
			agent.CredentialInteractive, agent.CredentialNone:
		default:
			return fmt.Errorf(
				"config: agent %q has credential_source %q; want auto, env, interactive or none",
				a.ID, a.CredentialSource)
		}
	}

	for name, raw := range map[string]string{
		"url": c.Proxy.URL, "http": c.Proxy.HTTP, "https": c.Proxy.HTTPS,
	} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("config: proxy.%s is not a valid url: %w", name, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("config: proxy.%s must include a scheme and host, got %q", name, raw)
		}
	}
	return nil
}

// BuildRegistry assembles the agents, applies the per-agent modules, resolves
// each agent's credential once, and returns the registry.
//
// Credentials are resolved here, at startup, rather than on every spawn: a
// module can read the key the agent already stored, and the operator does not
// have to duplicate it into the service environment.
func (c Config) BuildRegistry(modules []agent.Module, source agent.Source) (*agent.Registry, error) {
	base, err := c.Registry()
	if err != nil {
		return nil, err
	}

	agents, err := agent.Apply(base.List(), modules)
	if err != nil {
		return nil, err
	}
	agents, err = agent.ResolveCredentials(agents, modules, source)
	if err != nil {
		return nil, err
	}

	registry := agent.NewRegistry()
	for _, a := range agents {
		registry.Register(a)
	}
	return registry, nil
}

// Registry builds the agent registry: built-ins first, then configuration
// overrides, so a config entry can retarget a built-in command. With
// DisableBuiltins only the configured agents exist.
func (c Config) Registry() (*agent.Registry, error) {
	proxyEnv := c.Proxy.Env()

	// Start from the built-ins, unless they are disabled.
	var agents []agent.Agent
	if !c.DisableBuiltins {
		agents = agent.Builtins()
	}
	// The proxy is global, so it reaches built-ins too — not only the agents a
	// configuration happens to name.
	for i := range agents {
		agents[i].Env = mergeEnv(proxyEnv, agents[i].Env)
	}

	// Then apply the configured agents: an entry overrides a built-in by id, or
	// adds a new one.
	index := make(map[string]int, len(agents))
	for i, a := range agents {
		index[a.ID] = i
	}
	for _, configured := range c.Agents {
		entry := agent.Agent{
			ID:               configured.ID,
			Name:             configured.Name,
			Command:          configured.Command,
			Args:             configured.Args,
			Env:              mergeEnv(proxyEnv, configured.Env),
			AuthMethod:       configured.AuthMethod,
			APIKeyEnv:        configured.APIKeyEnv,
			CredentialSource: configured.CredentialSource,
		}
		if i, ok := index[configured.ID]; ok {
			// An override keeps the built-in display name unless it sets one.
			if entry.Name == "" {
				entry.Name = agents[i].Name
			}
			agents[i] = entry
			continue
		}
		if entry.Name == "" {
			entry.Name = entry.ID
		}
		index[entry.ID] = len(agents)
		agents = append(agents, entry)
	}

	if len(agents) == 0 {
		return nil, errors.New("config: no agents configured")
	}
	return agent.NewRegistry(agents...), nil
}

// isLoopback reports whether an address is bound to loopback only.
func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
