package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/client"
	"github.com/quonaro/acp2api/internal/fakeagent"
	"github.com/quonaro/acp2api/internal/session"
)

func TestMain(m *testing.M) {
	if fakeagent.MaybeRun() {
		return
	}
	os.Exit(m.Run())
}

func noop(acp.SessionUpdate) error { return nil }

// fakeRegistry registers the test binary itself as the "fake" agent.
func fakeRegistry() *agent.Registry {
	return agent.NewRegistry(agent.Agent{ID: "fake", Name: "Fake", Command: os.Args[0]})
}

func newManager(t *testing.T, registry *agent.Registry, env map[string]string) (*session.Manager, string) {
	t.Helper()
	workspace := t.TempDir()
	merged := map[string]string{"ACP2API_FAKE_AGENT": "1"}
	for k, v := range env {
		merged[k] = v
	}
	m, err := session.New(registry, session.Options{
		Workspace:      workspace,
		Policy:         client.AllowAll(),
		RequestTimeout: 15 * time.Second,
		Env:            merged,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, workspace
}

func TestPromptStreamsUpdates(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{"FAKE_AGENT_CHUNKS": "3"})

	var kinds []string
	res, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, func(u acp.SessionUpdate) error {
		kinds = append(kinds, u.SessionUpdate)
		return nil
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.StopReason != acp.StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", res.StopReason, acp.StopEndTurn)
	}
	if res.Agent != "fake" || res.SessionID == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(kinds) != 3 {
		t.Fatalf("got %d updates, want 3", len(kinds))
	}
}

func TestConversationReusesItsSession(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), nil)
	ctx := context.Background()

	first, err := m.Prompt(ctx, session.Request{Model: "fake", ConversationID: "c1", Prompt: "one"}, noop)
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	second, err := m.Prompt(ctx, session.Request{Model: "fake", ConversationID: "c1", Prompt: "two"}, noop)
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("conversation reused %q then %q, want the same session", first.SessionID, second.SessionID)
	}

	ephemeral, err := m.Prompt(ctx, session.Request{Model: "fake", Prompt: "three"}, noop)
	if err != nil {
		t.Fatalf("ephemeral prompt: %v", err)
	}
	if ephemeral.SessionID == first.SessionID {
		t.Fatalf("ephemeral request reused session %q, want a fresh one", ephemeral.SessionID)
	}
}

func TestUnknownModelIsRejected(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), nil)
	if _, err := m.Prompt(context.Background(), session.Request{Model: "nope", Prompt: "x"}, noop); err == nil {
		t.Fatal("expected an unknown model id to fail")
	}
}

func TestAgentWriteLandsInsideWorkspace(t *testing.T) {
	m, workspace := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_WRITE_PATH":    "out.txt",
		"FAKE_AGENT_WRITE_CONTENT": "written",
	})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "write"}, noop); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "out.txt"))
	if err != nil {
		t.Fatalf("agent write did not land in the workspace: %v", err)
	}
	if string(data) != "written" {
		t.Fatalf("file content = %q", data)
	}
}

func TestAgentWriteOutsideWorkspaceIsRefused(t *testing.T) {
	m, workspace := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_WRITE_PATH":    "../escape.txt",
		"FAKE_AGENT_WRITE_CONTENT": "nope",
	})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "escape"}, noop); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	escaped := filepath.Join(filepath.Dir(workspace), "escape.txt")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("agent escaped the workspace and wrote %s", escaped)
	}
}

func TestOnWriteIsReported(t *testing.T) {
	workspace := t.TempDir()
	merged := map[string]string{
		"ACP2API_FAKE_AGENT":       "1",
		"FAKE_AGENT_WRITE_PATH":    "tracked.txt",
		"FAKE_AGENT_WRITE_CONTENT": "body",
	}

	var gotPath, gotNew string
	m, err := session.New(fakeRegistry(), session.Options{
		Workspace:      workspace,
		Policy:         client.AllowAll(),
		RequestTimeout: 15 * time.Second,
		Env:            merged,
		OnWrite: func(path, _, newContent string) {
			gotPath, gotNew = path, newContent
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "write"}, noop); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if gotNew != "body" {
		t.Fatalf("OnWrite new content = %q", gotNew)
	}
	if gotPath != filepath.Join(workspace, "tracked.txt") {
		t.Fatalf("OnWrite path = %q", gotPath)
	}
}

func TestMissingAgentBinaryIsReported(t *testing.T) {
	registry := agent.NewRegistry(agent.Agent{ID: "missing", Command: "/nonexistent/agent-binary"})
	m, _ := newManager(t, registry, nil)

	_, err := m.Prompt(context.Background(), session.Request{Model: "missing", Prompt: "x"}, noop)
	if err == nil {
		t.Fatal("expected a missing agent binary to fail")
	}
}

// TestAgentRequiringAuthIsAuthenticated covers the flow the Devin CLI needs:
// initialize advertises an auth method, and session/new is refused until
// authenticate has been called.
func TestAgentRequiringAuthIsAuthenticated(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{"FAKE_AGENT_REQUIRE_AUTH": "1"})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop); err != nil {
		t.Fatalf("the gateway must authenticate before opening a session: %v", err)
	}
}

func TestMissingAPIKeyEnvIsReportedClearly(t *testing.T) {
	registry := agent.NewRegistry(agent.Agent{
		ID:        "fake",
		Command:   os.Args[0],
		APIKeyEnv: "ACP2API_TEST_ABSENT_KEY",
	})
	m, _ := newManager(t, registry, map[string]string{"FAKE_AGENT_REQUIRE_AUTH": "1"})

	_, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop)
	if err == nil {
		t.Fatal("expected a missing api key variable to be reported")
	}
	if !strings.Contains(err.Error(), "ACP2API_TEST_ABSENT_KEY") {
		t.Fatalf("error should name the missing variable: %v", err)
	}
}
