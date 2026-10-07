package handler

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/openai"
)

// Tool-call sources, shown in the log module as "tool_calling:<source>". The
// three are distinct because a tool can live in three different places, and the
// gateway is only responsible for one of them.
const (
	// toolExternal is an MCP server wired into the ACP client itself.
	toolExternal = "external"
	// toolInternal is one of the agent's own built-in tools.
	toolInternal = "internal"
	// toolFromREST is a caller-declared function the gateway relays over REST.
	toolFromREST = "from rest"
)

// mcpNamespace is the prefix an ACP client gives its MCP tools:
// mcp__<server>__<tool>. The agent's own tools (exec, edit, …) do not carry it.
const mcpNamespace = "mcp__"

// toolModule names the log subsystem for a tool-call source, so the logger
// renders it as [tool_calling:external] and the like.
func toolModule(source string) string { return "tool_calling:" + source }

// isMCPTool reports whether a tool call targets an MCP server the ACP client has
// wired in, rather than one of the agent's own tools.
//
// The programmatic name is authoritative; an agent that reports none leaves the
// human-readable title as the only signal.
func isMCPTool(name, title string) bool {
	if name != "" {
		return strings.HasPrefix(name, mcpNamespace)
	}
	return strings.Contains(title, mcpNamespace)
}

// turnLog logs one turn's traffic: the tool calls on the way, from all three
// sources, and the agent's reply. It remembers where each agent call came
// from.
//
// The memory matters because a `tool_call_update` is a patch keyed by id: it
// usually omits the name and title, so without the initial call's classification
// an update to an MCP call would read as an internal one.
type turnLog struct {
	log     *slog.Logger
	sources map[string]string
}

// newTurnLog creates the per-turn logger, tagged with the caller's
// conversation key when one was supplied.
func newTurnLog(log *slog.Logger, conversation string) *turnLog {
	if conversation != "" {
		log = log.With("conversation", conversation)
	}
	return &turnLog{log: log, sources: make(map[string]string)}
}

// bind attaches the ACP session id once the manager reports it — the same id
// the response's acp.session_id carries, so a log line correlates end to end.
func (t *turnLog) bind(sessionID string) {
	t.log = t.log.With("session", sessionID)
}

// answered logs the agent's reply for the turn. The text is quoted so a
// multi-line answer still occupies one record.
func (t *turnLog) answered(text, stop string) {
	t.log.With("module", "turn").Info("reply",
		"stop", stop, "chars", len(text), "text", strconv.Quote(text))
}

// update maps one ACP session update onto the OpenAI-facing view, logging any
// tool call it carries on the way through.
func (t *turnLog) update(u acp.SessionUpdate) (string, string, *openai.Step) {
	t.logAgentCall(u)
	return openai.FromUpdate(u)
}

// logAgentCall records one agent-reported tool call or its progress update.
//
// Agent tool calls are the agent's own: either an MCP server the ACP client has
// wired in ([tool_calling:external]) or one of the agent's built-in tools
// ([tool_calling:internal]). Everything is logged at debug level, arguments
// included, because a tool call is the agent's work rather than the answer.
func (t *turnLog) logAgentCall(u acp.SessionUpdate) {
	if u.SessionUpdate != acp.UpdateToolCall && u.SessionUpdate != acp.UpdateToolCallUpdate {
		return
	}
	attrs := []any{
		"module", toolModule(t.source(u)),
		"tool_call_id", u.ToolCallID,
		"name", u.Name,
		"title", u.Title,
		"kind", u.Kind,
		"status", u.Status,
	}
	if len(u.RawInput) > 0 {
		attrs = append(attrs, "input", string(u.RawInput))
	}
	if len(u.RawOutput) > 0 {
		attrs = append(attrs, "output", string(u.RawOutput))
	}
	t.log.Debug(u.SessionUpdate, attrs...)
}

// source classifies a tool call, remembering the answer for later updates.
//
// A patch that carries neither a name nor a title inherits the classification
// its initial tool_call established; one that carries either is reclassified, so
// an agent that fills the name in on a later update is still believed.
func (t *turnLog) source(u acp.SessionUpdate) string {
	if u.Name == "" && u.Title == "" {
		if source, ok := t.sources[u.ToolCallID]; ok {
			return source
		}
		return toolInternal
	}
	source := toolInternal
	if isMCPTool(u.Name, u.Title) {
		source = toolExternal
	}
	t.sources[u.ToolCallID] = source
	return source
}

// callerCalls records the tool calls the gateway hands back to the caller over
// REST. They are the caller's own functions, not the agent's tools, so they are
// logged under their own source.
func (t *turnLog) callerCalls(calls []openai.ToolCall) {
	for _, call := range calls {
		t.log.Debug("tool_call",
			"module", toolModule(toolFromREST),
			"tool_call_id", call.ID,
			"name", call.Function.Name,
			"arguments", call.Function.Arguments,
		)
	}
}
