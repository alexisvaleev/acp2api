package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/client"
	"github.com/quonaro/acp2api/internal/version"
)

// connection is one agent process serving many ACP sessions.
type connection struct {
	agent     agent.Agent
	workspace string
	manager   *Manager
	client    *acp.Client

	// createMu serialises session creation on this connection. ACP v1 cannot
	// delete a session, so a lost creation race would orphan one on the agent.
	createMu sync.Mutex

	mu sync.Mutex
	// sessions is indexed by ACP session id: agent callbacks and notifications
	// carry that id, so it is the routing key.
	sessions map[string]*state
	// conversations is indexed by the caller's conversation id and points at the
	// same states, giving a conversation a stable session across calls.
	conversations map[string]*state
	lastUsed      time.Time

	// capabilities is what the agent reported during initialize.
	capabilities connectionCapabilities
	// modelOption is the agent's model selector, captured from session/new. It
	// is the only place the gateway learns which model ids exist.
	modelOption *acp.ConfigOption
}

// connectionCapabilities is the part of the agent's initialize result the
// gateway gates behaviour on.
type connectionCapabilities struct {
	// Images reports whether the agent accepts image prompt content.
	Images bool
}

// connection returns the live connection for an agent and workspace, starting
// one if needed.
func (m *Manager) connection(ctx context.Context, a agent.Agent, workspace string) (*connection, error) {
	key := a.ID + "\x00" + workspace

	// Serialise per key so two concurrent requests cannot spawn two agents.
	lock := m.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("session: manager is closed")
	}
	if c, ok := m.conns[key]; ok && !c.client.Closed() {
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()

	c, err := m.startConnection(a, workspace)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = c.client.Close()
		return nil, errors.New("session: manager is closed")
	}
	m.conns[key] = c
	return c, nil
}

// startConnection spawns an agent and completes the ACP handshake.
func (m *Manager) startConnection(a agent.Agent, workspace string) (*connection, error) {
	c := &connection{
		agent:         a,
		workspace:     workspace,
		manager:       m,
		sessions:      make(map[string]*state),
		conversations: make(map[string]*state),
		lastUsed:      time.Now(),
	}

	cl, err := acp.Start(m.ctx, acp.Options{
		Command: a.Command,
		Args:    a.Args,
		// The agent's own env is layered last, so a per-agent setting wins over
		// the manager's — which is how a proxy or a model override reaches one
		// agent and not the others.
		Env:            buildEnv(m.opts.Env, a.Env),
		Dir:            workspace,
		OnRequest:      c.onRequest,
		OnNotification: c.onNotification,
		RequestTimeout: m.opts.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("session: start agent %q: %w", a.ID, err)
	}
	c.client = cl

	initCtx, cancel := context.WithTimeout(m.ctx, m.opts.RequestTimeout)
	defer cancel()
	initRaw, err := cl.Request(initCtx, acp.MethodInitialize, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersion,
		ClientInfo:         acp.Implementation{Name: version.Name, Version: version.Version},
		ClientCapabilities: a.Capabilities(),
	})
	if err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("session: initialize agent %q: %w", a.ID, err)
	}
	c.capabilities = parseCapabilities(initRaw)

	// authenticate is mandatory before session/new for some agents: the Devin
	// CLI refuses the session with "ACP host has not authenticated" until it is
	// called, even when the CLI itself is already logged in.
	if err := c.authenticate(initCtx, cl, a, initRaw); err != nil {
		_ = cl.Close()
		return nil, err
	}

	slog.With("module", "session").Info("agent ready",
		"agent", a.ID, "pid", cl.PID(), "workspace", workspace, "images", c.capabilities.Images)
	return c, nil
}

// onRequest routes an agent→client request to the owning session's handler.
func (c *connection) onRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var meta struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &meta); err != nil {
		return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: err.Error()}
	}
	st := c.byID(meta.SessionID)
	if st == nil {
		return nil, &acp.Error{
			Code:    acp.CodeInvalidParams,
			Message: fmt.Sprintf("unknown session %q", meta.SessionID),
		}
	}
	return st.handler.Handle(ctx, method, params)
}

// onNotification routes a session/update to the owning session.
func (c *connection) onNotification(method string, params json.RawMessage) {
	if method != acp.MethodSessionUpdate {
		return
	}
	var n acp.SessionUpdateNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return
	}
	if st := c.byID(n.SessionID); st != nil {
		st.deliver(n.Update)
	}
}

// byID looks a session up by its ACP session id.
func (c *connection) byID(sessionID string) *state {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[sessionID]
}

// session returns the session for a conversation, creating it when absent.
//
// The bool reports whether it was created, which is what tells the caller the
// agent holds no history yet and the transcript has to be replayed.
func (c *connection) session(ctx context.Context, conversationID, model, effort string) (*state, bool, error) {
	if st := c.lookup(conversationID); st != nil {
		c.warnEffortOnLiveSession(conversationID, st, model, effort)
		return st, false, nil
	}

	c.createMu.Lock()
	defer c.createMu.Unlock()

	// Re-check: another goroutine may have created it while we waited.
	if st := c.lookup(conversationID); st != nil {
		c.warnEffortOnLiveSession(conversationID, st, model, effort)
		return st, false, nil
	}

	st, err := c.newSession(ctx, model, effort)
	if err != nil {
		return nil, false, err
	}

	c.mu.Lock()
	c.lastUsed = time.Now()
	c.sessions[st.id] = st
	if conversationID != "" {
		c.conversations[conversationID] = st
	}
	c.mu.Unlock()

	// This is the line that ties the caller's conversation key to the agent's
	// session id; every later session line keys off `session` alone.
	attrs := []any{"agent", c.agent.ID, "session", st.id}
	if conversationID != "" {
		attrs = append(attrs, "conversation", conversationID)
	}
	slog.With("module", "session").Info("session opened", attrs...)
	return st, true, nil
}

// warnEffortOnLiveSession reports an effort a live session cannot honour.
//
// A session is created once and keeps its model for its whole life: ACP has no
// way to re-select a variant without disturbing the conversation, and turning
// an existing session into a new one would throw its history away. The effort
// is therefore dropped — which is exactly the kind of silent no-op this gateway
// refuses elsewhere, so it is named in the log rather than left to be inferred
// from a model name the caller cannot see.
//
// It stays a warning rather than an error because the turn itself is still the
// one the caller asked for; only the level is not.
func (c *connection) warnEffortOnLiveSession(conversationID string, st *state, model, effort string) {
	if effort == "" {
		return
	}
	attrs := []any{
		"agent", c.agent.ID, "session", st.id, "model", model, "reasoning_effort", effort,
	}
	if conversationID != "" {
		attrs = append(attrs, "conversation", conversationID)
	}
	slog.With("module", "session").Warn(
		"reasoning_effort applies only when a session is created; the existing session keeps its model",
		attrs...)
}

// lookup returns an existing session for a conversation, touching it.
func (c *connection) lookup(conversationID string) *state {
	if conversationID == "" {
		return nil
	}
	c.mu.Lock()
	st, ok := c.conversations[conversationID]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	st.touch()
	return st
}

// newSession opens one ACP session and its client-side handler.
func (c *connection) newSession(ctx context.Context, model, effort string) (*state, error) {
	raw, err := c.client.Request(ctx, acp.MethodSessionNew, acp.NewSessionRequest{
		Cwd:        c.workspace,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return nil, fmt.Errorf("session: session/new: %w", err)
	}
	var res acp.NewSessionResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("session: decode session/new: %w", err)
	}
	if res.SessionID == "" {
		return nil, errors.New("session: agent returned an empty session id")
	}

	handler, err := client.New(client.Options{
		Workspace:  c.workspace,
		Policy:     c.manager.opts.Policy,
		OnWrite:    c.manager.opts.OnWrite,
		Filesystem: client.Filesystem(c.agent.FilesystemMode()),
	})
	if err != nil {
		return nil, err
	}

	// The catalog is the only place the gateway learns which model ids exist,
	// so it is captured before anything tries to select one.
	c.captureCatalog(res)

	st := newState(res.SessionID, c.client, handler)

	if err := c.applyMode(ctx, res); err != nil {
		return nil, err
	}
	if effort != "" && model == "" {
		// An effort with no family cannot pick a variant: "agent" alone means
		// the default, and guessing a family would silently answer through the
		// wrong model.
		return nil, fmt.Errorf(
			"session: reasoning_effort %q needs a model family; send the model as %q",
			effort, c.agent.ID+"/<model>")
	}
	if model != "" {
		if err := c.selectModel(ctx, res.SessionID, model, effort); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// applyMode selects the agent's session mode when one is configured.
//
// The requested mode is checked against what the agent advertised. Selecting an
// unknown one would otherwise be accepted or quietly ignored, and the operator
// would believe the agent is read-only while it is not — the exact failure this
// gateway refuses to have.
func (c *connection) applyMode(ctx context.Context, session acp.NewSessionResponse) error {
	mode := c.agent.Mode
	if mode == "" {
		return nil
	}

	if session.Modes == nil || len(session.Modes.AvailableModes) == 0 {
		return fmt.Errorf(
			"session: agent %q advertises no session modes, so mode %q cannot be selected",
			c.agent.ID, mode)
	}

	available := make([]string, 0, len(session.Modes.AvailableModes))
	for _, candidate := range session.Modes.AvailableModes {
		if candidate.ID != mode {
			available = append(available, candidate.ID)
			continue
		}
		if _, err := c.client.Request(ctx, acp.MethodSessionSetMode, acp.SetModeRequest{
			SessionID: session.SessionID,
			ModeID:    mode,
		}); err != nil {
			return fmt.Errorf("session: select mode %q on agent %q: %w", mode, c.agent.ID, err)
		}
		slog.With("module", "session").Info("mode selected",
			"agent", c.agent.ID, "session", session.SessionID, "mode", mode)
		return nil
	}

	return fmt.Errorf("session: agent %q does not offer mode %q; available: %s",
		c.agent.ID, mode, strings.Join(available, ", "))
}

// drop removes a session from both indexes and reports the conversation key
// it served, empty when the session was ephemeral.
func (c *connection) drop(st *state) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, st.id)
	var conv string
	for key, s := range c.conversations {
		if s == st {
			conv = key
			delete(c.conversations, key)
		}
	}
	return conv
}

// idleSessions returns the sessions idle beyond ttl that are not mid-turn.
func (c *connection) idleSessions(ttl time.Duration) []*state {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*state
	for _, st := range c.sessions {
		if !st.busy() && st.idleFor() > ttl {
			out = append(out, st)
		}
	}
	return out
}
