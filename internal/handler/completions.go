package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// handleCompletions serves POST /v1/completions, the legacy surface.
//
// A prompt array runs one turn per entry and returns one choice per entry,
// which is what the legacy API means by it.
func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	var req openai.CompletionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_json",
			"request body is not valid JSON: "+err.Error(), "")
		return
	}

	ignored, paramErr := openai.ValidateParams(&req)
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

	prompts, err := openai.CompletionPrompts(req.Prompt)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_prompt", err.Error(), "prompt")
		return
	}
	if _, err := openai.NewTextLimit(req.Stop, req.MaxTokens); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_stop", err.Error(), "stop")
		return
	}

	if req.Stream && len(prompts) > 1 {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "unsupported_stream_prompt_array",
			"streaming is not supported together with an array prompt; send one prompt or disable stream", "stream")
		return
	}
	if req.Stream && req.N != nil && *req.N > 1 {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "unsupported_stream_choices",
			"streaming is not supported together with n > 1; send one choice or disable stream", "stream")
		return
	}

	if req.Stream {
		s.streamCompletion(w, r, req, prompts[0])
		return
	}
	s.blockingCompletion(w, r, req, prompts, ignored)
}

// completionRun is the outcome of one prompt.
type completionRun struct {
	text   string
	stop   string
	capped bool
	acp    *openai.ACPMeta
	err    error
}

// runPrompt executes one prompt and returns its text, bounded by stop and
// max_tokens.
func (s *Server) runPrompt(r *http.Request, req openai.CompletionRequest, prompt, conversationID string) completionRun {
	limit, err := openai.NewTextLimit(req.Stop, req.MaxTokens)
	if err != nil {
		return completionRun{err: err}
	}

	var text strings.Builder
	var steps openai.StepLog
	tools := newTurnLog(s.log, conversationID)

	result, err := s.manager.Prompt(r.Context(), session.Request{
		Model:          req.Model,
		ConversationID: conversationID,
		Workspace:      req.Workspace,
		Prompt:         prompt,
		OnSession:      tools.bind,
	}, func(u acp.SessionUpdate) error {
		piece, _, step := tools.update(u)
		steps.Add(step)
		if piece == "" {
			return nil
		}
		emit, _ := limit.Push(piece)
		text.WriteString(emit)
		return nil
	})
	if err != nil {
		return completionRun{err: err}
	}
	text.WriteString(limit.Finish())
	tools.answered(text.String(), result.StopReason)

	return completionRun{
		text:   text.String(),
		stop:   result.StopReason,
		capped: limit.Capped(),
		acp: &openai.ACPMeta{
			Agent:          result.Agent,
			SessionID:      result.SessionID,
			ConversationID: result.ConversationID,
			StopReason:     result.StopReason,
			Steps:          steps.Steps(),
		},
	}
}

// blockingCompletion runs every prompt and returns all choices.
//
// The legacy API returns n completions per prompt, so the work list is the
// prompts crossed with n.
func (s *Server) blockingCompletion(w http.ResponseWriter, r *http.Request, req openai.CompletionRequest, prompts []string, ignored []string) {
	repeats := 1
	if req.N != nil && *req.N > 1 {
		repeats = *req.N
	}

	type job struct {
		prompt         string
		conversationID string
	}
	jobs := make([]job, 0, len(prompts)*repeats)
	for _, prompt := range prompts {
		for repeat := 0; repeat < repeats; repeat++ {
			// A conversation only makes sense for a single prompt asked once;
			// independent choices need independent sessions.
			conversationID := ""
			if len(prompts) == 1 && repeats == 1 {
				conversationID = conversationIDFor(req.ConversationID, s.chatHeader(r), req.User)
			}
			jobs = append(jobs, job{prompt: prompt, conversationID: conversationID})
		}
	}

	results := make([]completionRun, len(jobs))
	var wg sync.WaitGroup
	for i, work := range jobs {
		wg.Add(1)
		go func(i int, work job) {
			defer wg.Done()
			results[i] = s.runPrompt(r, req, work.prompt, work.conversationID)
		}(i, work)
	}
	wg.Wait()

	for _, result := range results {
		if result.err != nil {
			s.writeAgentError(w, result.err)
			return
		}
	}

	choices := make([]openai.CompletionChoice, 0, len(results))
	var first *openai.ACPMeta
	totalIn, totalOut := 0, 0

	for i, result := range results {
		text := result.text
		if req.Echo != nil && *req.Echo {
			text = jobs[i].prompt + text
		}
		choices = append(choices, openai.CompletionChoice{
			Text:         text,
			Index:        i,
			FinishReason: completionFinish(result),
		})
		totalIn += openai.EstimateTokens(jobs[i].prompt)
		totalOut += openai.EstimateTokens(result.text)
		if first == nil {
			first = result.acp
		}
	}
	if first != nil {
		first.IgnoredParams = ignored
	}

	writeJSON(w, http.StatusOK, openai.CompletionResponse{
		ID:      openai.NewID("cmpl"),
		Object:  openai.ObjectTextCompletion,
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: choices,
		Usage: &openai.Usage{
			PromptTokens:     totalIn,
			CompletionTokens: totalOut,
			TotalTokens:      totalIn + totalOut,
		},
		ACP: first,
	})
}

// completionFinish reports why a completion ended.
func completionFinish(result completionRun) string {
	if result.capped {
		return openai.FinishLength
	}
	return openai.FinishReason(result.stop)
}

// streamCompletion streams one prompt as legacy text_completion events.
func (s *Server) streamCompletion(w http.ResponseWriter, r *http.Request, req openai.CompletionRequest, prompt string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, openai.ErrTypeServer, "stream_unsupported",
			"this server cannot stream responses", "")
		return
	}

	id := openai.NewID("cmpl")
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
	delta := func(text string, finish *string) {
		send(openai.CompletionChunk{
			ID: id, Object: openai.ObjectTextCompletion, Created: created, Model: req.Model,
			Choices: []openai.CompletionDelta{{Text: text, Index: 0, FinishReason: finish}},
		})
	}

	limit, err := openai.NewTextLimit(req.Stop, req.MaxTokens)
	if err != nil {
		delta("", nil)
		_ = writeSSEDone(w)
		flusher.Flush()
		return
	}

	conversationID := conversationIDFor(req.ConversationID, s.chatHeader(r), req.User)
	tools := newTurnLog(s.log, conversationID)
	var text strings.Builder
	var steps openai.StepLog
	result, promptErr := s.manager.Prompt(r.Context(), session.Request{
		Model:          req.Model,
		ConversationID: conversationID,
		Workspace:      req.Workspace,
		Prompt:         prompt,
		OnSession:      tools.bind,
	}, func(u acp.SessionUpdate) error {
		piece, _, step := tools.update(u)
		steps.Add(step)
		if piece == "" {
			return nil
		}
		emit, _ := limit.Push(piece)
		if emit != "" {
			text.WriteString(emit)
			delta(emit, nil)
		}
		return nil
	})

	if promptErr != nil {
		send(openai.ErrorResponse{Error: openai.ErrorBody{
			Message: promptErr.Error(), Type: openai.ErrTypeServer, Code: "agent_error",
		}})
		_ = writeSSEDone(w)
		flusher.Flush()
		return
	}

	if rest := limit.Finish(); rest != "" {
		text.WriteString(rest)
		delta(rest, nil)
	}
	tools.answered(text.String(), result.StopReason)

	finish := openai.FinishReason(result.StopReason)
	if limit.Capped() {
		finish = openai.FinishLength
	}
	delta("", &finish)
	_ = writeSSEDone(w)
	flusher.Flush()
}

// conversationIDFor resolves a conversation id from either extension field.
// conversationIDFor resolves the key that makes a session persistent, in the
// order of how explicit the caller was: the body's own field, then the per-chat
// header, then `user`, which exists for clients that cannot set a custom field.
func conversationIDFor(conversationID, header, user string) string {
	if conversationID != "" {
		return conversationID
	}
	if header != "" {
		return header
	}
	return user
}
