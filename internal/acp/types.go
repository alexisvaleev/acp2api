package acp

import "encoding/json"

// ProtocolVersion is the ACP wire version this client speaks.
const ProtocolVersion = 1

// Method names, in both directions. Kept as constants so a typo is a compile
// error rather than a mysterious -32601 from the agent.
const (
	MethodInitialize       = "initialize"
	MethodAuthenticate     = "authenticate"
	MethodSessionNew       = "session/new"
	MethodSessionLoad      = "session/load"
	MethodSessionPrompt    = "session/prompt"
	MethodSessionCancel    = "session/cancel"
	MethodSessionSetMode   = "session/set_mode"
	MethodSessionSetConfig = "session/set_config_option"
	MethodSessionUpdate    = "session/update"
	MethodRequestPerm      = "session/request_permission"
	MethodReadTextFile     = "fs/read_text_file"
	MethodWriteTextFile    = "fs/write_text_file"
	MethodTerminalCreate   = "terminal/create"
	MethodTerminalOutput   = "terminal/output"
	MethodTerminalWaitExit = "terminal/wait_for_exit"
	MethodTerminalKill     = "terminal/kill"
	MethodTerminalRelease  = "terminal/release"
	MethodElicitation      = "elicitation/create"
)

// session/update discriminators.
const (
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
	UpdateAvailableCommands = "available_commands_update"
	UpdateCurrentMode       = "current_mode_update"
	UpdateConfigOption      = "config_option_update"
)

// Stop reasons returned by session/prompt.
const (
	StopEndTurn   = "end_turn"
	StopMaxTokens = "max_tokens"
	StopMaxTurns  = "max_turn_requests"
	StopRefusal   = "refusal"
	StopCancelled = "cancelled"
)

// Permission option kinds. The client policy maps these to allow/deny.
const (
	PermAllowOnce    = "allow_once"
	PermAllowAlways  = "allow_always"
	PermRejectOnce   = "reject_once"
	PermRejectAlways = "reject_always"
)

/* ---- content ---- */

// ContentBlock is one entry of a prompt or of a session update's content.
// Only the text variant is produced by this gateway; the rest are decoded so
// they can be passed through to the OpenAI layer without loss.
type ContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MimeType string          `json:"mimeType,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
}

// TextBlock builds a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

/* ---- lifecycle ---- */

// Implementation identifies a client or agent.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Title   string `json:"title,omitempty"`
}

// InitializeRequest is the first message sent to an agent.
type InitializeRequest struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientInfo         Implementation `json:"clientInfo"`
	ClientCapabilities map[string]any `json:"clientCapabilities,omitempty"`
}

// AgentCapabilities is what the agent reports it can do.
type AgentCapabilities struct {
	LoadSession        bool           `json:"loadSession,omitempty"`
	PromptCapabilities map[string]any `json:"promptCapabilities,omitempty"`
	Meta               map[string]any `json:"_meta,omitempty"`
}

// AuthMethod is one advertised way to authenticate the agent.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// AuthenticateRequest selects an advertised auth method.
//
// Some agents require it before a session can be opened — the Devin CLI refuses
// session/new with "ACP host has not authenticated" until this call is made,
// even when the CLI itself is already logged in. Meta carries vendor options,
// such as an API key for a headless login.
type AuthenticateRequest struct {
	MethodID string         `json:"methodId"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

// InitializeResponse is the agent's handshake reply.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
}

/* ---- sessions ---- */

// EnvVar is one environment entry, in the ACP shape.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// McpServer declares an MCP server for a session. The gateway passes an empty
// list: agents that need MCP read it from their own config, and a server the
// agent cannot resolve makes the handshake fail.
type McpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []EnvVar `json:"env,omitempty"`
}

// NewSessionRequest opens a conversation rooted at Cwd.
type NewSessionRequest struct {
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// SessionMode is one selectable agent mode (e.g. plan, build).
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState is the agent's current mode and the available ones.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// ConfigOptionValue is one value of a config select.
type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
}

// ConfigOption is an agent-advertised setting, such as the model catalog.
type ConfigOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type,omitempty"`
	CurrentValue any                 `json:"currentValue,omitempty"`
	Options      []ConfigOptionValue `json:"options,omitempty"`
}

// NewSessionResponse is the agent's reply to session/new.
type NewSessionResponse struct {
	SessionID     string            `json:"sessionId"`
	Modes         *SessionModeState `json:"modes,omitempty"`
	ConfigOptions []ConfigOption    `json:"configOptions,omitempty"`
}

// PromptRequest sends one user turn.
type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptResponse reports why the turn ended.
type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

// CancelNotification asks the agent to stop the current turn.
type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// SetConfigOptionRequest sets an agent-advertised config value, such as the
// model. Not every agent implements it; the gateway treats a failure as
// best-effort so an agent without config options still works.
type SetConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// SetModeRequest switches the agent's session mode (e.g. plan, build).
type SetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

/* ---- session updates ---- */

// PlanEntry is one step of an agent plan.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// ToolCall is a tool invocation reported by the agent.
type ToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Locations  json.RawMessage `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
}

// SessionUpdate is one session/update payload. Unknown fields are preserved in
// Raw so vendor extensions survive a round trip through the mapping layer.
type SessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       *ContentBlock   `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	RawOutput     json.RawMessage `json:"rawOutput,omitempty"`
	Entries       []PlanEntry     `json:"entries,omitempty"`
	ConfigOptions []ConfigOption  `json:"configOptions,omitempty"`
	CurrentModeID string          `json:"currentModeId,omitempty"`

	// Raw is the exact payload as received, excluding nothing.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the known fields and keeps the raw payload.
func (u *SessionUpdate) UnmarshalJSON(data []byte) error {
	type plain SessionUpdate
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*u = SessionUpdate(p)
	u.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// SessionUpdateNotification wraps a session/update notification.
type SessionUpdateNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

/* ---- permissions ---- */

// PermissionOption is one choice offered to the client.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
}

// RequestPermissionRequest asks the client to allow or deny a tool call.
type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCall           `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome is the selected option, or a cancellation.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// RequestPermissionResponse answers a permission request.
type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

/* ---- filesystem ---- */

// ReadTextFileRequest asks the client to read a file for the agent.
type ReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

// ReadTextFileResponse carries the file content.
type ReadTextFileResponse struct {
	Content string `json:"content"`
}

// WriteTextFileRequest asks the client to write a file for the agent.
type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

/* ---- terminals ---- */

// CreateTerminalRequest asks the client to run a command.
type CreateTerminalRequest struct {
	SessionID       string   `json:"sessionId"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Env             []EnvVar `json:"env,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	OutputByteLimit *int     `json:"outputByteLimit,omitempty"`
}

// CreateTerminalResponse returns the handle for the new terminal.
type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}
