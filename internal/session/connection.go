package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
		Command:        a.Command,
		Args:           a.Args,
		Env:            buildEnv(m.opts.Env),
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

	slog.Info("session: agent ready",
		"agent", a.ID, "pid", cl.PID(), "workspace", workspace, "images", c.capabilities.Images)
	return c, nil
}

// authenticate selects an auth method when the agent advertises any. An agent
// that advertises none needs no call, which is the common case.
func (c *connection) authenticate(ctx context.Context, cl *acp.Client, a agent.Agent, initRaw json.RawMessage) error {
	var init acp.InitializeResponse
	if err := json.Unmarshal(initRaw, &init); err != nil || len(init.AuthMethods) == 0 {
		return nil
	}

	methodID := a.AuthMethod
	if methodID == "" {
		methodID = init.AuthMethods[0].ID
	}

	request := acp.AuthenticateRequest{MethodID: methodID}
	if a.APIKeyEnv != "" {
		key := os.Getenv(a.APIKeyEnv)
		if key == "" {
			return fmt.Errorf(
				"session: agent %q requires authentication and %s is not set; "+
					"run the agent's own login, or set the variable",
				a.ID, a.APIKeyEnv)
		}
		request.Meta = map[string]any{"api_key": key}
	}

	if _, err := cl.Request(ctx, acp.MethodAuthenticate, request); err != nil {
		return fmt.Errorf(
			"session: authenticate agent %q with method %q: %w "+
				"(if the agent needs a login, run its own login command first)",
			a.ID, methodID, err)
	}
	slog.Debug("session: authenticated", "agent", a.ID, "method", methodID)
	return nil
}

// parseCapabilities reads the agent's prompt capabilities. An agent that says
// nothing is treated as text-only, which is the safe default: sending an image
// to an agent that cannot read it wastes a turn and answers blind.
func parseCapabilities(raw json.RawMessage) connectionCapabilities {
	var init acp.InitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return connectionCapabilities{}
	}
	supported, _ := init.AgentCapabilities.PromptCapabilities["image"].(bool)
	return connectionCapabilities{Images: supported}
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
func (c *connection) session(ctx context.Context, conversationID, model string) (*state, error) {
	if st := c.lookup(conversationID); st != nil {
		return st, nil
	}

	c.createMu.Lock()
	defer c.createMu.Unlock()

	// Re-check: another goroutine may have created it while we waited.
	if st := c.lookup(conversationID); st != nil {
		return st, nil
	}

	st, err := c.newSession(ctx, model)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.lastUsed = time.Now()
	c.sessions[st.id] = st
	if conversationID != "" {
		c.conversations[conversationID] = st
	}
	c.mu.Unlock()
	return st, nil
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
func (c *connection) newSession(ctx context.Context, model string) (*state, error) {
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
		Workspace: c.workspace,
		Policy:    c.manager.opts.Policy,
		OnWrite:   c.manager.opts.OnWrite,
	})
	if err != nil {
		return nil, err
	}

	st := &state{id: res.SessionID, client: c.client, handler: handler, lastUsed: time.Now()}

	// Selecting a model is best-effort: an agent that advertises no model
	// config option still works, it just runs its own default.
	if model != "" {
		if _, err := c.client.Request(ctx, acp.MethodSessionSetConfig, acp.SetConfigOptionRequest{
			SessionID: res.SessionID,
			ConfigID:  "model",
			Value:     model,
		}); err != nil {
			slog.Debug("session: model selection rejected", "agent", c.agent.ID, "model", model, "error", err)
		}
	}
	return st, nil
}

// drop removes a session from both indexes.
func (c *connection) drop(st *state) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, st.id)
	for conv, s := range c.conversations {
		if s == st {
			delete(c.conversations, conv)
		}
	}
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
