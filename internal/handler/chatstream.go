package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// streamTurn runs the turn and emits the assistant text as server-sent events.
//
// With caller tools in play the text is held back while it could still be a
// tool-call envelope, so the envelope never reaches the client as prose. The
// hold fails open: anything that turns out not to be an envelope is released.
func (s *Server) streamTurn(w http.ResponseWriter, r *http.Request, plan turnPlan) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, openai.ErrTypeServer, "stream_unsupported",
			"this server cannot stream responses", "")
		return
	}

	// Build the output limiter before the status line goes out, so a bad stop
	// value is still a clean error response.
	limit, err := openai.NewTextLimit(plan.req.Stop, plan.req.EffectiveMaxTokens())
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_stop", err.Error(), "stop")
		return
	}

	id := openai.NewID("chatcmpl")
	created := time.Now().Unix()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(v any) {
		if err := writeSSE(w, v); err == nil {
			flusher.Flush()
		}
	}
	sendText := func(text string) {
		if text != "" {
			send(newChunk(id, created, plan.req.Model, openai.Delta{Content: text}, nil))
		}
	}
	// Reasoning streams ahead of the answer and outside both the tool hold and
	// the output limit: a thought is not the reply, so it is neither held back
	// as a possible envelope nor counted against max_tokens.
	sendReasoning := func(text string) {
		if text != "" {
			send(newChunk(id, created, plan.req.Model, openai.Delta{ReasoningContent: text}, nil))
		}
	}

	send(newChunk(id, created, plan.req.Model, openai.Delta{Role: "assistant"}, nil))

	var text strings.Builder
	var reasoning strings.Builder
	var steps openai.StepLog
	hold := openai.NewToolStream(plan.tools)
	tools := newTurnLog(s.log, plan.turn.ConversationID)
	plan.turn.OnSession = tools.bind

	// A structured output has to be verified before it is delivered, so the
	// answer is buffered rather than streamed. Streaming it and then reporting
	// it as invalid would leave the caller with unusable text.
	bufferOnly := plan.format.Active()

	result, err := s.manager.Prompt(r.Context(), plan.turn, func(u acp.SessionUpdate) error {
		piece, thought, step := tools.update(u)
		steps.Add(step)
		if thought != "" {
			reasoning.WriteString(thought)
			sendReasoning(thought)
		}
		if piece == "" {
			return nil
		}
		emit := hold.Push(piece)
		if emit == "" {
			return nil
		}
		// The tool hold runs first: stop sequences apply to the answer, not to
		// an envelope that is about to be consumed.
		allowed, _ := limit.Push(emit)
		if allowed != "" {
			text.WriteString(allowed)
			if !bufferOnly {
				sendText(allowed)
			}
		}
		return nil
	})

	if err != nil {
		// The status line is already on the wire, so the failure travels in band
		// as an error object followed by the terminator.
		send(openai.ErrorResponse{Error: openai.ErrorBody{
			Message: err.Error(),
			Type:    openai.ErrTypeServer,
			Code:    "agent_error",
		}})
		_ = writeSSEDone(w)
		flusher.Flush()
		return
	}

	// Settle the hold: what is left is either the answer or a tool call.
	rest, calls := hold.Finish()
	calls = openai.LimitCalls(calls, plan.req.ParallelToolCalls)
	tools.callerCalls(calls)
	if rest != "" {
		if allowed, _ := limit.Push(rest); allowed != "" {
			text.WriteString(allowed)
			if !bufferOnly {
				sendText(allowed)
			}
		}
	}
	if tail := limit.Finish(); tail != "" {
		text.WriteString(tail)
		if !bufferOnly {
			sendText(tail)
		}
	}

	tools.answered(text.String(), result.StopReason)
	capped := limit.Capped()

	// A structured answer is verified here, and retried once if it is wrong.
	if bufferOnly && len(calls) == 0 {
		out, formatErr := s.enforceFormat(r, plan, turnOutcome{
			text: text.String(), reasoning: reasoning.String(), result: result, steps: steps, capped: capped,
		})
		if formatErr != nil {
			send(openai.ErrorResponse{Error: openai.ErrorBody{
				Message: formatErr.Error(),
				Type:    openai.ErrTypeInvalidRequest,
				Code:    "invalid_response_format",
				Param:   "response_format",
			}})
			_ = writeSSEDone(w)
			flusher.Flush()
			return
		}
		text.Reset()
		text.WriteString(out.text)
		result = out.result
		steps = out.steps
		capped = out.capped
		sendText(out.text)
	}

	finish := openai.FinishReason(result.StopReason)
	if capped {
		finish = openai.FinishLength
	}
	if len(calls) > 0 {
		finish = openai.FinishToolCalls
		send(newChunk(id, created, plan.req.Model, openai.Delta{ToolCalls: openai.ToolCallDeltas(calls)}, nil))
	}

	final := newChunk(id, created, plan.req.Model, openai.Delta{}, &finish)
	final.ACP = acpMeta(result, steps, plan.ignored)
	send(final)

	if plan.req.StreamOptions != nil && plan.req.StreamOptions.IncludeUsage {
		send(openai.ChatCompletionChunk{
			ID:      id,
			Object:  openai.ObjectChatCompletionChunk,
			Created: created,
			Model:   plan.req.Model,
			Usage:   estimateUsage(plan.prompt, text.String()),
		})
	}

	_ = writeSSEDone(w)
	flusher.Flush()
}

// writeAgentError maps a session failure onto an HTTP status. Model and
// configuration problems are rejected before a turn starts, so anything
// arriving here is the agent's fault — except an image the agent cannot read,
// which is the caller's request and must be a clear refusal rather than a
// silently blind answer.
func (s *Server) writeAgentError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrImagesUnsupported) {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, openai.CodeUnsupportedParameter,
			"the selected agent did not advertise image prompt support, so the image would be ignored; "+
				"use an agent that accepts images, or remove the image", openai.ParamImageURL)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, openai.ErrTypeServer, "timeout", err.Error(), "")
		return
	}
	s.log.With("module", "handler").Warn("agent turn failed", "error", err)
	writeError(w, http.StatusBadGateway, openai.ErrTypeServer, "agent_error", err.Error(), "")
}

// contentParts builds the ACP content blocks for a turn: the prompt text,
// followed by any images the caller supplied.
func contentParts(prompt string, images []openai.ImagePart) []acp.ContentBlock {
	if len(images) == 0 {
		return nil
	}
	parts := make([]acp.ContentBlock, 0, len(images)+1)
	parts = append(parts, acp.TextBlock(prompt))
	for _, image := range images {
		parts = append(parts, acp.ContentBlock{
			Type:     "image",
			Data:     image.Data,
			MimeType: image.MimeType,
		})
	}
	return parts
}

// newChunk builds one streaming chunk with a single choice.
func newChunk(id string, created int64, model string, delta openai.Delta, finish *string) openai.ChatCompletionChunk {
	return openai.ChatCompletionChunk{
		ID:      id,
		Object:  openai.ObjectChatCompletionChunk,
		Created: created,
		Model:   model,
		Choices: []openai.ChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
}

// acpMeta assembles the ACP extension attached to a response.
func acpMeta(result session.Result, steps openai.StepLog, ignored []string) *openai.ACPMeta {
	return &openai.ACPMeta{
		Agent:          result.Agent,
		SessionID:      result.SessionID,
		ConversationID: result.ConversationID,
		StopReason:     result.StopReason,
		Steps:          steps.Steps(),
		IgnoredParams:  ignored,
	}
}

// estimateUsage builds a usage block from text lengths.
func estimateUsage(prompt, completion string) *openai.Usage {
	in := openai.EstimateTokens(prompt)
	out := openai.EstimateTokens(completion)
	return &openai.Usage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}
}

// writeSSE writes one server-sent event.
func writeSSE(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// writeSSEDone writes the stream terminator.
func writeSSEDone(w io.Writer) error {
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}
