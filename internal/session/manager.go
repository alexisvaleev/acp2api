// Package session owns the lifetime of agent processes and ACP sessions, and
// maps an OpenAI conversation onto them.
//
// One agent process serves many sessions: a connection is keyed by agent and
// workspace, and an ACP session is what a conversation maps to. ACP v1 has no
// session deletion, so the process — not the session — is the unit the manager
// can actually reclaim.
package session

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/client"
)

const (
	// defaultRequestTimeout bounds a single ACP request when none is configured.
	defaultRequestTimeout = 120 * time.Second
	// reapInterval is how often idle sessions and connections are swept.
	reapInterval = time.Minute
)

// Options configures a Manager.
type Options struct {
	// Workspace is the default agent working directory.
	Workspace string
	// Policy decides permission requests.
	Policy client.Policy
	// RequestTimeout bounds one ACP request. Zero uses a 120s default.
	RequestTimeout time.Duration
	// SessionTTL closes idle sessions and connections after this. Zero disables
	// reaping, which is only appropriate for short-lived processes.
	SessionTTL time.Duration
	// Env is extra environment for agent processes, on top of the inherited one.
	Env map[string]string
	// OnWrite is notified when an agent writes a file.
	OnWrite func(path, oldContent, newContent string)
}

// Request is one turn to run.
type Request struct {
	// Model is the OpenAI model id: "agent" or "agent/model".
	Model string
	// Effort is the caller's reasoning-effort level (OpenAI reasoning_effort,
	// or reasoning.effort on the Responses surface). It applies only alongside
	// the model part of Model: "agent/family" plus an effort resolves to the
	// catalog's "<family>-<effort>" variant at session creation.
	Effort string
	// ConversationID maps to a persistent ACP session. Empty means an ephemeral
	// session that is not remembered between calls.
	ConversationID string
	// Workspace overrides the manager's default working directory.
	Workspace string
	// Prompt is the user's message for this turn.
	Prompt string
	// Parts, when non-empty, replaces Prompt with structured content blocks.
	// That is how image prompts reach the agent.
	Parts []acp.ContentBlock
	// Replay is the same turn with the whole conversation flattened in front of
	// it, and is used only when the session had to be created for this request.
	//
	// A session the gateway already holds holds the history itself, so replaying
	// would duplicate it. A session created now holds nothing, so sending only
	// the newest turn leaves the agent answering with no context at all — a
	// confident answer to a question nobody asked, with nothing to show that
	// anything went wrong. It is the client that remembers a conversation key
	// across a gateway restart; the gateway does not.
	//
	// Empty means Prompt is already the whole text, which is the case for an
	// ephemeral request and for the legacy completions endpoint.
	Replay string
	// ReplayParts, when set, replaces Parts alongside Replay. The two are always
	// set together: they carry the same content blocks, with Replay's text.
	ReplayParts []acp.ContentBlock
	// OnSession, when set, is notified of the ACP session id as soon as it is
	// known — before the turn's first update — so a caller can tag its own log
	// lines with the session they belong to.
	OnSession func(sessionID string)
}

// replayed swaps in the whole-transcript rendering, for a session that has just
// been created.
func (r Request) replayed() Request {
	if r.Replay == "" {
		return r
	}
	r.Prompt = r.Replay
	r.Parts = r.ReplayParts
	return r
}

// Result describes a completed turn.
type Result struct {
	ConversationID string
	Agent          string
	SessionID      string
	StopReason     string
}

// Manager owns agent processes and ACP sessions.
type Manager struct {
	registry *agent.Registry
	opts     Options

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	conns  map[string]*connection
	closed bool

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// New creates a manager. The returned manager must be closed to release agents.
func New(registry *agent.Registry, opts Options) (*Manager, error) {
	if registry == nil {
		return nil, errors.New("session: registry is required")
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		registry: registry,
		opts:     opts,
		ctx:      ctx,
		cancel:   cancel,
		conns:    make(map[string]*connection),
		locks:    make(map[string]*sync.Mutex),
	}
	if opts.SessionTTL > 0 {
		go m.reap()
	}
	return m, nil
}

// Close terminates every agent process. It is idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conns := m.conns
	m.conns = make(map[string]*connection)
	m.mu.Unlock()

	m.cancel()

	var firstErr error
	for _, c := range conns {
		if err := c.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Agents returns the registered agents, for the model listing.
func (m *Manager) Agents() []agent.Agent { return m.registry.List() }

// Resolve maps an OpenAI model id to an agent and an optional agent-side model,
// so callers can reject an unknown model before starting any work.
func (m *Manager) Resolve(modelID string) (agent.Agent, string, error) {
	return m.registry.Resolve(modelID)
}

// ErrImagesUnsupported reports an agent that did not advertise image prompts.
var ErrImagesUnsupported = errors.New("session: the agent does not accept image prompts")

// Prompt resolves the agent, ensures a connection and a session, and runs one turn.
func (m *Manager) Prompt(ctx context.Context, req Request, onUpdate func(acp.SessionUpdate) error) (Result, error) {
	a, model, err := m.registry.Resolve(req.Model)
	if err != nil {
		return Result{}, err
	}
	// A per-call workspace wins, then the agent's own, then the manager's
	// default. The workspace is half of the connection key, so a request that
	// names a different one gets its own agent process.
	workspace := req.Workspace
	if workspace == "" {
		workspace = a.Workspace
	}
	if workspace == "" {
		workspace = m.opts.Workspace
	}
	if workspace == "" {
		return Result{}, errors.New("session: no workspace configured")
	}

	conn, err := m.connection(ctx, a, workspace)
	if err != nil {
		return Result{}, err
	}
	if hasImages(req.Parts) && !conn.capabilities.Images {
		return Result{}, ErrImagesUnsupported
	}
	st, created, err := conn.session(ctx, req.ConversationID, model, req.Effort)
	if err != nil {
		return Result{}, err
	}
	if req.OnSession != nil {
		req.OnSession(st.id)
	}
	if created {
		// The agent holds no history of its own, so the transcript is the only
		// history this turn will ever see.
		req = req.replayed()
	}
	logTurnPrompt(a.ID, st.id, req, created)

	stop, err := st.run(ctx, req, onUpdate)
	if err != nil {
		return Result{}, err
	}
	return Result{
		ConversationID: req.ConversationID,
		Agent:          a.ID,
		SessionID:      st.id,
		StopReason:     stop,
	}, nil
}

// logTurnPrompt records what the turn sends to the agent, on the session it is
// sent over. A replayed transcript is marked, or the line would read as an
// ordinary single-turn prompt; the text is quoted so a multi-line prompt stays
// one record.
func logTurnPrompt(agentID, sessionID string, req Request, fresh bool) {
	attrs := []any{"agent", agentID, "session", sessionID}
	if req.ConversationID != "" {
		attrs = append(attrs, "conversation", req.ConversationID)
	}
	if fresh && req.Replay != "" {
		attrs = append(attrs, "replayed", true)
	}
	slog.With("module", "turn").Info("prompt",
		append(attrs, "chars", len(req.Prompt), "text", strconv.Quote(req.Prompt))...)
}

// hasImages reports whether any part is an image block.
func hasImages(parts []acp.ContentBlock) bool {
	for _, part := range parts {
		if part.Type == "image" {
			return true
		}
	}
	return false
}

// keyLock returns the mutex guarding one connection key, creating it on demand.
func (m *Manager) keyLock(key string) *sync.Mutex {
	m.locksMu.Lock()
	defer m.locksMu.Unlock()
	l, ok := m.locks[key]
	if !ok {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	return l
}

/* ---- idle reaping ---- */

// reap closes sessions and connections that have been idle beyond SessionTTL.
func (m *Manager) reap() {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.reapOnce()
		}
	}
}

// reapOnce performs one sweep. A connection is closed only when it holds no
// sessions and has itself been idle, so a just-created connection is not
// reclaimed before its first session appears.
func (m *Manager) reapOnce() {
	ttl := m.opts.SessionTTL
	var doomed []*connection

	m.mu.Lock()
	for key, c := range m.conns {
		if c.client.Closed() {
			delete(m.conns, key)
			continue
		}
		for _, st := range c.idleSessions(ttl) {
			attrs := []any{"agent", c.agent.ID, "session", st.id}
			if conv := c.drop(st); conv != "" {
				attrs = append(attrs, "conversation", conv)
			}
			slog.With("module", "session").Info("session expired", attrs...)
		}

		c.mu.Lock()
		empty := len(c.sessions) == 0
		idle := time.Since(c.lastUsed) > ttl
		c.mu.Unlock()

		if empty && idle {
			delete(m.conns, key)
			doomed = append(doomed, c)
		}
	}
	m.mu.Unlock()

	for _, c := range doomed {
		slog.With("module", "session").Info("closing idle agent", "agent", c.agent.ID)
		_ = c.client.Close()
	}
}

// buildEnv layers extra variables over the inherited environment.
//
// Layers merge in order, later winning, and the result is de-duplicated.
// Appending a second HTTPS_PROXY would not override the inherited one: getenv
// returns the first match in environ, so the inherited value would win and the
// override would be lost without a word.
func buildEnv(layers ...map[string]string) []string {
	merged := make(map[string]string)
	order := make([]string, 0, len(os.Environ()))

	put := func(name, value string) {
		if _, seen := merged[name]; !seen {
			order = append(order, name)
		}
		merged[name] = value
	}

	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			put(name, value)
		}
	}
	for _, layer := range layers {
		for name, value := range layer {
			put(name, value)
		}
	}

	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, name+"="+merged[name])
	}
	return out
}
