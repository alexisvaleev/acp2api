package handler

import (
	"encoding/json"
	"fmt"
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
	// history and only the newest turn is sent to it. Without one the agent
	// starts from nothing, so the whole transcript must be flattened into the
	// prompt.
	conversationID := conversationIDFor(req.ConversationID, s.chatHeader(r), req.User)

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

	// The whole transcript, which is what an ephemeral session needs and what a
	// session created for this request needs: the agent then holds no history.
	replay, err := openai.BuildTurn(req.Messages, false, req.Tools, choice)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_messages", err.Error(), "messages")
		return
	}
	// A session that already exists holds the history itself, so replaying the
	// transcript into it would duplicate what it can already see.
	prompt := replay
	if conversationID != "" {
		if prompt, err = openai.BuildTurn(req.Messages, true, req.Tools, choice); err != nil {
			writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_messages", err.Error(), "messages")
			return
		}
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
		prefix := instruction + "\n"
		prompt = prefix + prompt
		replay = prefix + replay
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
			Effort:         effortOf(req.ReasoningEffort),
			ConversationID: conversationID,
			Workspace:      req.Workspace,
			Prompt:         prompt,
			Parts:          contentParts(prompt, images),
			Replay:         replay,
			ReplayParts:    contentParts(replay, images),
		},
	}

	if req.Stream {
		if req.N != nil && *req.N > 1 {
			writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "unsupported_stream_choices",
				"streaming is not supported together with n > 1; send one choice or disable stream", "stream")
			return
		}
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
	text      string
	reasoning string
	calls     []openai.ToolCall
	result    session.Result
	steps     openai.StepLog
	capped    bool
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
	var reasoning strings.Builder
	var steps openai.StepLog
	tools := newTurnLog(s.log, turn.ConversationID)
	turn.OnSession = tools.bind

	result, err := s.manager.Prompt(r.Context(), turn, func(u acp.SessionUpdate) error {
		piece, thought, step := tools.update(u)
		steps.Add(step)
		reasoning.WriteString(thought)
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

	out := turnOutcome{
		text:      text.String(),
		reasoning: reasoning.String(),
		result:    result,
		steps:     steps,
		capped:    limit.Capped(),
	}
	if plan.tools {
		// A tool call is the whole message: the envelope is consumed, leaving
		// no prose behind.
		if parsed := openai.ParseToolCalls(out.text); len(parsed) > 0 {
			out.calls = openai.LimitCalls(parsed, plan.req.ParallelToolCalls)
			out.text = ""
		}
	}
	// The reply is the raw text the agent produced — a tool-call envelope when
	// it asked for a caller function, prose otherwise.
	tools.answered(text.String(), result.StopReason)
	tools.callerCalls(out.calls)
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

// blockingTurn runs the turn and returns one complete completion, or n of them.
func (s *Server) blockingTurn(w http.ResponseWriter, r *http.Request, plan turnPlan) {
	choices := plan.req.N
	if choices == nil || *choices < 1 {
		one := 1
		choices = &one
	}
	// Independent choices need independent sessions, so a multi-choice request
	// does not run on a shared conversation.
	if *choices > 1 {
		plan.turn.ConversationID = ""
	}

	out := make([]openai.Choice, 0, *choices)
	var meta *openai.ACPMeta
	totalIn, totalOut := 0, 0

	for i := 0; i < *choices; i++ {
		run, err := s.runTurn(r, plan, plan.turn.Prompt, true)
		if err != nil {
			s.writeAgentError(w, err)
			return
		}
		if plan.format.Active() && len(run.calls) == 0 {
			run, err = s.enforceFormat(r, plan, run)
			if err != nil {
				writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
					"invalid_response_format", err.Error(), "response_format")
				return
			}
		}

		finish := openai.FinishReason(run.result.StopReason)
		if run.capped {
			finish = openai.FinishLength
		}
		if len(run.calls) > 0 {
			finish = openai.FinishToolCalls
		}

		out = append(out, openai.Choice{
			Index:        i,
			Message:      openai.NewResponseMessage(run.text, run.reasoning, run.calls),
			FinishReason: finish,
		})
		totalIn += openai.EstimateTokens(plan.prompt)
		totalOut += openai.EstimateTokens(run.text)
		if meta == nil {
			meta = acpMeta(run.result, run.steps, plan.ignored)
		}
	}

	writeJSON(w, http.StatusOK, openai.ChatCompletionResponse{
		ID:      openai.NewID("chatcmpl"),
		Object:  openai.ObjectChatCompletion,
		Created: time.Now().Unix(),
		Model:   plan.req.Model,
		Choices: out,
		Usage: &openai.Usage{
			PromptTokens:     totalIn,
			CompletionTokens: totalOut,
			TotalTokens:      totalIn + totalOut,
		},
		ACP: meta,
	})
}
