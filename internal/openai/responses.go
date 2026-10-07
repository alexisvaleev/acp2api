package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Response object types and statuses.
const (
	ObjectResponse = "response"
	// StatusCompleted means the turn finished normally.
	StatusCompleted = "completed"
	// StatusIncomplete means the agent stopped early, for example on a length cap.
	StatusIncomplete = "incomplete"
	// StatusFailed means the turn errored.
	StatusFailed = "failed"

	// Output item types.
	ItemMessage      = "message"
	ItemFunctionCall = "function_call"
	// Output content part type.
	PartOutputText = "output_text"
)

// ResponsesRequest is the subset of the Responses API this gateway understands.
type ResponsesRequest struct {
	Model string `json:"model"`
	// Input is either a string or an array of input items.
	Input json.RawMessage `json:"input"`
	// Instructions are prepended to every turn. ACP sessions have no system
	// prompt of their own, so this is the only place they can live.
	Instructions string `json:"instructions,omitempty"`
	// PreviousResponseID resumes the session that produced that response.
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	// Store keeps the response so it can be retrieved and resumed. Defaults to
	// true, as the OpenAI API does.
	Store *bool `json:"store,omitempty"`
	// Stream switches to the named-event protocol.
	Stream bool `json:"stream,omitempty"`

	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`

	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	User            string          `json:"user,omitempty"`

	// Reasoning carries the Responses effort selector; its effort level maps
	// onto the agent's model-variant catalog the way chat's reasoning_effort
	// does.
	Reasoning *struct {
		Effort string `json:"effort,omitempty"`
	} `json:"reasoning,omitempty"`

	// ConversationID and Workspace are this gateway's extensions, mirroring the
	// chat surface.
	ConversationID string `json:"conversation_id,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
}

// InputItem is one entry of an array-form `input`.
type InputItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	// function_call fields.
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output field.
	Output json.RawMessage `json:"output,omitempty"`
}

// Response is the Responses API reply.
type Response struct {
	ID                 string          `json:"id"`
	Object             string          `json:"object"`
	CreatedAt          int64           `json:"created_at"`
	Status             string          `json:"status"`
	Model              string          `json:"model"`
	Output             []OutputItem    `json:"output"`
	OutputText         string          `json:"output_text"`
	Usage              *ResponseUsage  `json:"usage,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Instructions       string          `json:"instructions,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	Error              *ErrorBody      `json:"error,omitempty"`
	ACP                *ACPMeta        `json:"acp,omitempty"`
}

// OutputItem is one entry of `output`: a message or a function call.
type OutputItem struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Status  string          `json:"status,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content []OutputContent `json:"content,omitempty"`
	// function_call fields.
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// OutputContent is one part of a message item.
type OutputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations,omitempty"`
}

// ResponseUsage is the Responses API token report.
type ResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// InputMessages converts a Responses `input` value into the chat message shape
// the rest of the gateway already speaks, so the tool translation, the turn
// builder, and the envelope parser are all reused unchanged.
func InputMessages(input json.RawMessage) ([]Message, error) {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("input is required")
	}

	// The string form is one user turn.
	var text string
	if err := json.Unmarshal(input, &text); err == nil {
		return []Message{{Role: "user", Content: json.RawMessage(mustJSON(text))}}, nil
	}

	var items []InputItem
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of items: %w", err)
	}

	messages := make([]Message, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case ItemFunctionCall:
			// The assistant's own call. It is not input to the agent, but it
			// keeps the transcript well-formed for the tool-result path.
			messages = append(messages, Message{Role: "assistant"})
		case "function_call_output":
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: item.CallID,
				Content:    json.RawMessage(mustJSON(outputText(item.Output))),
			})
		default:
			role := item.Role
			if role == "" {
				role = "user"
			}
			content := item.Content
			if len(content) == 0 {
				content = json.RawMessage(`""`)
			}
			messages = append(messages, Message{Role: role, Content: content})
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("input contained no usable items")
	}
	return messages, nil
}

// outputText renders a function_call_output payload as text.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// mustJSON renders a Go value as JSON, falling back to a quoted empty string.
func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// NewMessageItem builds a completed assistant message item.
func NewMessageItem(text string) OutputItem {
	return OutputItem{
		Type:   ItemMessage,
		ID:     NewID("msg"),
		Status: StatusCompleted,
		Role:   "assistant",
		Content: []OutputContent{{
			Type: PartOutputText,
			Text: text,
		}},
	}
}

// NewFunctionCallItem builds a function call item from a tool call.
func NewFunctionCallItem(call ToolCall) OutputItem {
	return OutputItem{
		Type:      ItemFunctionCall,
		ID:        NewID("fc"),
		Status:    StatusCompleted,
		CallID:    call.ID,
		Name:      call.Function.Name,
		Arguments: call.Function.Arguments,
	}
}

// OutputTextOf concatenates the text of a response's message items.
func OutputTextOf(items []OutputItem) string {
	var b strings.Builder
	for _, item := range items {
		if item.Type != ItemMessage {
			continue
		}
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
