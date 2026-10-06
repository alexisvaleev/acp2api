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
	// ClientCapabilities overrides the default initialize capabilities.
	// Nil means DefaultCapabilities().
	ClientCapabilities map[string]any
}

// Capabilities returns the client capabilities to send during initialize.
func (a Agent) Capabilities() map[string]any {
	if a.ClientCapabilities != nil {
		return a.ClientCapabilities
	}
	return DefaultCapabilities()
}

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
