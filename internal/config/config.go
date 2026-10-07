// Package config loads the gateway's configuration.
//
// The format is YAML, which is a superset of JSON, so an existing JSON config
// keeps working. Precedence is defaults, then the file, then environment
// variables, then command-line flags.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/agent"
)

// Defaults for the optional numeric fields.
const (
	DefaultAddr           = "127.0.0.1:8720"
	DefaultRequestTimeout = 120 * time.Second
	DefaultSessionTTL     = 30 * time.Minute
	// DefaultConversationHeader is the header a client may send to key a
	// session. It is a name, not a capability, so it can be anything the
	// deployment agrees on.
	DefaultConversationHeader = "X-Chat-Id"
)

// Config is the gateway's runtime configuration.
type Config struct {
	// Addr is the listen address. Defaults to loopback only.
	Addr string `json:"addr" yaml:"addr"`
	// Workspace is the default agent working directory. Empty means the
	// process's working directory, except under FilesystemNone, where it means
	// a private scratch directory.
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
	// Filesystem is the filesystem surface exposed to every agent: "full",
	// "readonly" or "none".
	//
	// One knob rather than a boolean read-only switch, because the three levels
	// nest and a pair of switches would allow a combination that means nothing.
	//
	// "none" is the provider-style mode: the agent is told at initialize that
	// the client has no filesystem, and every fs/* call is refused. That
	// removes the gateway's filesystem surface, not the agent process's own
	// access to the host — an agent CLI is a real process with the operator's
	// permissions. It is a contract with a cooperative agent, not a sandbox.
	Filesystem string `json:"filesystem" yaml:"filesystem"`
	// Mode is the session mode selected after opening a session, for every
	// agent. "plan" and "ask" are read-only on most agents. The value is
	// checked against what each agent advertises.
	Mode string `json:"mode" yaml:"mode"`
	// ConversationHeader names the request header that keys a session, for
	// clients that cannot put an extension field in the body.
	//
	// It is read only when the body names no conversation, so the most explicit
	// key still wins. An empty value disables the mechanism, which is why it is
	// not refilled when a file sets it to nothing.
	ConversationHeader string `json:"conversation_header" yaml:"conversation_header"`
	// DiscoverModels reads each agent's model catalog in the background at
	// startup, so /v1/models lists the models and not only the agents.
	//
	// Off by default: discovery starts every agent, which costs its cold start.
	// Without it the catalog still appears, lazily, once an agent is first used.
	DiscoverModels bool `json:"discover_models" yaml:"discover_models"`
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
	// Filesystem overrides the global mode for this agent. Empty inherits it,
	// which is also how a proxied deployment keeps one agent that can write.
	Filesystem string `json:"filesystem" yaml:"filesystem"`
	// Mode overrides the global mode for this agent. Empty inherits it.
	Mode string `json:"mode" yaml:"mode"`
	// Workspace overrides the global working directory for this agent. Empty
	// inherits it. A request may still override it per call.
	Workspace string `json:"workspace" yaml:"workspace"`
}

// Default returns the configuration used when nothing is specified.
func Default() Config {
	return Config{
		Addr:                  DefaultAddr,
		Permission:            "allow",
		Filesystem:            agent.FilesystemFull,
		ConversationHeader:    DefaultConversationHeader,
		RequestTimeoutSeconds: int(DefaultRequestTimeout / time.Second),
		SessionTTLSeconds:     int(DefaultSessionTTL / time.Second),
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
	switch c.Filesystem {
	case agent.FilesystemFull, agent.FilesystemReadOnly, agent.FilesystemNone:
	default:
		return fmt.Errorf("config: filesystem must be %q, %q or %q, got %q",
			agent.FilesystemFull, agent.FilesystemReadOnly, agent.FilesystemNone, c.Filesystem)
	}
	for _, a := range c.Agents {
		if strings.TrimSpace(a.ID) == "" {
			return errors.New("config: every agent needs an id")
		}
		if strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("config: agent %q needs a command", a.ID)
		}
		switch a.Filesystem {
		case "", agent.FilesystemFull, agent.FilesystemReadOnly, agent.FilesystemNone:
		default:
			return fmt.Errorf("config: agent %q has filesystem %q; want %q, %q or %q",
				a.ID, a.Filesystem, agent.FilesystemFull, agent.FilesystemReadOnly, agent.FilesystemNone)
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
	// The proxy, filesystem and mode are global, so they reach built-ins too —
	// not only the agents a configuration happens to name.
	for i := range agents {
		agents[i].Env = mergeEnv(proxyEnv, agents[i].Env)
		agents[i].Filesystem = c.Filesystem
		agents[i].Mode = c.Mode
		agents[i].Workspace = c.Workspace
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

		filesystem := configured.Filesystem
		if filesystem == "" {
			filesystem = c.Filesystem
		}
		mode := configured.Mode
		if mode == "" {
			mode = c.Mode
		}
		workspace := configured.Workspace
		if workspace == "" {
			workspace = c.Workspace
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
			Filesystem:       filesystem,
			Mode:             mode,
			Workspace:        workspace,
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
