package session_test

import (
	"context"
	"log/slog"
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

	// Production resolves credentials once at startup; mirror that here so the
	// session layer sees the same shape it does in the daemon.
	agents, err := agent.ResolveCredentials(registry.List(), nil, agent.OS())
	if err != nil {
		t.Fatalf("resolve credentials: %v", err)
	}
	registry = agent.NewRegistry(agents...)

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

// TestAgentWorkspaceIsUsed covers the layering: an agent that names its own
// directory works there, not in the manager's default.
func TestAgentWorkspaceIsUsed(t *testing.T) {
	managerWorkspace := t.TempDir()
	agentWorkspace := t.TempDir()

	registry := agent.NewRegistry(agent.Agent{
		ID:        "fake",
		Name:      "Fake",
		Command:   os.Args[0],
		Workspace: agentWorkspace,
	})
	m, err := session.New(registry, session.Options{
		Workspace:      managerWorkspace,
		Policy:         client.AllowAll(),
		RequestTimeout: 15 * time.Second,
		Env: map[string]string{
			"ACP2API_FAKE_AGENT":       "1",
			"FAKE_AGENT_WRITE_PATH":    "where.txt",
			"FAKE_AGENT_WRITE_CONTENT": "here",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "write"}, noop); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if _, err := os.Stat(filepath.Join(agentWorkspace, "where.txt")); err != nil {
		t.Fatalf("the agent should have written in its own workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(managerWorkspace, "where.txt")); err == nil {
		t.Fatal("the file landed in the manager's workspace instead")
	}
}

// TestRequestWorkspaceBeatsTheAgents covers the last layer: a per-call override.
func TestRequestWorkspaceBeatsTheAgents(t *testing.T) {
	agentWorkspace := t.TempDir()
	requestWorkspace := t.TempDir()

	registry := agent.NewRegistry(agent.Agent{
		ID: "fake", Name: "Fake", Command: os.Args[0], Workspace: agentWorkspace,
	})
	m, err := session.New(registry, session.Options{
		Workspace:      t.TempDir(),
		Policy:         client.AllowAll(),
		RequestTimeout: 15 * time.Second,
		Env: map[string]string{
			"ACP2API_FAKE_AGENT":       "1",
			"FAKE_AGENT_WRITE_PATH":    "where.txt",
			"FAKE_AGENT_WRITE_CONTENT": "here",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if _, err := m.Prompt(context.Background(), session.Request{
		Model: "fake", Workspace: requestWorkspace, Prompt: "write",
	}, noop); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if _, err := os.Stat(filepath.Join(requestWorkspace, "where.txt")); err != nil {
		t.Fatalf("the per-call workspace should win: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agentWorkspace, "where.txt")); err == nil {
		t.Fatal("the file landed in the agent's workspace instead")
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

// TestAgentRequiringAuthIsAuthenticated covers the handshake order the Devin CLI
// needs: initialize advertises an auth method, and session/new is refused until
// authenticate has been called. Interactive auth is allowed here so the call
// actually happens; the default path is covered below.
func TestAgentRequiringAuthIsAuthenticated(t *testing.T) {
	registry := agent.NewRegistry(agent.Agent{
		ID:               "fake",
		Command:          os.Args[0],
		CredentialSource: agent.CredentialInteractive,
	})
	m, _ := newManager(t, registry, map[string]string{"FAKE_AGENT_REQUIRE_AUTH": "1"})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop); err != nil {
		t.Fatalf("the gateway must authenticate before opening a session: %v", err)
	}
}

// TestConfiguredAPIKeyIsSentToTheAgent is the Devin case: the agent ignores its
// own on-disk login and wants the key in authenticate's _meta.
func TestConfiguredAPIKeyIsSentToTheAgent(t *testing.T) {
	t.Setenv("ACP2API_TEST_KEY", "secret-key-value")

	registry := agent.NewRegistry(agent.Agent{
		ID:        "fake",
		Command:   os.Args[0],
		APIKeyEnv: "ACP2API_TEST_KEY",
	})
	m, _ := newManager(t, registry, map[string]string{
		"FAKE_AGENT_REQUIRE_AUTH":    "1",
		"FAKE_AGENT_REQUIRE_API_KEY": "1",
	})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop); err != nil {
		t.Fatalf("the configured key should have satisfied the agent: %v", err)
	}
}

// TestNoKeyMeansNoInteractiveAuth is the behaviour a daemon needs: without a
// key the gateway must not start an interactive flow, so the agent's own error
// surfaces instead of a browser window.
func TestNoKeyMeansNoInteractiveAuth(t *testing.T) {
	registry := agent.NewRegistry(agent.Agent{ID: "fake", Command: os.Args[0]})
	m, _ := newManager(t, registry, map[string]string{
		"FAKE_AGENT_REQUIRE_AUTH":    "1",
		"FAKE_AGENT_REQUIRE_API_KEY": "1",
	})

	_, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop)
	if err == nil {
		t.Fatal("expected the agent's own authentication error to surface")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("the agent's error should reach the caller, got: %v", err)
	}
}

// TestInteractiveAuthIsOptIn covers an operator explicitly accepting a prompt.
func TestInteractiveAuthIsOptIn(t *testing.T) {
	registry := agent.NewRegistry(agent.Agent{
		ID:               "fake",
		Command:          os.Args[0],
		CredentialSource: agent.CredentialInteractive,
	})
	m, _ := newManager(t, registry, map[string]string{"FAKE_AGENT_REQUIRE_AUTH": "1"})

	if _, err := m.Prompt(context.Background(), session.Request{Model: "fake", Prompt: "hi"}, noop); err != nil {
		t.Fatalf("interactive auth was allowed, so the session should open: %v", err)
	}
}

func replyText(t *testing.T, m *session.Manager, req session.Request) (string, error) {
	t.Helper()
	var reply strings.Builder
	_, err := m.Prompt(context.Background(), req, func(u acp.SessionUpdate) error {
		if u.SessionUpdate == "agent_message_chunk" && u.Content != nil {
			reply.WriteString(u.Content.Text)
		}
		return nil
	})
	return reply.String(), err
}

// TestEffortSelectsFamilyVariant pins the family form: "fake/base" plus
// effort "high" selects the catalog entry "base-high", not the default.
func TestEffortSelectsFamilyVariant(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high,base-high-fast",
		"FAKE_AGENT_ECHO_STATE": "1",
	})
	reply, err := replyText(t, m, session.Request{Model: "fake/base", Effort: "high", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "model=base-high") {
		t.Fatalf("reply = %q, want the base-high variant selected", reply)
	}
}

// TestEffortRewritesLevelKeepingModifier covers a suffixed id: the level word
// is replaced and the trailing speed modifier survives the rewrite.
func TestEffortRewritesLevelKeepingModifier(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "swe-low-fast,swe-high-fast",
		"FAKE_AGENT_ECHO_STATE": "1",
	})
	reply, err := replyText(t, m, session.Request{Model: "fake/swe-low-fast", Effort: "high", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "model=swe-high-fast") {
		t.Fatalf("reply = %q, want the swe-high-fast variant selected", reply)
	}
}

// TestEffortUnknownLevelListsVariants fails loud with the levels the family
// actually advertises, so a mistyped effort is fixable from the error alone.
func TestEffortUnknownLevelListsVariants(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS": "base-low,base-high",
	})
	_, err := replyText(t, m, session.Request{Model: "fake/base", Effort: "xhigh", Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "base-low") {
		t.Fatalf("err = %v, want the family's variants listed", err)
	}
}

// TestEffortNeedsModelFamily rejects an effort on the bare agent id: with no
// family there is no variant to resolve.
func TestEffortNeedsModelFamily(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), nil)
	_, err := replyText(t, m, session.Request{Model: "fake", Effort: "high", Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("err = %v, want a family-required error", err)
	}
}

// TestEffortIsCaseInsensitive keeps the wire liberal: "High" means "high".
func TestEffortIsCaseInsensitive(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high",
		"FAKE_AGENT_ECHO_STATE": "1",
	})
	reply, err := replyText(t, m, session.Request{Model: "fake/base", Effort: "High", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "model=base-high") {
		t.Fatalf("reply = %q, want the base-high variant selected", reply)
	}
}

// TestFamilyWithoutEffortPicksDefault pins the omitted-effort path: a bare
// family id selects its mid-tier variant instead of failing on no exact match.
func TestFamilyWithoutEffortPicksDefault(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high,base-max",
		"FAKE_AGENT_ECHO_STATE": "1",
	})
	reply, err := replyText(t, m, session.Request{Model: "fake/base", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(reply, "model=base-high") {
		t.Fatalf("reply = %q, want the base-high default selected", reply)
	}
}

// TestVariantListIsTheFamilyOnly is the regression guard for a variant list
// built from a bare string prefix: "base" must not claim "base-2-high" or
// "basex-high", or a caller fixing a mistyped effort is sent to a different
// model's ids.
func TestVariantListIsTheFamilyOnly(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		// base-high is the family's own level. base-2-high and basex-high share
		// its prefix but belong to other models; base-fast is a modifier with no
		// level behind it, so it is not a variant of base either.
		"FAKE_AGENT_MODELS": "base-2-high,base-fast,base-high,basex-high,base-none",
	})
	_, err := replyText(t, m, session.Request{Model: "fake/base", Effort: "medium", Prompt: "hi"})
	if err == nil {
		t.Fatal("an effort the family does not offer must fail")
	}
	for _, want := range []string{"base-high", "base-none"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %s listed as a variant", err, want)
		}
	}
	for _, unwanted := range []string{"base-2-high", "basex-high", "base-fast"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Fatalf("err = %q, must not offer %s from another model", err, unwanted)
		}
	}
}

// TestVariantListPreservesModifiers keeps the rewrite discoverable: the error
// that lists variants has to show the modifier spellings the effort swap can
// produce, or the caller cannot tell which ids the swap will accept.
func TestVariantListPreservesModifiers(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS": "swe-low,swe-max-fast",
	})
	_, err := replyText(t, m, session.Request{Model: "fake/swe", Effort: "xhigh", Prompt: "hi"})
	if err == nil {
		t.Fatal("xhigh is not offered by this family")
	}
	if !strings.Contains(err.Error(), "swe-max-fast") {
		t.Fatalf("err = %q, want the modifier spelling swe-max-fast listed", err)
	}
}

// TestNoVariantsIsSaidInWords covers the empty case: a family with no variants
// at all must not answer with an empty "variants:" list, and must not offer the
// id the caller already sent back as though it were an alternative.
func TestNoVariantsIsSaidInWords(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS": "alpha,beta",
	})
	_, err := replyText(t, m, session.Request{Model: "fake/alpha", Effort: "high", Prompt: "hi"})
	if err == nil {
		t.Fatal("an unknown effort on a family with no variants must fail")
	}
	if !strings.Contains(err.Error(), "no effort variants") {
		t.Fatalf("err = %q, want the no-variants sentence", err)
	}
	if strings.Contains(err.Error(), "variants: alpha") {
		t.Fatalf("err = %q, must not echo the requested id as a variant", err)
	}
}

// TestEffortOnLiveConversationIsReported pins the one case a session cannot
// honour: a session keeps the model it was created with, so a later turn that
// changes the effort is dropped — and has to say so in the log rather than pass
// silently.
func TestEffortOnLiveConversationIsReported(t *testing.T) {
	var logged strings.Builder
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high",
		"FAKE_AGENT_ECHO_STATE": "1",
	})

	first, err := replyText(t, m, session.Request{
		Model: "fake/base", Effort: "low", ConversationID: "c1", Prompt: "hi"})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if !strings.Contains(first, "model=base-low") {
		t.Fatalf("first reply = %q, want base-low", first)
	}

	logged.Reset()
	second, err := replyText(t, m, session.Request{
		Model: "fake/base", Effort: "high", ConversationID: "c1", Prompt: "again"})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	// The session cannot change model, so the level is dropped; that is
	// deliberate, but it must be visible.
	if !strings.Contains(logged.String(), "reasoning_effort") {
		t.Fatalf("log = %q, want the dropped effort named", logged.String())
	}
	if !strings.Contains(second, "model=base-low") {
		t.Fatalf("second reply = %q, the live session keeps base-low", second)
	}
}

// TestEffortAbsentOnLiveConversationIsQuiet keeps the warning rare: an ordinary
// follow-up turn that names no effort must not log an ignored one.
func TestEffortAbsentOnLiveConversationIsQuiet(t *testing.T) {
	var logged strings.Builder
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high",
		"FAKE_AGENT_ECHO_STATE": "1",
	})
	if _, err := replyText(t, m, session.Request{
		Model: "fake/base", Effort: "low", ConversationID: "c2", Prompt: "hi"}); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	logged.Reset()
	if _, err := replyText(t, m, session.Request{
		Model: "fake/base", ConversationID: "c2", Prompt: "again"}); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if strings.Contains(logged.String(), "reasoning_effort") {
		t.Fatalf("log = %q, want no dropped-effort warning without an effort", logged.String())
	}
}
