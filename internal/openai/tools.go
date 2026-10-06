package openai

import "encoding/json"

// Tool is one caller-declared function the model may invoke.
//
// The gateway does not execute tools: it relays a call to the caller, who runs
// it and returns the result. ACP has no equivalent concept, so the contract is
// carried in the prompt — see ToolPreamble.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a callable function.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolCall is one function invocation the model asked for.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is the name and serialised arguments of a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallDelta is one tool call inside a streaming chunk. Unlike the
// non-streaming form it carries an index, which is how clients assemble the
// list.
type ToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// ToolCallDeltas converts a call list into streaming deltas.
func ToolCallDeltas(calls []ToolCall) []ToolCallDelta {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCallDelta, 0, len(calls))
	for i, call := range calls {
		out = append(out, ToolCallDelta{
			Index:    i,
			ID:       call.ID,
			Type:     call.Type,
			Function: call.Function,
		})
	}
	return out
}

// ToolChoice is the OpenAI tool_choice value: a mode, or a named function.
type ToolChoice struct {
	Mode     string
	Function string
}

// Tool choice modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceFunction = "function"
)

// UsesCallerTools reports whether the choice permits a caller tool to be called.
func (c ToolChoice) UsesCallerTools() bool {
	return c.Mode != ToolChoiceNone
}
