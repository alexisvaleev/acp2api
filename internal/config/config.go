// Package config loads the gateway's configuration.
//
// The format is JSON: the standard library already speaks it, so the gateway
// keeps a zero-dependency build. Precedence is defaults, then the file, then
// environment variables, then command-line flags.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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
	Addr string `json:"addr" yaml:"addr"`
	// Workspace is the default agent working directory. Empty means the
	// process's working directory.
	Workspace string `json:"workspace" yaml:"workspace"`
	// Token is the bearer token required on /v1/* routes. An empty token is
	// only accepted together with AllowNoAuth.
	Token string `json:"token" yaml:"token"`
	// Permission is the permission policy: "allow" or "deny".
	Permission string `json:"permission" yaml:"permission"`
	// RequestTimeoutSeconds bounds one ACP request. Zero uses the default.
	RequestTimeoutSeconds int `json:"request_timeout_seconds" yaml:"request_timeout_seconds"`
	// SessionTTLSeconds closes idle sessions and agent processes. Zero uses the
	// default; a negative value disables reaping.
	SessionTTLSeconds int `json:"session_ttl_seconds" yaml:"session_ttl_seconds"`
	// Agents overrides built-in agents by id and adds new ones.
	Agents []AgentConfig `json:"agents" yaml:"agents"`
	// DisableBuiltins removes the built-in agents, so only those listed in
	// Agents are served. Without it the Agents list can only add and override,
	// which makes /v1/models advertise agents the host cannot run.
	DisableBuiltins bool `json:"disable_builtins" yaml:"disable_builtins"`
	// Proxy routes the agents' outbound traffic. The gateway itself makes no
	// outbound requests, so this exists for the agent CLIs.
	Proxy ProxyConfig `json:"proxy" yaml:"proxy"`
}

// AgentConfig describes one agent CLI, overriding or extending the built-ins.
type AgentConfig struct {
	ID      string            `json:"id" yaml:"id"`
	Name    string            `json:"name" yaml:"name"`
	Command string            `json:"command" yaml:"command"`
	Args    []string          `json:"args" yaml:"args"`
	Env     map[string]string `json:"env" yaml:"env"`
	// AuthMethod overrides which advertised ACP auth method is selected.
	AuthMethod string `json:"auth_method" yaml:"auth_method"`
	// APIKeyEnv names an environment variable holding an API key for a headless
	// authenticate. Leave it empty for an agent already logged in on this host.
	APIKeyEnv string `json:"api_key_env" yaml:"api_key_env"`
	// CredentialSource is "auto" (default), "env", "interactive" or "none".
	// See agent.Agent.CredentialSource.
	CredentialSource string `json:"credential_source" yaml:"credential_source"`
	// Proxy overrides the global proxy for this agent only. Absent inherits the
	// global one; present replaces it, and an empty url sends this agent direct.
	Proxy *ProxyConfig `json:"proxy" yaml:"proxy"`
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
//
// The file is YAML. YAML is a superset of JSON, so an existing JSON config keeps
// working through the same parser, and comments — which JSON does not allow —
// are available in new ones.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		expanded, err := expandEnv(string(data))
		if err != nil {
			return Config{}, fmt.Errorf("config: %s: %w", path, err)
		}
		if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
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
		if a.Proxy != nil {
			if err := a.Proxy.validate("agents." + a.ID + ".proxy"); err != nil {
				return err
			}
		}
	}

	return c.Proxy.validate("proxy")
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
		// An agent's own proxy replaces the global one rather than merging with
		// it: a half-override would leave one protocol pointed at the global
		// proxy and another at the agent's, which is never what was meant.
		proxy := c.Proxy
		if configured.Proxy != nil {
			proxy = *configured.Proxy
		}

		entry := agent.Agent{
			ID:               configured.ID,
			Name:             configured.Name,
			Command:          configured.Command,
			Args:             configured.Args,
			Env:              mergeEnv(proxy.Env(), configured.Env),
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
