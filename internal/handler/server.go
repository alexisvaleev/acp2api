// Package handler exposes the OpenAI-compatible HTTP surface.
//
// Handlers stay thin: they decode, validate, delegate to the session manager,
// and render. No ACP detail leaks past this package's mapping calls.
package handler

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// Server is the HTTP surface.
type Server struct {
	manager            *session.Manager
	token              string
	log                *slog.Logger
	started            time.Time
	conversationHeader string
	responses          *responseStore
}

// chatHeader returns the per-chat identifier the client sent, if the deployment
// named a header and the client used it.
//
// It exists for clients that cannot put an extension field in the request body
// but can send a header per chat. Open WebUI is the motivating one: with
// ENABLE_FORWARD_USER_INFO_HEADERS it sends X-OpenWebUI-Chat-Id, which is the
// only per-chat identifier it exposes.
func (s *Server) chatHeader(r *http.Request) string {
	if s.conversationHeader == "" {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(s.conversationHeader))
}

// Options configures a Server.
type Options struct {
	// Token is the bearer token required on /v1/* routes. Empty disables auth,
	// which config validation only permits for a loopback bind.
	Token string
	// Logger receives request and agent diagnostics.
	Logger *slog.Logger
	// ConversationHeader names the header a client may send to key a session,
	// for clients that cannot put an extension field in the body. Empty disables
	// the mechanism.
	ConversationHeader string
}

// New creates a server over the given session manager.
func New(manager *session.Manager, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		manager:            manager,
		token:              opts.Token,
		log:                log,
		started:            time.Now(),
		conversationHeader: opts.ConversationHeader,
		responses:          newResponseStore(defaultResponseLimit),
	}
}

// Handler returns the routed handler with authentication applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /v1/models/{id}", s.handleGetModel)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/completions", s.handleCompletions)
	mux.HandleFunc("POST /v1/responses", s.handleCreateResponse)
	mux.HandleFunc("GET /v1/responses/{id}", s.handleGetResponse)
	mux.HandleFunc("DELETE /v1/responses/{id}", s.handleDeleteResponse)
	s.registerUnsupported(mux)
	mux.HandleFunc("GET /{$}", s.handleRoot)
	return s.withAuth(mux)
}

// handleGetModel serves GET /v1/models/{id}, for an agent or one of its models.
func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	created := s.started.Unix()

	for _, a := range s.manager.Agents() {
		if a.ID == id {
			writeJSON(w, http.StatusOK, openai.Model{
				ID: a.ID, Object: openai.ObjectModel, Created: created, OwnedBy: "acp2api",
			})
			return
		}
	}

	// A model id arrives as "agent/model" when the caller encodes the slash.
	if agentID, modelID, ok := strings.Cut(id, "/"); ok {
		for _, model := range s.manager.Models(agentID) {
			if model.ID == modelID {
				writeJSON(w, http.StatusOK, openai.Model{
					ID: id, Object: openai.ObjectModel, Created: created, OwnedBy: "acp2api",
				})
				return
			}
		}
	}

	writeError(w, http.StatusNotFound, openai.ErrTypeInvalidRequest, "model_not_found",
		fmt.Sprintf("model %q is unknown; available: %s", id, strings.Join(agentIDs(s.manager), ", ")), "")
}

// handleHealth is unauthenticated so a load balancer or a container runtime can
// probe it without credentials.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"uptime_seconds": int(time.Since(s.started).Seconds()),
	})
}

// handleRoot describes the service, so a bare GET is not a mystery 404.
func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "acp2api",
		"object":  "service",
		"served":  []string{"GET /healthz", "GET /v1/models", "GET /v1/models/{id}", "POST /v1/chat/completions", "POST /v1/completions", "POST /v1/responses", "GET /v1/responses/{id}", "DELETE /v1/responses/{id}"},
		"refused": unsupportedRoutes(),
		"agents":  agentIDs(s.manager),
		"message": "OpenAI-compatible gateway for ACP agents; endpoints without an ACP equivalent return 501",
	})
}

// handleModels lists the configured agents as OpenAI models, plus each agent's
// own models once its catalog has been discovered.
//
// A model id is "agent/model" — "devin/claude-opus-5-5-medium" — which is what
// the chat endpoints accept in their model field. An agent whose catalog is not
// known yet appears alone; discovery costs the agent's cold start, so it is not
// done here.
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	created := s.started.Unix()
	list := openai.ModelList{Object: openai.ObjectList}

	for _, a := range s.manager.Agents() {
		list.Data = append(list.Data, openai.Model{
			ID:      a.ID,
			Object:  openai.ObjectModel,
			Created: created,
			OwnedBy: "acp2api",
		})
		for _, model := range s.manager.Models(a.ID) {
			list.Data = append(list.Data, openai.Model{
				ID:      a.ID + "/" + model.ID,
				Object:  openai.ObjectModel,
				Created: created,
				OwnedBy: "acp2api",
			})
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// agentIDs returns the configured agent ids.
func agentIDs(m *session.Manager) []string {
	agents := m.Agents()
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	return ids
}

/* ---- middleware ---- */

// withAuth requires a bearer token on every /v1/* route when one is configured.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authorised(r) {
			writeError(w, http.StatusUnauthorized, openai.ErrTypeAuth, "invalid_api_key",
				"missing or invalid bearer token", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorised compares the request's bearer token in constant time.
func (s *Server) authorised(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return constantTimeEqual(strings.TrimPrefix(header, prefix), s.token)
}

// effortOf reads an optional effort pointer, the flat chat form. An explicit
// empty string means "not given", which is how a caller clears the level.
func effortOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// chatEffort resolves the effort a chat request asks for. reasoning_effort is
// the OpenAI field; reasoning is the object form clients send when they speak
// the Responses shape, and its effort key means the same thing here rather than
// being dropped.
//
// The flat field wins when both are present: it is the surface's own name, and
// a caller that set both would otherwise have named one level twice.
func chatEffort(req openai.ChatCompletionRequest) string {
	if effort := effortOf(req.ReasoningEffort); effort != "" {
		return effort
	}
	return openai.ReasoningEffort(req.Reasoning)
}

// checkEffort refuses an effort level no model catalog can carry.
//
// The catalog vocabulary is fixed and small, so an unknown level is a caller
// error we can name exactly — and naming it here fails the request before an
// agent process is spawned, rather than after its cold start has been paid for.
func (s *Server) checkEffort(w http.ResponseWriter, effort string) bool {
	if openai.ReasoningEffortIsKnown(effort) {
		return true
	}
	writeError(w, http.StatusBadRequest, openai.ErrTypeInvalidRequest,
		"invalid_reasoning_effort",
		fmt.Sprintf("reasoning_effort %q is not a level an agent catalog carries; use one of: %s",
			effort, strings.Join(openai.EffortLevels, ", ")), "reasoning_effort")
	return false
}

// writeJSON renders a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders the OpenAI error envelope.
func writeError(w http.ResponseWriter, status int, errType, code, message, param string) {
	writeJSON(w, status, openai.ErrorResponse{Error: openai.ErrorBody{
		Message: message,
		Type:    errType,
		Code:    code,
		Param:   param,
	}})
}

// constantTimeEqual compares two secrets without leaking their contents through
// timing.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
