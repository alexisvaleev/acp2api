package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// maxBodyBytes caps a request body. Conversations are text, so a megabyte is
// already generous.
const maxBodyBytes = 1 << 20

// handleChatCompletions serves POST /v1/chat/completions, streaming or not.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req openai.ChatCompletionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_json",
			"request body is not valid JSON: "+err.Error(), "")
		return
	}

	// The parameter policy runs first: a request we cannot honour must fail
	// before any agent process is spawned.
	ignored, paramErr := openai.ValidateRequest(&req)
	if paramErr != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
			openai.CodeUnsupportedParameter, paramErr.Error(), paramErr.Param)
		return
	}
	if len(ignored) > 0 {
		w.Header().Set("X-Acp2api-Ignored-Params", strings.Join(ignored, ","))
	}

	available := strings.Join(agentIDs(s.manager), ", ")
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "missing_model",
			"model is required; available: "+available, "model")
		return
	}
	if _, _, err := s.manager.Resolve(req.Model); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "unknown_model",
			fmt.Sprintf("%s; available: %s", err.Error(), available), "model")
		return
	}

	// A conversation id makes the session persistent, so the agent keeps the
	// history and only the newest turn is sent. Without one the agent starts
	// from nothing, so the whole transcript must be flattened into the prompt.
	conversationID := req.ConversationID
	if conversationID == "" {
		conversationID = req.User
	}

	choice, err := openai.ParseToolChoice(req.ToolChoice)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_tool_choice",
			err.Error(), "tool_choice")
		return
	}

	// Validate the output controls before any agent work starts.
	if _, err := openai.NewTextLimit(req.Stop, req.EffectiveMaxTokens()); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_stop", err.Error(), "stop")
		return
	}

	format, err := openai.ParseResponseFormat(req.ResponseFormat)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_response_format",
			err.Error(), "response_format")
		return
	}

	prompt, err := openai.BuildTurn(req.Messages, conversationID != "", req.Tools, choice)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_messages", err.Error(), "messages")
		return
	}

	images, err := openai.ImageParts(req.Messages)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
			openai.CodeUnsupportedParameter, err.Error(), openai.ParamImageURL)
		return
	}

	// The output format is prompt engineering, like the tool contract: ACP has
	// no schema negotiation, so the shape is requested and then verified.
	if instruction := format.Instruction(); instruction != "" {
		prompt = instruction + "\n" + prompt
	}

	plan := turnPlan{
		req:     req,
		prompt:  prompt,
		ignored: ignored,
		format:  format,
		// Caller tools are in play only when some were declared and the choice
		// does not forbid them. Only then can an agent message be an envelope.
		tools: len(req.Tools) > 0 && choice.UsesCallerTools(),
		turn: session.Request{
			Model:          req.Model,
			ConversationID: conversationID,
			Workspace:      req.Workspace,
			Prompt:         prompt,
			Parts:          contentParts(prompt, images),
		},
	}

	if req.Stream {
		s.streamTurn(w, r, plan)
		return
	}
	s.blockingTurn(w, r, plan)
}

// turnPlan is everything both renderers need for one turn.
type turnPlan struct {
	req     openai.ChatCompletionRequest
	turn    session.Request
	prompt  string
	ignored []string
	format  openai.ResponseFormat
	// tools reports whether the caller declared tools, so an agent message may
	// be a tool-call envelope rather than prose.
	tools bool
}

// turnOutcome is everything one turn produced.
type turnOutcome struct {
	text   string
	calls  []openai.ToolCall
	result session.Result
	steps  openai.StepLog
	capped bool
}

// runTurn executes one turn and applies the output controls.
//
// keepParts is false for a retry: the correction is text, and the agent already
// holds the image in the session.
func (s *Server) runTurn(r *http.Request, plan turnPlan, prompt string, keepParts bool) (turnOutcome, error) {
	limit, err := openai.NewTextLimit(plan.req.Stop, plan.req.EffectiveMaxTokens())
	if err != nil {
		return turnOutcome{}, err
	}

	turn := plan.turn
	turn.Prompt = prompt
	if !keepParts {
		turn.Parts = nil
	}

	var text strings.Builder
	var steps openai.StepLog

	result, err := s.manager.Prompt(r.Context(), turn, func(u acp.SessionUpdate) error {
		piece, step := openai.FromUpdate(u)
		steps.Add(step)
		if piece == "" {
			return nil
		}
		emit, _ := limit.Push(piece)
		text.WriteString(emit)
		return nil
	})
	if err != nil {
		return turnOutcome{}, err
	}
	text.WriteString(limit.Finish())

	out := turnOutcome{text: text.String(), result: result, steps: steps, capped: limit.Capped()}
	if plan.tools {
		// A tool call is the whole message: the envelope is consumed, leaving
		// no prose behind.
		if parsed := openai.ParseToolCalls(out.text); len(parsed) > 0 {
			out.calls = openai.LimitCalls(parsed, plan.req.ParallelToolCalls)
			out.text = ""
		}
	}
	return out, nil
}

// enforceFormat validates a reply against response_format, retrying once with a
// correction before giving up.
func (s *Server) enforceFormat(r *http.Request, plan turnPlan, out turnOutcome) (turnOutcome, error) {
	canonical, err := plan.format.Validate(out.text)
	if err == nil {
		out.text = string(canonical)
		return out, nil
	}

	retry, retryErr := s.runTurn(r, plan, plan.format.Correction(err.Error()), false)
	if retryErr != nil {
		return out, retryErr
	}
	canonical, err = plan.format.Validate(retry.text)
	if err != nil {
		return out, fmt.Errorf("the agent did not produce valid JSON after one retry: %w", err)
	}
	retry.text = string(canonical)
	return retry, nil
}

// blockingTurn runs the turn and returns one complete completion.
func (s *Server) blockingTurn(w http.ResponseWriter, r *http.Request, plan turnPlan) {
	out, err := s.runTurn(r, plan, plan.turn.Prompt, true)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	if plan.format.Active() && len(out.calls) == 0 {
		out, err = s.enforceFormat(r, plan, out)
		if err != nil {
			writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
				"invalid_response_format", err.Error(), "response_format")
			return
		}
	}

	content := out.text
	calls := out.calls

	finish := openai.FinishReason(out.result.StopReason)
	if out.capped {
		finish = openai.FinishLength
	}
	if len(calls) > 0 {
		finish = openai.FinishToolCalls
	}

	writeJSON(w, http.StatusOK, openai.ChatCompletionResponse{
		ID:      openai.NewID("chatcmpl"),
		Object:  openai.ObjectChatCompletion,
		Created: time.Now().Unix(),
		Model:   plan.req.Model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      openai.NewResponseMessage(content, calls),
			FinishReason: finish,
		}},
		Usage: estimateUsage(plan.prompt, content),
		ACP:   acpMeta(out.result, out.steps, plan.ignored),
	})
}

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

	send(newChunk(id, created, plan.req.Model, openai.Delta{Role: "assistant"}, nil))

	var text strings.Builder
	var steps openai.StepLog
	hold := openai.NewToolStream(plan.tools)

	// A structured output has to be verified before it is delivered, so the
	// answer is buffered rather than streamed. Streaming it and then reporting
	// it as invalid would leave the caller with unusable text.
	bufferOnly := plan.format.Active()

	result, err := s.manager.Prompt(r.Context(), plan.turn, func(u acp.SessionUpdate) error {
		piece, step := openai.FromUpdate(u)
		steps.Add(step)
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
		writeSSEDone(w)
		flusher.Flush()
		return
	}

	// Settle the hold: what is left is either the answer or a tool call.
	rest, calls := hold.Finish()
	calls = openai.LimitCalls(calls, plan.req.ParallelToolCalls)
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

	capped := limit.Capped()

	// A structured answer is verified here, and retried once if it is wrong.
	if bufferOnly && len(calls) == 0 {
		out, formatErr := s.enforceFormat(r, plan, turnOutcome{
			text: text.String(), result: result, steps: steps, capped: capped,
		})
		if formatErr != nil {
			send(openai.ErrorResponse{Error: openai.ErrorBody{
				Message: formatErr.Error(),
				Type:    openai.ErrTypeInvalidRequest,
				Code:    "invalid_response_format",
				Param:   "response_format",
			}})
			writeSSEDone(w)
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

	writeSSEDone(w)
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
	s.log.Warn("handler: agent turn failed", "error", err)
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
