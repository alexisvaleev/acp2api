package handler_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

func TestNonMappableEndpointsReturn501(t *testing.T) {
	srv := newTestServer(t, nil, "")

	routes := []struct{ method, path string }{
		{"POST", "/v1/embeddings"},
		{"POST", "/v1/moderations"},
		{"POST", "/v1/images/generations"},
		{"POST", "/v1/audio/speech"},
		{"GET", "/v1/files"},
		{"POST", "/v1/files"},
		{"GET", "/v1/files/abc"},
		{"DELETE", "/v1/files/abc"},
		{"POST", "/v1/batches"},
		{"POST", "/v1/fine_tuning/jobs"},
		{"POST", "/v1/assistants"},
		{"POST", "/v1/threads"},
		{"POST", "/v1/vector_stores"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req, err := http.NewRequest(route.method, srv.URL+route.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", resp.StatusCode)
			}
			var failure openai.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != openai.CodeUnsupportedEndpoint {
				t.Fatalf("error code = %q", failure.Error.Code)
			}
			if !strings.Contains(failure.Error.Message, "no ACP equivalent") {
				t.Fatalf("error message = %q", failure.Error.Message)
			}
		})
	}
}

func TestRootListsServedAndRefusedRoutes(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		Served  []string `json:"served"`
		Refused []string `json:"refused"`
		Agents  []string `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Served) == 0 || len(body.Refused) == 0 {
		t.Fatalf("root = %+v", body)
	}
	if len(body.Agents) != 1 || body.Agents[0] != "fake" {
		t.Fatalf("agents = %v", body.Agents)
	}
}

func TestGetOneModel(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp, err := srv.Client().Get(srv.URL + "/v1/models/fake")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var model openai.Model
	if err := json.NewDecoder(resp.Body).Decode(&model); err != nil {
		t.Fatal(err)
	}
	if model.ID != "fake" || model.Object != openai.ObjectModel {
		t.Fatalf("model = %+v", model)
	}

	missing, err := srv.Client().Get(srv.URL + "/v1/models/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown model status = %d, want 404", missing.StatusCode)
	}
}

func TestNReturnsIndependentChoices(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"n":        3,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 3 {
		t.Fatalf("choices = %d, want 3", len(completion.Choices))
	}
	for i, choice := range completion.Choices {
		if choice.Index != i {
			t.Fatalf("choice %d has index %d", i, choice.Index)
		}
		if choice.Message.ContentString() == "" {
			t.Fatalf("choice %d is empty", i)
		}
	}
	if completion.Usage == nil || completion.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestNAboveTheCapIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"n":        100,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Param != "n" {
		t.Fatalf("error = %+v", failure.Error)
	}
}

func TestStreamingWithNIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"n":        2,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestReadmeDocumentsEveryRefusedRoute keeps the endpoint table honest: a route
// the gateway refuses must be listed in the README.
func TestReadmeDocumentsEveryRefusedRoute(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Refused []string `json:"refused"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	text := string(readme)

	for _, route := range body.Refused {
		path := strings.SplitN(route, " ", 2)[1]
		if !strings.Contains(text, path) {
			t.Fatalf("route %q is refused but not documented in the README", route)
		}
	}
}
