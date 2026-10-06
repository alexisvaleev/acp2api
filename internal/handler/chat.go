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

	prompt, err := openai.BuildPrompt(req.Messages, conversationID != "")
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_messages", err.Error(), "messages")
		return
	}

	turn := session.Request{
		Model:          req.Model,
		ConversationID: conversationID,
		Workspace:      req.Workspace,
		Prompt:         prompt,
	}

	if req.Stream {
		s.streamTurn(w, r, req, turn, prompt, ignored)
		return
	}
	s.blockingTurn(w, r, req, turn, prompt, ignored)
}

// blockingTurn runs the turn and returns one complete completion.
func (s *Server) blockingTurn(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest, turn session.Request, prompt string, ignored []string) {
	var text strings.Builder
	var steps openai.StepLog

	result, err := s.manager.Prompt(r.Context(), turn, func(u acp.SessionUpdate) error {
		piece, step := openai.FromUpdate(u)
		text.WriteString(piece)
		steps.Add(step)
		return nil
	})
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	content := text.String()
	writeJSON(w, http.StatusOK, openai.ChatCompletionResponse{
		ID:      openai.NewID("chatcmpl"),
		Object:  openai.ObjectChatCompletion,
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      openai.ResponseMessage{Role: "assistant", Content: content},
			FinishReason: openai.FinishReason(result.StopReason),
		}},
		Usage: estimateUsage(prompt, content),
		ACP:   acpMeta(result, steps, ignored),
	})
}

// streamTurn runs the turn and emits the assistant text as server-sent events.
func (s *Server) streamTurn(w http.ResponseWriter, r *http.Request, req openai.ChatCompletionRequest, turn session.Request, prompt string, ignored []string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, openai.ErrTypeServer, "stream_unsupported",
			"this server cannot stream responses", "")
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

	send(newChunk(id, created, req.Model, openai.Delta{Role: "assistant"}, nil))

	var text strings.Builder
	var steps openai.StepLog

	result, err := s.manager.Prompt(r.Context(), turn, func(u acp.SessionUpdate) error {
		piece, step := openai.FromUpdate(u)
		steps.Add(step)
		if piece == "" {
			return nil
		}
		text.WriteString(piece)
		send(newChunk(id, created, req.Model, openai.Delta{Content: piece}, nil))
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

	content := text.String()
	finish := openai.FinishReason(result.StopReason)
	final := newChunk(id, created, req.Model, openai.Delta{}, &finish)
	final.ACP = acpMeta(result, steps, ignored)
	send(final)

	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		send(openai.ChatCompletionChunk{
			ID:      id,
			Object:  openai.ObjectChatCompletionChunk,
			Created: created,
			Model:   req.Model,
			Usage:   estimateUsage(prompt, content),
		})
	}

	writeSSEDone(w)
	flusher.Flush()
}

// writeAgentError maps a session failure onto an HTTP status. Model and
// configuration problems are rejected before a turn starts, so anything
// arriving here is the agent's fault.
func (s *Server) writeAgentError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, openai.ErrTypeServer, "timeout", err.Error(), "")
		return
	}
	s.log.Warn("handler: agent turn failed", "error", err)
	writeError(w, http.StatusBadGateway, openai.ErrTypeServer, "agent_error", err.Error(), "")
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
