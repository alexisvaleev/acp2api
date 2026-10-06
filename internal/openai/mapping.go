package openai

import (
	"errors"
	"strings"

	"github.com/quonaro/acp2api/internal/acp"
)

// MaxSteps bounds the activity summary attached to a response, so a long turn
// cannot produce an unbounded payload.
const MaxSteps = 64

// BuildTurn renders the prompt for one turn, including the caller-tools
// preamble when tools are in play.
func BuildTurn(messages []Message, persistent bool, tools []Tool, choice ToolChoice) (string, error) {
	body, err := turnBody(messages, persistent)
	if err != nil {
		return "", err
	}
	preamble := ToolPreamble(tools, choice)
	if preamble == "" {
		return body, nil
	}
	return preamble + "\n\n" + body, nil
}

// turnBody selects the text to send for this turn. Tool results take precedence
// over the last user turn: they are the newest input to the session, and the
// agent is waiting on them rather than on the user.
func turnBody(messages []Message, persistent bool) (string, error) {
	if results := RenderToolResults(trailingToolMessages(messages)); results != "" {
		return results, nil
	}
	return BuildPrompt(messages, persistent)
}

// BuildPrompt renders the request's messages into the text handed to the agent.
//
// The two modes exist because an ACP session is stateful:
//   - persistent: the agent already holds the conversation, so only the newest
//     user turn is sent. Replaying the transcript would duplicate history the
//     agent can already see.
//   - ephemeral: the agent starts from nothing, so the whole transcript is
//     flattened with role headers.
func BuildPrompt(messages []Message, persistent bool) (string, error) {
	if len(messages) == 0 {
		return "", errors.New("no messages in request")
	}
	if persistent {
		text := lastUserText(messages)
		if text == "" {
			return "", errors.New("no user message in request")
		}
		return text, nil
	}
	if text := flatten(messages); text != "" {
		return text, nil
	}
	return "", errors.New("no text content in request messages")
}

// lastUserText returns the newest user turn.
func lastUserText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		if text := strings.TrimSpace(messages[i].Text()); text != "" {
			return text
		}
	}
	return ""
}

// flatten renders the whole transcript with role headers. A lone user message
// is passed through unchanged, since headers would only add noise.
func flatten(messages []Message) string {
	nonEmpty := 0
	for _, m := range messages {
		if strings.TrimSpace(m.Text()) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 1 {
		return lastUserText(messages)
	}

	var b strings.Builder
	for _, m := range messages {
		text := strings.TrimSpace(m.Text())
		if text == "" {
			continue
		}
		b.WriteString("## ")
		b.WriteString(roleLabel(m.Role))
		b.WriteString("\n")
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// roleLabel names a message role in the flattened transcript.
func roleLabel(role string) string {
	switch role {
	case "system", "developer":
		return "system"
	case "assistant":
		return "assistant"
	case "tool":
		return "tool result"
	case "user":
		return "user"
	case "":
		return "user"
	default:
		return role
	}
}

// FromUpdate maps one ACP session update onto the OpenAI-facing view: the
// incremental assistant text, and an optional activity step.
//
// Thoughts are not part of the assistant reply — they are surfaced as steps so
// the reasoning is visible without polluting the content.
func FromUpdate(u acp.SessionUpdate) (text string, step *Step) {
	switch u.SessionUpdate {
	case acp.UpdateAgentMessageChunk:
		if u.Content != nil {
			text = u.Content.Text
		}
	case acp.UpdateAgentThoughtChunk:
		if u.Content != nil && u.Content.Text != "" {
			step = &Step{Type: StepThought, Text: u.Content.Text}
		}
	case acp.UpdateToolCall:
		step = &Step{
			Type:       StepToolCall,
			ToolCallID: u.ToolCallID,
			Title:      u.Title,
			Kind:       u.Kind,
			Status:     u.Status,
		}
	case acp.UpdateToolCallUpdate:
		step = &Step{
			Type:       StepToolCallUpdate,
			ToolCallID: u.ToolCallID,
			Title:      u.Title,
			Status:     u.Status,
		}
	case acp.UpdatePlan:
		step = &Step{Type: StepPlan, Text: planSummary(u.Entries)}
	}
	return text, step
}

// planSummary renders a plan as a single line.
func planSummary(entries []acp.PlanEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Content) != "" {
			parts = append(parts, strings.TrimSpace(e.Content))
		}
	}
	return strings.Join(parts, "; ")
}

// FinishToolCalls is the finish_reason for a turn that asked the caller to run
// something. It is not derived from the ACP stop reason — the agent finished
// normally; it is the OpenAI contract that changes shape.
const FinishToolCalls = "tool_calls"

// FinishReason maps an ACP stop reason onto the OpenAI finish_reason
// vocabulary. The original value is preserved in the acp extension, so a
// caller that needs it can still tell "end_turn" from "cancelled".
func FinishReason(stopReason string) string {
	switch stopReason {
	case acp.StopMaxTokens:
		return "length"
	case acp.StopRefusal:
		return "content_filter"
	default:
		return "stop"
	}
}

// EstimateTokens approximates a token count from a text length. Agents do not
// report usage, so this stands in at roughly four bytes per token.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// StepLog collects activity steps, bounded by MaxSteps.
type StepLog struct {
	steps []Step
}

// Add records a step, ignoring nil steps and anything past the cap.
func (l *StepLog) Add(s *Step) {
	if s == nil || len(l.steps) >= MaxSteps {
		return
	}
	l.steps = append(l.steps, *s)
}

// Steps returns the collected steps, or nil when there were none.
func (l *StepLog) Steps() []Step {
	if len(l.steps) == 0 {
		return nil
	}
	return l.steps
}
