package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ObjectTextCompletion is the legacy completions object type.
const ObjectTextCompletion = "text_completion"

// CompletionRequest is the legacy completions request.
//
// It is the pre-chat surface: a bare prompt with no message roles. The gateway
// runs one turn per prompt, which is where the array form and `best_of` become
// meaningful.
type CompletionRequest struct {
	Model  string          `json:"model"`
	Prompt json.RawMessage `json:"prompt"`
	Stream bool            `json:"stream,omitempty"`

	MaxTokens        *int            `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	N                *int            `json:"n,omitempty"`
	BestOf           *int            `json:"best_of,omitempty"`
	Echo             *bool           `json:"echo,omitempty"`
	Suffix           *string         `json:"suffix,omitempty"`
	Logprobs         *int            `json:"logprobs,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	Seed             *int            `json:"seed,omitempty"`
	User             string          `json:"user,omitempty"`

	// ConversationID and Workspace are this gateway's extensions.
	ConversationID string `json:"conversation_id,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
}

// CompletionResponse is the legacy completions reply.
type CompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   *Usage             `json:"usage,omitempty"`
	ACP     *ACPMeta           `json:"acp,omitempty"`
}

// CompletionChoice is one completion.
type CompletionChoice struct {
	Text         string `json:"text"`
	Index        int    `json:"index"`
	Logprobs     *int   `json:"logprobs"`
	FinishReason string `json:"finish_reason"`
}

// CompletionChunk is one streaming completions event.
type CompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []CompletionDelta `json:"choices"`
}

// CompletionDelta is one streaming completion delta.
type CompletionDelta struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	Logprobs     *int    `json:"logprobs"`
	FinishReason *string `json:"finish_reason"`
}

// CompletionPrompts decodes the prompt field into one or more prompts.
func CompletionPrompts(prompt json.RawMessage) ([]string, error) {
	trimmed := strings.TrimSpace(string(prompt))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("prompt is required")
	}

	var single string
	if err := json.Unmarshal(prompt, &single); err == nil {
		return []string{single}, nil
	}

	var many []string
	if err := json.Unmarshal(prompt, &many); err != nil {
		return nil, fmt.Errorf("prompt must be a string or an array of strings: %w", err)
	}
	if len(many) == 0 {
		return nil, fmt.Errorf("prompt array is empty")
	}
	return many, nil
}
