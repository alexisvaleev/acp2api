package handler

import (
	"fmt"
	"net/http"

	"github.com/quonaro/acp2api/internal/openai"
)

// unsupportedEndpoint is an OpenAI route this gateway cannot serve.
//
// Each one is answered with a structured 501 naming the reason, rather than a
// 404 that looks like a typo or a fabricated success. An ACP agent is a
// stateful coding agent: it produces text and edits files. It cannot embed,
// transcribe, classify, or generate an image, and pretending otherwise would be
// the worst possible behaviour for a compatibility layer.
type unsupportedEndpoint struct {
	method string
	path   string
	reason string
}

// unsupportedEndpoints is the full list of non-mappable routes. It is the
// source of truth for the endpoint table in the README, and a test asserts the
// two agree.
var unsupportedEndpoints = []unsupportedEndpoint{
	{"POST", "/v1/embeddings", "an ACP agent produces text, not embedding vectors"},
	{"POST", "/v1/moderations", "there is no classifier behind an agent"},
	{"POST", "/v1/images/generations", "an ACP agent does not generate images"},
	{"POST", "/v1/images/edits", "an ACP agent does not edit images"},
	{"POST", "/v1/images/variations", "an ACP agent does not generate image variations"},
	{"POST", "/v1/audio/speech", "an ACP agent produces text, not audio"},
	{"POST", "/v1/audio/transcriptions", "an ACP agent does not transcribe audio"},
	{"POST", "/v1/audio/translations", "an ACP agent does not translate audio"},
	{"GET", "/v1/files", "the gateway keeps no file store"},
	{"POST", "/v1/files", "the gateway keeps no file store"},
	{"GET", "/v1/files/{id}", "the gateway keeps no file store"},
	{"DELETE", "/v1/files/{id}", "the gateway keeps no file store"},
	{"POST", "/v1/batches", "the gateway runs no batch queue"},
	{"GET", "/v1/batches", "the gateway runs no batch queue"},
	{"POST", "/v1/fine_tuning/jobs", "an ACP agent is not a trainable model"},
	{"GET", "/v1/fine_tuning/jobs", "an ACP agent is not a trainable model"},
	{"POST", "/v1/assistants", "the Assistants API is superseded; use POST /v1/responses"},
	{"GET", "/v1/assistants", "the Assistants API is superseded; use POST /v1/responses"},
	{"POST", "/v1/threads", "the Assistants API is superseded; use POST /v1/responses"},
	{"POST", "/v1/vector_stores", "the gateway keeps no vector store"},
}

// registerUnsupported installs the 501 handlers for every non-mappable route.
func (s *Server) registerUnsupported(mux *http.ServeMux) {
	for _, endpoint := range unsupportedEndpoints {
		reason := endpoint.reason
		mux.HandleFunc(endpoint.method+" "+endpoint.path, func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotImplemented, openai.ErrTypeInvalidRequest,
				openai.CodeUnsupportedEndpoint,
				fmt.Sprintf("%s %s has no ACP equivalent: %s", r.Method, r.URL.Path, reason), "")
		})
	}
}

// unsupportedRoutes returns the registered routes, for documentation checks.
func unsupportedRoutes() []string {
	routes := make([]string, 0, len(unsupportedEndpoints))
	for _, endpoint := range unsupportedEndpoints {
		routes = append(routes, endpoint.method+" "+endpoint.path)
	}
	return routes
}
