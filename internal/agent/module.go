package agent

import (
	"fmt"
	"log/slog"
	"os"
)

// Module is the agent-specific knowledge the core deliberately does not have.
//
// The core drives any ACP agent through one generic path. A module augments a
// single agent with what only that agent needs: where it keeps its credentials,
// how to authenticate without a prompt, which model catalog it advertises.
//
// The direction of the dependency is the point: the core never imports a
// module. Modules are assembled at the edge — in cmd — and passed in, so adding
// an agent never means editing the core, and the core never grows a switch on
// an agent's name.
type Module interface {
	// ID is the agent this module augments. It must match an agent id.
	ID() string
	// Augment returns the agent with the module's knowledge applied: spawn
	// details, client capabilities, auth behaviour.
	Augment(Agent) Agent
	// Credential resolves the key the agent needs for authenticate, or "" when
	// it needs none.
	//
	// It runs once, at startup, so a key found in the agent's own store is read
	// a single time and then held for every spawn. That is deliberate: a daemon
	// should not go looking for credentials on each request, and an operator
	// should not have to copy a key the agent already has.
	Credential(Source) (string, error)
}

// Source is what a module may consult while resolving a credential.
type Source struct {
	// Env reads a process environment variable.
	Env func(string) string
	// ReadFile reads a file, so a module can look in the agent's own store.
	ReadFile func(string) ([]byte, error)
	// Home is the user's home directory.
	Home string
}

// OS returns a Source backed by the real process and filesystem.
func OS() Source {
	home, _ := os.UserHomeDir()
	return Source{
		Env:      os.Getenv,
		ReadFile: os.ReadFile,
		Home:     home,
	}
}

// Apply runs every module against the agents, returning the augmented set.
//
// A module that names an agent which is not registered is an error rather than
// a silent no-op: a typo in a module id would otherwise disable its knowledge
// without anyone noticing.
func Apply(agents []Agent, modules []Module) ([]Agent, error) {
	byID := make(map[string]int, len(agents))
	for i, a := range agents {
		byID[a.ID] = i
	}

	out := make([]Agent, len(agents))
	copy(out, agents)

	for _, module := range modules {
		index, ok := byID[module.ID()]
		if !ok {
			return nil, fmt.Errorf("agent: module %q names an agent that is not registered", module.ID())
		}
		out[index] = module.Augment(out[index])
	}
	return out, nil
}

// ResolveCredentials fills in each agent's API key by asking its module, then
// falls back to the configured environment variable.
//
// Resolved once at startup; the value is held on the Agent for every spawn. It
// is never logged.
func ResolveCredentials(agents []Agent, modules []Module, source Source) ([]Agent, error) {
	byID := make(map[string]Module, len(modules))
	for _, module := range modules {
		byID[module.ID()] = module
	}

	out := make([]Agent, len(agents))
	copy(out, agents)

	for i := range out {
		a := &out[i]

		// interactive and none mean "no key by design", so nothing is resolved.
		if !a.UsesStoredCredential() {
			continue
		}

		// An explicit setting wins: the operator named the variable to use.
		if a.APIKeyEnv != "" {
			key := source.Env(a.APIKeyEnv)
			if key == "" {
				return nil, fmt.Errorf(
					"agent %q is configured with api_key_env %q, which is not set and "+
						"holds no credential; set it, or drop api_key_env to let the agent's "+
						"own error explain what it needs",
					a.ID, a.APIKeyEnv)
			}
			a.APIKey = key
			slog.With("module", "agent").Info("credential resolved", "agent", a.ID, "source", a.APIKeyEnv)
			continue
		}

		if a.CredentialMode() == CredentialEnv {
			return nil, fmt.Errorf(
				"agent %q uses credential_source %q but names no api_key_env to read",
				a.ID, CredentialEnv)
		}

		// auto: ask the module, which may know where the agent keeps it.
		module, ok := byID[a.ID]
		if !ok {
			continue
		}
		key, err := module.Credential(source)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", a.ID, err)
		}
		if key != "" {
			a.APIKey = key
			slog.With("module", "agent").Info("credential resolved", "agent", a.ID, "source", "module")
		}
	}
	return out, nil
}
