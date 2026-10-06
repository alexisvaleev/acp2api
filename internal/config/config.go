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
	// AllowInteractiveAuth permits an authenticate call that may open a browser
	// or prompt. Off by default, because a daemon must not open windows.
	AllowInteractiveAuth bool `json:"allow_interactive_auth"`
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
	var r *agent.Registry
	if c.DisableBuiltins {
		r = agent.NewRegistry()
	} else {
		r = agent.BuiltinRegistry()
	}

	for _, a := range c.Agents {
		name := a.Name
		if name == "" {
			if existing, ok := r.Get(a.ID); ok {
				name = existing.Name
			} else {
				name = a.ID
			}
		}
		r.Register(agent.Agent{
			ID:                   a.ID,
			Name:                 name,
			Command:              a.Command,
			Args:                 a.Args,
			Env:                  a.Env,
			AuthMethod:           a.AuthMethod,
			APIKeyEnv:            a.APIKeyEnv,
			AllowInteractiveAuth: a.AllowInteractiveAuth,
		})
	}
	if len(r.List()) == 0 {
		return nil, errors.New("config: no agents configured")
	}
	return r, nil
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
