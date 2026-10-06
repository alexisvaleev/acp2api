package agent_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/agent"
)

// stubModule is a module under test's control.
type stubModule struct {
	id         string
	credential string
	err        error
	augmented  *agent.Agent
}

func (m *stubModule) ID() string { return m.id }

func (m *stubModule) Augment(a agent.Agent) agent.Agent {
	if m.augmented != nil {
		return *m.augmented
	}
	a.Args = append(a.Args, "--augmented")
	return a
}

func (m *stubModule) Credential(agent.Source) (string, error) {
	return m.credential, m.err
}

// modules adapts the stubs to the interface slice Go will not convert for us.
func modules(stubs ...*stubModule) []agent.Module {
	out := make([]agent.Module, len(stubs))
	for i, s := range stubs {
		out[i] = s
	}
	return out
}

// testSource is a Source backed by maps, so no test touches the real machine.
func testSource(files map[string]string, env map[string]string) agent.Source {
	return agent.Source{
		Home: "/home/test",
		Env:  func(k string) string { return env[k] },
		ReadFile: func(path string) ([]byte, error) {
			body, ok := files[path]
			if !ok {
				return nil, errors.New("no such file")
			}
			return []byte(body), nil
		},
	}
}

func TestApplyAugmentsMatchingAgents(t *testing.T) {
	agents := []agent.Agent{{ID: "devin", Command: "devin"}, {ID: "other", Command: "other"}}

	out, err := agent.Apply(agents, modules(&stubModule{id: "devin"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(out[0].Args) != 1 || out[0].Args[0] != "--augmented" {
		t.Fatalf("devin was not augmented: %+v", out[0])
	}
	if len(out[1].Args) != 0 {
		t.Fatalf("an unrelated agent was modified: %+v", out[1])
	}
	// The input slice must not be mutated: Apply returns a copy.
	if len(agents[0].Args) != 0 {
		t.Fatalf("Apply mutated its input: %+v", agents[0])
	}
}

// TestApplyRejectsAModuleForAnUnknownAgent guards a silent failure: a typo in a
// module id would otherwise disable that agent's knowledge without a word.
func TestApplyRejectsAModuleForAnUnknownAgent(t *testing.T) {
	_, err := agent.Apply([]agent.Agent{{ID: "devin"}}, modules(&stubModule{id: "devinn"}))
	if err == nil {
		t.Fatal("expected a module for an unregistered agent to be rejected")
	}
	if !strings.Contains(err.Error(), "devinn") {
		t.Fatalf("error should name the module: %v", err)
	}
}

// TestResolveCredentialsPrefersTheConfiguredVariable covers the priority: an
// operator who named a variable meant it, so it beats the module's discovery.
func TestResolveCredentialsPrefersTheConfiguredVariable(t *testing.T) {
	agents := []agent.Agent{{ID: "devin", APIKeyEnv: "FROM_ENV"}}
	source := testSource(nil, map[string]string{"FROM_ENV": "env-key"})

	out, err := agent.ResolveCredentials(agents, modules(&stubModule{id: "devin", credential: "module-key"}), source)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].APIKey != "env-key" {
		t.Fatalf("the configured variable should win, got %q", out[0].APIKey)
	}
	if !out[0].HasKey() {
		t.Fatal("HasKey should report the resolved credential")
	}
}

// TestResolveCredentialsFallsBackToTheModule is the Devin case: no variable is
// configured, and the module reads the key the agent already stored.
func TestResolveCredentialsFallsBackToTheModule(t *testing.T) {
	agents := []agent.Agent{{ID: "devin"}}

	out, err := agent.ResolveCredentials(agents, modules(&stubModule{id: "devin", credential: "module-key"}), testSource(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out[0].APIKey != "module-key" {
		t.Fatalf("APIKey = %q, want the module's key", out[0].APIKey)
	}
}

// TestResolveCredentialsFailsAtStartup is the point of resolving early: a
// misconfigured key must stop the process, not surface on the first request.
func TestResolveCredentialsFailsAtStartup(t *testing.T) {
	agents := []agent.Agent{{ID: "devin", APIKeyEnv: "ABSENT"}}

	_, err := agent.ResolveCredentials(agents, nil, testSource(nil, nil))
	if err == nil {
		t.Fatal("expected a configured but unset key variable to fail at startup")
	}
	if !strings.Contains(err.Error(), "ABSENT") {
		t.Fatalf("error should name the variable: %v", err)
	}
}

func TestResolveCredentialsSurfacesAModuleError(t *testing.T) {
	agents := []agent.Agent{{ID: "devin"}}
	broken := &stubModule{id: "devin", err: errors.New("unreadable store")}

	_, err := agent.ResolveCredentials(agents, modules(broken), testSource(nil, nil))
	if err == nil || !strings.Contains(err.Error(), "unreadable store") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCredentialsLeavesAgentsWithoutAKeyAlone(t *testing.T) {
	agents := []agent.Agent{{ID: "opencode"}}

	out, err := agent.ResolveCredentials(agents, nil, testSource(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out[0].HasKey() {
		t.Fatal("an agent with no configured key must stay keyless")
	}
}
