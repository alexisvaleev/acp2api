// Package openai defines the OpenAI-compatible wire types and the mapping
// between them and ACP.
//
// The mapping is deliberately honest about what does not translate: an agent
// session, its permission decisions, and its tool activity have no OpenAI
// equivalent, so they travel in a namespaced `acp` field that clients are free
// to ignore.
package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Object type strings, as the OpenAI API spells them.
const (
	ObjectChatCompletion      = "chat.completion"
	ObjectChatCompletionChunk = "chat.completion.chunk"
	ObjectModel               = "model"
	ObjectList                = "list"
)

/* ---- requests ---- */

// ChatCompletionRequest is the subset of the OpenAI request this gateway
// understands. Sampling parameters are accepted and ignored: the agent owns its
// own model configuration, and silently pretending otherwise would be a lie.
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`

	// StreamOptions controls whether a usage block is emitted on the stream.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	// ConversationID is this gateway's extension. When set, the turn runs on a
	// persistent ACP session, so the agent keeps context between calls.
	ConversationID string `json:"conversation_id,omitempty"`

	// User is the standard OpenAI end-user field, accepted as a weaker
	// conversation key for clients that cannot set a custom field.
	User string `json:"user,omitempty"`

	// Workspace is this gateway's extension: the agent's working directory.
	// Empty means the server's configured default.
	Workspace string `json:"workspace,omitempty"`
}

// StreamOptions mirrors the OpenAI field.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Message is one entry of the request's message list.
type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// Text renders the message content as plain text. Both the string form and the
// array-of-parts form are accepted; non-text parts are summarised, never
// dropped silently.
func (m Message) Text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		out := ""
		for _, p := range parts {
			switch {
			case p.Text != "":
				out += p.Text
			case p.Type != "":
				out += fmt.Sprintf("[%s]", p.Type)
			}
		}
		return out
	}
	return ""
}

/* ---- responses ---- */

// ChatCompletionResponse is a non-streaming completion.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
	ACP     *ACPMeta `json:"acp,omitempty"`
}

// Choice is one completion candidate. This gateway always returns exactly one.
type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

// ResponseMessage is the assistant's reply.
type ResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is a token estimate. Agents do not report token counts, so these are
// approximations and are labelled as such in the docs.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionChunk is one server-sent event of a streaming completion.
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
	ACP     *ACPMeta      `json:"acp,omitempty"`
}

// ChunkChoice is one streaming choice delta.
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// Delta is an incremental assistant message.
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

/* ---- ACP extension ---- */

// ACPMeta carries the ACP-specific detail that has no OpenAI equivalent.
// It is additive: a client that ignores it still sees a normal completion.
type ACPMeta struct {
	Agent          string `json:"agent,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	StopReason     string `json:"stop_reason,omitempty"`
	Steps          []Step `json:"steps,omitempty"`
}

// Step is one activity the agent performed during the turn: reasoning, a tool
// call, or a plan. Steps are a summary, not a transcript.
type Step struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Title      string `json:"title,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
}

// Step types.
const (
	StepThought        = "thought"
	StepToolCall       = "tool_call"
	StepToolCallUpdate = "tool_call_update"
	StepPlan           = "plan"
)

/* ---- models ---- */

// Model is one entry of the model listing.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelList is the /v1/models response.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

/* ---- errors ---- */

// ErrorResponse is the OpenAI error envelope. Every failure is rendered this
// way, including ACP failures, so clients need only one error path.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes a failure.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

// Error types, matching the OpenAI vocabulary.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeServer         = "server_error"
	ErrTypeAuth           = "authentication_error"
)

// NewID returns a random identifier with the given prefix, in the shape the
// OpenAI API uses (e.g. "chatcmpl-1a2b3c…").
func NewID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf[:])
}
