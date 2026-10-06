package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolChoice decodes the OpenAI tool_choice value: a mode string, or a
// named function.
func ParseToolChoice(raw json.RawMessage) (ToolChoice, error) {
	if len(raw) == 0 {
		return ToolChoice{Mode: ToolChoiceAuto}, nil
	}

	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		switch mode {
		case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
			return ToolChoice{Mode: mode}, nil
		default:
			return ToolChoice{}, fmt.Errorf("unknown tool_choice %q", mode)
		}
	}

	var named struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return ToolChoice{}, fmt.Errorf("invalid tool_choice: %w", err)
	}
	if strings.TrimSpace(named.Function.Name) == "" {
		return ToolChoice{}, fmt.Errorf("tool_choice names no function")
	}
	return ToolChoice{Mode: ToolChoiceFunction, Function: named.Function.Name}, nil
}

// ToolPreamble returns the instruction prepended to a turn when the caller
// supplied tools, or an empty string when no caller tool may be used.
//
// This is prompt engineering, not a protocol feature: ACP has no notion of a
// caller-defined function, so there is nothing to negotiate. The contract is
// therefore best-effort, and the gateway fails open — an agent that ignores it
// has its text returned as the answer, with no tool call.
func ToolPreamble(tools []Tool, choice ToolChoice) string {
	if len(tools) == 0 || !choice.UsesCallerTools() {
		return ""
	}

	var b strings.Builder
	b.WriteString("[system — caller tools]\n")
	b.WriteString("You are answering through a host application that executes tools for the user.\n")
	b.WriteString("The host has registered the functions below. You cannot run them yourself; ask the host by calling one.\n\n")
	b.WriteString("Callable functions:\n")
	for _, tool := range tools {
		if strings.TrimSpace(tool.Function.Name) == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(tool.Function.Name)
		if desc := strings.TrimSpace(tool.Function.Description); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		if len(tool.Function.Parameters) > 0 {
			b.WriteString("\n  parameters: ")
			b.Write(tool.Function.Parameters)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nTo call one, reply with ONLY this JSON object and no other text:\n")
	b.WriteString(`{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"<name>","arguments":"<json object>"}}]}`)
	b.WriteString("\n\nRules:\n")
	b.WriteString("1. Call only the functions listed above. Use your own tools for everything else.\n")
	b.WriteString("2. The arguments value is a JSON object encoded as a string.\n")
	b.WriteString("3. When a tool result appears below, use it to answer in plain language.\n")
	switch choice.Mode {
	case ToolChoiceRequired:
		b.WriteString("4. You must call one of the functions now.\n")
	case ToolChoiceFunction:
		fmt.Fprintf(&b, "4. You must call the function %s now.\n", choice.Function)
	}
	return strings.TrimSpace(b.String())
}

// RenderToolResults renders the trailing role:"tool" messages as the tool
// results the agent asked for.
func RenderToolResults(messages []Message) string {
	var b strings.Builder
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		b.WriteString("[tool result")
		if message.ToolCallID != "" {
			b.WriteString(" ")
			b.WriteString(message.ToolCallID)
		}
		b.WriteString("]\n")
		b.WriteString(strings.TrimSpace(message.Text()))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// trailingToolMessages returns the run of role:"tool" messages at the end of
// the transcript. They are the newest input to the session, so they take
// precedence over the last user turn.
func trailingToolMessages(messages []Message) []Message {
	end := len(messages)
	for end > 0 && messages[end-1].Role == "tool" {
		end--
	}
	return messages[end:]
}

// HasToolResults reports whether the transcript ends with tool results, which
// means the session is mid tool loop rather than waiting on a user turn.
func HasToolResults(messages []Message) bool {
	return len(trailingToolMessages(messages)) > 0
}
