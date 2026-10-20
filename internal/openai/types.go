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

	// The fields below are modelled explicitly so the parameter policy can tell
	// "absent" from "zero". A plain float64 cannot distinguish a temperature of
	// 0 from an omitted temperature, and the policy has to report exactly what
	// the caller sent.

	// Tools and ToolChoice are the modern function-calling surface.
	Tools      []Tool          `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`

	// Functions and FunctionCall are the deprecated predecessors of the above.
	Functions    []json.RawMessage `json:"functions,omitempty"`
	FunctionCall json.RawMessage   `json:"function_call,omitempty"`

	// ResponseFormat requests JSON or schema-constrained output.
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	// Stop is a string or an array of strings.
	Stop json.RawMessage `json:"stop,omitempty"`

	// MaxTokens and MaxCompletionTokens cap the output length.
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	// N requests more than one choice.
	N *int `json:"n,omitempty"`

	// Logprobs and TopLogprobs request token probabilities.
	Logprobs    *bool `json:"logprobs,omitempty"`
	TopLogprobs *int  `json:"top_logprobs,omitempty"`

	// Sampling parameters. The agent owns its own sampling, so these are
	// accepted and reported rather than honoured.
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	Seed             *int           `json:"seed,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int `json:"logit_bias,omitempty"`

	// ParallelToolCalls is honoured: false truncates a multi-call envelope to
	// its first call, because a caller that forbade parallel calls cannot be
	// expected to handle two.
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`

	// Parameters that steer the agent but cannot change the response shape.
	// They are accepted and reported.
	Verbosity   *string         `json:"verbosity,omitempty"`
	ServiceTier *string         `json:"service_tier,omitempty"`
	Prediction  json.RawMessage `json:"prediction,omitempty"`
	Store       *bool           `json:"store,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`

	// ReasoningEffort is the flat effort selector, honoured because it picks
	// the agent's model variant. It is a pointer so an explicit empty string
	// counts as absent rather than as a level.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// Reasoning is the object form, which clients that speak the Responses
	// shape send here too. It is kept raw so checkReasoning sees every key.
	Reasoning json.RawMessage `json:"reasoning,omitempty"`

	// Modalities must be text-only: this gateway cannot return audio.
	Modalities []string `json:"modalities,omitempty"`
	// Audio requests spoken output, which an ACP agent cannot produce.
	Audio json.RawMessage `json:"audio,omitempty"`
	// WebSearchOptions requests a built-in tool with no ACP equivalent.
	WebSearchOptions json.RawMessage `json:"web_search_options,omitempty"`
}

// EffectiveMaxTokens returns whichever output cap the caller set. The OpenAI
// API renamed max_tokens to max_completion_tokens; either may appear.
func (r ChatCompletionRequest) EffectiveMaxTokens() *int {
	if r.MaxCompletionTokens != nil {
		return r.MaxCompletionTokens
	}
	return r.MaxTokens
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
			case p.Type == "image_url" || p.Type == "input_image":
				// The image itself travels as its own content block; the text
				// transcript only needs a placeholder.
				out += "[image]"
			case p.Type != "":
				out += "[" + p.Type + "]"
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

// ResponseMessage is the assistant's reply. Content is a pointer so a tool-call
// turn can send `"content": null`, which is what the OpenAI API does: a turn
// either answers or asks the caller to run something, never both.
//
// ReasoningContent carries the agent's thoughts, in the `reasoning_content`
// field DeepSeek popularised and clients such as Open WebUI render. It is not
// standard OpenAI, but it is the de facto convention, and the alternative —
// burying reasoning in the `acp` extension — means no client shows it.
type ResponseMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ContentString returns the message content, or an empty string when the turn
// carried no prose.
func (m ResponseMessage) ContentString() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// NewResponseMessage builds an assistant message from optional prose, optional
// reasoning, and calls. At most one of prose and calls is expected to be
// non-empty; reasoning may accompany either.
func NewResponseMessage(content, reasoning string, calls []ToolCall) ResponseMessage {
	message := ResponseMessage{Role: "assistant", ReasoningContent: reasoning, ToolCalls: calls}
	if len(calls) == 0 {
		message.Content = &content
	}
	return message
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

// Delta is an incremental assistant message. ReasoningContent streams the
// agent's thoughts ahead of the answer, mirroring ResponseMessage.
type Delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
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
	// IgnoredParams names the request parameters the gateway accepted but did
	// not honour. It is also echoed in the X-Acp2api-Ignored-Params header, so
	// a caller can always tell what was dropped.
	IgnoredParams []string `json:"ignored_params,omitempty"`
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
