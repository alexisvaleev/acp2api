// Package handler exposes the OpenAI-compatible HTTP surface.
//
// Handlers stay thin: they decode, validate, delegate to the session manager,
// and render. No ACP detail leaks past this package's mapping calls.
package handler

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

// Server is the HTTP surface.
type Server struct {
	manager   *session.Manager
	token     string
	log       *slog.Logger
	started   time.Time
	responses *responseStore
}

// Options configures a Server.
type Options struct {
	// Token is the bearer token required on /v1/* routes. Empty disables auth,
	// which config validation only permits for a loopback bind.
	Token string
	// Logger receives request and agent diagnostics.
	Logger *slog.Logger
}

// New creates a server over the given session manager.
func New(manager *session.Manager, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		manager:   manager,
		token:     opts.Token,
		log:       log,
		started:   time.Now(),
		responses: newResponseStore(defaultResponseLimit),
	}
}

// Handler returns the routed handler with authentication applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/responses", s.handleCreateResponse)
	mux.HandleFunc("GET /v1/responses/{id}", s.handleGetResponse)
	mux.HandleFunc("DELETE /v1/responses/{id}", s.handleDeleteResponse)
	mux.HandleFunc("GET /{$}", s.handleRoot)
	return s.withAuth(mux)
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
		"routes":  []string{"GET /healthz", "GET /v1/models", "POST /v1/chat/completions"},
		"agents":  agentIDs(s.manager),
		"message": "OpenAI-compatible gateway for ACP agents",
	})
}

// handleModels lists the configured agents as OpenAI models.
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
