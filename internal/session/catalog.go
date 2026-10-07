package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/quonaro/acp2api/internal/acp"
)

// ModelInfo is one model an agent advertises.
type ModelInfo struct {
	// ID is the value to pass as the model half of "agent/model".
	ID string
	// Name is the agent's own label for it.
	Name string
	// Description is optional extra text from the agent.
	Description string
}

// maxListedModels caps how many ids an error message lists. Some agents
// advertise over a hundred, and an error nobody can read is not an error
// message.
const maxListedModels = 20

// captureCatalog records the agent's model option from session/new.
//
// The catalog is the same for every session on a connection, so the first
// session captures it. It is the only place the gateway learns which model ids
// exist — ACP has no separate discovery call.
func (c *connection) captureCatalog(session acp.NewSessionResponse) {
	for _, option := range session.ConfigOptions {
		if !isModelOption(option) {
			continue
		}
		c.mu.Lock()
		first := c.modelOption == nil
		c.modelOption = &option
		c.mu.Unlock()
		// The catalog is a property of the connection, not the session, so only
		// the first capture is worth a line — the rest would repeat it verbatim.
		if first {
			slog.With("module", "session").Debug("model catalog captured",
				"agent", c.agent.ID, "session", session.SessionID, "option", option.ID,
				"models", len(option.Options), "current", option.CurrentValue,
				"sample", sampleValues(option.Options))
		}
		return
	}
}

// sampleValues renders the first few option values for a debug line.
func sampleValues(options []acp.ConfigOptionValue) string {
	limit := min(3, len(options))
	parts := make([]string, 0, limit)
	for _, option := range options[:limit] {
		parts = append(parts, option.Value)
	}
	return strings.Join(parts, ",")
}

// isModelOption reports whether a config option is the model selector.
func isModelOption(option acp.ConfigOption) bool {
	if option.Category == "model" {
		return true
	}
	// Fall back to the id, and never mistake the mode or thought-level selects
	// for the model one.
	if option.ID == "mode" || option.Category != "" {
		return false
	}
	return strings.Contains(option.ID, "model")
}

// Catalog returns the agent's advertised models, or nil when it has not been
// discovered yet — the catalog only exists once a session is open.
func (c *connection) Catalog() []ModelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return modelInfos(c.modelOption)
}

// modelInfos renders a model option as a sorted list.
func modelInfos(option *acp.ConfigOption) []ModelInfo {
	if option == nil {
		return nil
	}
	out := make([]ModelInfo, 0, len(option.Options))
	for _, value := range option.Options {
		if strings.TrimSpace(value.Value) == "" {
			continue
		}
		name := value.Name
		if name == "" {
			name = value.Value
		}
		out = append(out, ModelInfo{ID: value.Value, Name: name, Description: value.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// levelWords are the trailing id components a catalog uses for effort levels:
// "claude-opus-5-max" is family "claude-opus-5" at level "max".
var levelWords = map[string]bool{
	"none": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// modifierWords are trailing id components independent of effort — speed or
// context variants such as "-fast", "-priority", "-1m" — preserved when an
// effort swap rewrites the level in front of them.
var modifierWords = map[string]bool{"fast": true, "priority": true, "1m": true}

// splitVariant peels at most one level word and any modifiers off the end of a
// catalog id, so "claude-opus-5-max-fast" splits as base "claude-opus-5",
// level "max", modifier "fast". An id with no recognisable suffix is its own
// base.
func splitVariant(id string) (base, level, modifier string) {
	parts := strings.Split(id, "-")
	for len(parts) > 0 {
		last := parts[len(parts)-1]
		switch {
		case level == "" && levelWords[last]:
			level = last
		case modifier == "" && modifierWords[last]:
			modifier = last
		default:
			return strings.Join(parts, "-"), level, modifier
		}
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "-"), level, modifier
}

// defaultEffortOrder is the level preference for a family id the caller named
// without an effort: "devin/swe-2" selects swe-2-high, the family's mid-tier
// default, the same way omitting reasoning_effort defers to a provider's own.
var defaultEffortOrder = []string{"high", "medium", "max", "xhigh", "low", "minimal", "none"}

// modelCandidates lists the catalog ids a request may mean, most specific
// first. Without an effort the id itself leads, then the family defaults in
// defaultEffortOrder, so a bare family name keeps working. With an effort the
// family form "model-effort" leads — "swe-2" + "max" tries "swe-2-max" —
// followed by the suffixed-id rewrite that keeps a trailing modifier:
// "gpt-6-sol-max-priority" + "high" tries "gpt-6-sol-high-priority" before
// "gpt-6-sol-high".
func modelCandidates(model, effort string) []string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		out := []string{model}
		for _, level := range defaultEffortOrder {
			out = append(out, model+"-"+level)
		}
		return out
	}

	base, _, modifier := splitVariant(model)
	rewritten := base + "-" + effort
	if modifier != "" {
		rewritten += "-" + modifier
	}

	seen := map[string]bool{}
	out := []string{}
	for _, candidate := range []string{model + "-" + effort, rewritten, base + "-" + effort} {
		if !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

// familyVariants lists the catalog entries under one family prefix, for the
// error an unknown effort produces: the caller learns the levels that exist
// rather than the whole catalog.
func familyVariants(option *acp.ConfigOption, model string) []string {
	base, _, _ := splitVariant(model)
	variants := []string{}
	for _, candidate := range option.Options {
		if candidate.Value == base || strings.HasPrefix(candidate.Value, base+"-") {
			variants = append(variants, candidate.Value)
		}
	}
	sort.Strings(variants)
	return variants
}

// selectModel applies a requested model to a session.
//
// An unknown id is an error, never a silent no-op: the caller asked for a
// specific model, and running the agent's default instead would answer a
// question nobody asked. The error lists what the agent does offer.
func (c *connection) selectModel(ctx context.Context, sessionID, model, effort string) error {
	c.mu.Lock()
	option := c.modelOption
	c.mu.Unlock()

	if option == nil {
		return fmt.Errorf(
			"session: agent %q advertises no model to select, so model %q cannot be honoured",
			c.agent.ID, model)
	}

	candidates := modelCandidates(model, effort)
	for _, want := range candidates {
		for _, candidate := range option.Options {
			if candidate.Value != want {
				continue
			}
			if _, err := c.client.Request(ctx, acp.MethodSessionSetConfig, acp.SetConfigOptionRequest{
				SessionID: sessionID,
				ConfigID:  option.ID,
				Value:     want,
			}); err != nil {
				return fmt.Errorf("session: select model %q on agent %q: %w", want, c.agent.ID, err)
			}
			slog.With("module", "session").Debug("model selected",
				"agent", c.agent.ID, "session", sessionID, "model", want)
			return nil
		}
	}

	if effort != "" {
		return fmt.Errorf("session: agent %q model %q has no effort %q; variants: %s",
			c.agent.ID, model, effort, strings.Join(familyVariants(option, model), ", "))
	}
	return fmt.Errorf("session: agent %q has no model %q; %s",
		c.agent.ID, model, describeModels(modelInfos(option)))
}

// describeModels renders the available ids for an error message.
func describeModels(models []ModelInfo) string {
	if len(models) == 0 {
		return "it advertises no models"
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if len(ids) <= maxListedModels {
		return "available: " + strings.Join(ids, ", ")
	}
	return fmt.Sprintf("available: %s and %d more",
		strings.Join(ids[:maxListedModels], ", "), len(ids)-maxListedModels)
}

// Models returns the models an agent has advertised, or nil when none of its
// connections has opened a session yet.
//
// The catalog is discovered, not configured: it comes from the agent, and only
// exists once the agent has been talked to. An agent with no live connection is
// simply absent from the result rather than costing a cold start.
func (m *Manager) Models(agentID string) []ModelInfo {
	m.mu.Lock()
	conns := make([]*connection, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()

	for _, c := range conns {
		if c.agent.ID != agentID {
			continue
		}
		if catalog := c.Catalog(); len(catalog) > 0 {
			return catalog
		}
	}
	return nil
}

// Discover opens a connection to every registered agent so its model catalog
// becomes known, then leaves the processes to the idle reaper.
//
// It is best-effort and meant to run in the background: an agent that cannot
// start is logged and skipped, because failing to enumerate models is not a
// reason to refuse the requests that do not name one.
func (m *Manager) Discover(ctx context.Context) {
	for _, a := range m.registry.List() {
		if ctx.Err() != nil {
			return
		}
		if len(m.Models(a.ID)) > 0 {
			continue
		}
		workspace := a.Workspace
		if workspace == "" {
			workspace = m.opts.Workspace
		}
		conn, err := m.connection(ctx, a, workspace)
		if err != nil {
			slog.With("module", "session").Warn("model discovery failed", "agent", a.ID, "error", err)
			continue
		}
		if err := conn.discoverCatalog(ctx); err != nil {
			slog.With("module", "session").Warn("model discovery failed", "agent", a.ID, "error", err)
			continue
		}
		slog.With("module", "session").Info("models discovered", "agent", a.ID, "count", len(m.Models(a.ID)))
	}
}

// discoverCatalog opens and closes one session so the agent advertises its
// catalog, without running a turn.
func (c *connection) discoverCatalog(ctx context.Context) error {
	if len(c.Catalog()) > 0 {
		return nil
	}
	c.createMu.Lock()
	defer c.createMu.Unlock()
	if len(c.Catalog()) > 0 {
		return nil
	}

	raw, err := c.client.Request(ctx, acp.MethodSessionNew, acp.NewSessionRequest{
		Cwd:        c.workspace,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var res acp.NewSessionResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decode session/new: %w", err)
	}
	c.captureCatalog(res)
	return nil
}
