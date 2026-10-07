// Package agent describes the ACP agent CLIs this gateway can drive.
//
// An agent is pure data: how to spawn it, what environment it needs, and which
// client capabilities to advertise. Adding an agent means adding an entry here
// and nothing else — no other package knows a specific CLI by name.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// Agent is one drivable ACP agent CLI.
type Agent struct {
	// ID is the selector used in the OpenAI `model` field and in config.
	ID string
	// Name is the human-readable label shown by /v1/models.
	Name string
	// Command is the executable, resolved on PATH or absolute.
	Command string
	// Args are passed to Command verbatim; the ACP subcommand lives here.
	Args []string
	// Env is extra environment for the child, on top of the inherited one.
	Env map[string]string
	// AuthMethod overrides which advertised auth method is selected during
	// authenticate. Empty means the first one the agent advertises.
	AuthMethod string
	// APIKeyEnv names an environment variable whose value is sent as the
	// authenticate `_meta.api_key`, for a headless login. Empty sends no key,
	// which is right for an agent already logged in on this machine.
	APIKeyEnv string
	// APIKey is the credential resolved once at startup, either by the agent's
	// module or from APIKeyEnv. It is held for the process lifetime and is
	// never logged.
	APIKey string
	// CredentialSource decides where the credential comes from, and therefore
	// whether an interactive flow is ever allowed. One knob, because the two
	// questions are the same question.
	//
	//   auto        (default) APIKeyEnv, then the agent's module
	//   env         APIKeyEnv only; a missing value stops the process
	//   interactive never send a key; let the agent prompt, browser included
	//   none        never authenticate
	CredentialSource string
	// Filesystem is the filesystem surface the gateway exposes to this agent:
	// "full", "readonly" or "none". Empty means full.
	//
	// One knob rather than a boolean because the three levels nest — "none"
	// implies "readonly" — and a pair of switches would allow a combination
	// that means nothing. It also decides the capabilities advertised at
	// initialize: advertising a capability we then refuse would be a lie, and
	// the agent would spend a turn discovering it.
	Filesystem string
	// Mode is the session mode to select after opening a session, such as
	// "plan" or "ask". Empty leaves whatever the agent defaults to.
	Mode string
	// Workspace is the directory this agent works in. Empty falls back to the
	// manager's default. A request may still override it per call.
	//
	// It is also half of the connection key, so two agents with different
	// workspaces get separate processes rather than sharing one.
	Workspace string
	// ClientCapabilities overrides the default initialize capabilities.
	// Nil means DefaultCapabilities() or ReadOnlyCapabilities().
	ClientCapabilities map[string]any
}

// Credential sources. See Agent.CredentialSource.
const (
	CredentialAuto        = "auto"
	CredentialEnv         = "env"
	CredentialInteractive = "interactive"
	CredentialNone        = "none"
)

// Filesystem access granted to an agent. See Agent.Filesystem.
const (
	// FilesystemFull serves reads and writes.
	FilesystemFull = "full"
	// FilesystemReadOnly serves reads and refuses writes.
	FilesystemReadOnly = "readonly"
	// FilesystemNone serves neither: the agent has no filesystem at all.
	FilesystemNone = "none"
)

// FilesystemMode returns the agent's filesystem mode.
//
// An unset mode is full, which is what an operator who never mentioned the
// filesystem means. An unrecognised one is none: a typo must not hand the agent
// the filesystem. Configuration validation rejects such a value before a
// request is served, so this is the second line of defence.
func (a Agent) FilesystemMode() string {
	switch a.Filesystem {
	case FilesystemReadOnly:
		return FilesystemReadOnly
	case FilesystemNone:
		return FilesystemNone
	case "", FilesystemFull:
		return FilesystemFull
	default:
		return FilesystemNone
	}
}

// CredentialMode returns the agent's credential source, defaulting to auto.
func (a Agent) CredentialMode() string {
	if a.CredentialSource == "" {
		return CredentialAuto
	}
	return a.CredentialSource
}

// WantsInteractiveAuth reports whether the agent may prompt or open a browser.
func (a Agent) WantsInteractiveAuth() bool {
	return a.CredentialMode() == CredentialInteractive
}

// UsesStoredCredential reports whether the gateway may supply a key at all.
func (a Agent) UsesStoredCredential() bool {
	switch a.CredentialMode() {
	case CredentialInteractive, CredentialNone:
		return false
	default:
		return true
	}
}

// Capabilities returns the client capabilities to send during initialize.
func (a Agent) Capabilities() map[string]any {
	if a.ClientCapabilities != nil {
		return a.ClientCapabilities
	}
	switch a.FilesystemMode() {
	case FilesystemNone:
		return NoFilesystemCapabilities()
	case FilesystemReadOnly:
		return ReadOnlyCapabilities()
	default:
		return DefaultCapabilities()
	}
}

// HasKey reports whether a credential was resolved for this agent.
func (a Agent) HasKey() bool { return a.APIKey != "" }

// DefaultCapabilities advertises exactly what this gateway implements: reading
// and writing files. Terminals are deliberately not advertised — a headless
// gateway cannot stream a TTY, and claiming the capability makes agents issue
// terminal/* calls that must then be refused.
func DefaultCapabilities() map[string]any {
	return map[string]any{
		"fs": map[string]any{
			"readTextFile":  true,
			"writeTextFile": true,
		},
	}
}

// ReadOnlyCapabilities advertises reading only.
//
// This is the switch that makes a coding agent behave like a model provider:
// the point of read-only use is to ask questions, and an agent that believes it
// can edit files will try.
func ReadOnlyCapabilities() map[string]any {
	return map[string]any{
		"fs": map[string]any{
			"readTextFile":  true,
			"writeTextFile": false,
		},
	}
}

// NoFilesystemCapabilities advertises no filesystem at all.
//
// This pair of false values is ACP's own default for clientCapabilities, and
// the spec is explicit that an agent MUST NOT call a filesystem method whose
// capability is false or absent. Stating it rather than omitting the key keeps
// the handshake unambiguous: the agent is told the client has no filesystem,
// instead of being left to guess whether the client forgot to describe itself.
//
// It removes the gateway's filesystem surface, not the agent process's own
// access to the host — an agent CLI is a real process with the operator's
// permissions. It is a contract with a cooperative agent, not a sandbox.
func NoFilesystemCapabilities() map[string]any {
	return map[string]any{
		"fs": map[string]any{
			"readTextFile":  false,
			"writeTextFile": false,
		},
	}
}

// Registry resolves agents by id and by OpenAI model id.
type Registry struct {
	agents map[string]Agent
	order  []string
}

// NewRegistry builds a registry from the given agents. Later entries with a
// duplicate id replace earlier ones.
func NewRegistry(agents ...Agent) *Registry {
	r := &Registry{agents: make(map[string]Agent)}
	for _, a := range agents {
		r.Register(a)
	}
	return r
}

// Register adds or replaces an agent.
func (r *Registry) Register(a Agent) {
	if _, exists := r.agents[a.ID]; !exists {
		r.order = append(r.order, a.ID)
	}
	r.agents[a.ID] = a
}

// Get returns the agent with the given id.
func (r *Registry) Get(id string) (Agent, bool) {
	a, ok := r.agents[id]
	return a, ok
}

// List returns the registered agents, sorted by id.
func (r *Registry) List() []Agent {
	out := make([]Agent, 0, len(r.agents))
	for _, id := range r.order {
		out = append(out, r.agents[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resolve maps an OpenAI model id to an agent and an optional agent-side model.
//
// The model id is either a bare agent id ("devin") or "agent/model"
// ("devin/opus"). The model half selects a value for the agent's model config
// option once the session is open; it is empty for the bare form.
func (r *Registry) Resolve(modelID string) (Agent, string, error) {
	if modelID == "" {
		return Agent{}, "", fmt.Errorf("agent: empty model id")
	}
	id, model, _ := strings.Cut(modelID, "/")
	a, ok := r.agents[id]
	if !ok {
		return Agent{}, "", fmt.Errorf("agent: unknown model id %q", modelID)
	}
	return a, model, nil
}
