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

// handleCreateResponse serves POST /v1/responses, streaming or not.
func (s *Server) handleCreateResponse(w http.ResponseWriter, r *http.Request) {
	var req openai.ResponsesRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_json",
			"request body is not valid JSON: "+err.Error(), "")
		return
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

	choice, err := openai.ParseToolChoice(req.ToolChoice)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_tool_choice",
			err.Error(), "tool_choice")
		return
	}

	messages, err := openai.InputMessages(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_input", err.Error(), "input")
		return
	}

	responseID := openai.NewID("resp")
	conversationID, ok := s.resumeConversation(w, req)
	if !ok {
		return
	}
	// A stored response implies a resumable session, so give it a conversation
	// even when the caller supplied none.
	if conversationID == "" && s.shouldStore(req) {
		conversationID = "resp:" + responseID
	}

	prompt, err := openai.BuildTurn(messages, conversationID != "", req.Tools, choice)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest, "invalid_input", err.Error(), "input")
		return
	}
	// ACP sessions have no system prompt, so instructions ride on every turn.
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		prompt = "## instructions\n" + instructions + "\n\n" + prompt
	}

	images, err := openai.ImageParts(messages)
	if err != nil {
		writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
			openai.CodeUnsupportedParameter, err.Error(), openai.ParamImageURL)
		return
	}

	plan := responsePlan{
		req:            req,
		responseID:     responseID,
		prompt:         prompt,
		tools:          len(req.Tools) > 0 && choice.UsesCallerTools(),
		conversationID: conversationID,
		turn: session.Request{
			Model:          req.Model,
			ConversationID: conversationID,
			Workspace:      req.Workspace,
			Prompt:         prompt,
			Parts:          contentParts(prompt, images),
		},
	}

	if req.Stream {
		s.streamResponse(w, r, plan)
		return
	}
	s.blockingResponse(w, r, plan)
}

// responsePlan is everything both renderers need for one response.
type responsePlan struct {
	req            openai.ResponsesRequest
	responseID     string
	turn           session.Request
	prompt         string
	conversationID string
	tools          bool
}

// resumeConversation resolves the ACP conversation for the request.
func (s *Server) resumeConversation(w http.ResponseWriter, req openai.ResponsesRequest) (string, bool) {
	if req.PreviousResponseID == "" {
		if req.ConversationID != "" {
			return req.ConversationID, true
		}
		return req.User, true
	}

	entry, found := s.responses.get(req.PreviousResponseID)
	if !found {
		writeError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest, "response_not_found",
			fmt.Sprintf("previous_response_id %q is unknown, or the response was not stored",
				req.PreviousResponseID), "previous_response_id")
		return "", false
	}
	return entry.conversationID, true
}

// shouldStore reports whether the response must be kept. The OpenAI API
// defaults to storing.
func (s *Server) shouldStore(req openai.ResponsesRequest) bool {
	return req.Store == nil || *req.Store
}

// blockingResponse runs the turn and returns one complete response.
func (s *Server) blockingResponse(w http.ResponseWriter, r *http.Request, plan responsePlan) {
	var text strings.Builder
	var steps openai.StepLog

	result, err := s.manager.Prompt(r.Context(), plan.turn, func(u acp.SessionUpdate) error {
		piece, _, step := openai.FromUpdate(u)
		text.WriteString(piece)
		steps.Add(step)
		return nil
	})
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	content := text.String()
	var calls []openai.ToolCall
	if plan.tools {
		if parsed := openai.ParseToolCalls(content); len(parsed) > 0 {
			calls, content = openai.LimitCalls(parsed, plan.req.ParallelToolCalls), ""
		}
	}

	items := outputItems(content, calls)
	response := plan.build(result, items, steps, content)

	if s.shouldStore(plan.req) {
		s.responses.put(response.ID, result.ConversationID, response)
	}
	writeJSON(w, http.StatusOK, response)
}

// outputItems renders a turn's result as output items. A turn either answers or
// asks the caller to run something, never both.
func outputItems(content string, calls []openai.ToolCall) []openai.OutputItem {
	if len(calls) > 0 {
		items := make([]openai.OutputItem, 0, len(calls))
		for _, call := range calls {
			items = append(items, openai.NewFunctionCallItem(call))
		}
		return items
	}
	return []openai.OutputItem{openai.NewMessageItem(content)}
}

// build assembles the response object.
func (p responsePlan) build(result session.Result, items []openai.OutputItem, steps openai.StepLog, content string) *openai.Response {
	response := &openai.Response{
		ID:                 p.responseID,
		Object:             openai.ObjectResponse,
		CreatedAt:          time.Now().Unix(),
		Status:             responseStatus(result.StopReason),
		Model:              p.req.Model,
		Output:             items,
		OutputText:         openai.OutputTextOf(items),
		Usage:              responseUsage(p.prompt, content),
		PreviousResponseID: p.req.PreviousResponseID,
		Instructions:       p.req.Instructions,
		Metadata:           p.req.Metadata,
		ACP: &openai.ACPMeta{
			Agent:          result.Agent,
			SessionID:      result.SessionID,
			ConversationID: result.ConversationID,
			StopReason:     result.StopReason,
			Steps:          steps.Steps(),
		},
	}
	return response
}

// responseStatus maps an ACP stop reason onto the Responses API status.
func responseStatus(stopReason string) string {
	switch stopReason {
	case acp.StopMaxTokens, acp.StopRefusal, acp.StopCancelled:
		return openai.StatusIncomplete
	default:
		return openai.StatusCompleted
	}
}

// responseUsage builds the Responses API usage block.
func responseUsage(prompt, completion string) *openai.ResponseUsage {
	in := openai.EstimateTokens(prompt)
	out := openai.EstimateTokens(completion)
	return &openai.ResponseUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

// handleGetResponse serves GET /v1/responses/{id}.
func (s *Server) handleGetResponse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, found := s.responses.get(id)
	if !found {
		writeError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest, "response_not_found",
			fmt.Sprintf("response %q is unknown, or was not stored", id), "")
		return
	}
	writeJSON(w, http.StatusOK, entry.response)
}

// handleDeleteResponse serves DELETE /v1/responses/{id}.
func (s *Server) handleDeleteResponse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.responses.delete(id) {
		writeError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest, "response_not_found",
			fmt.Sprintf("response %q is unknown, or was not stored", id), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "response.deleted", "deleted": true})
}
