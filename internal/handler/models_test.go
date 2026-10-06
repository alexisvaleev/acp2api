package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// listModels fetches /v1/models and returns the ids.
func listModels(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var list openai.ModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestModelSelectionIsApplied(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake/fake-model-2",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestUnknownModelIsRefused covers the honesty rule: a caller who named a model
// must not silently get the agent's default instead.
func TestUnknownModelIsRefused(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake/no-such-model",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no-such-model", "fake-model-1", "fake-model-2"} {
		if !strings.Contains(failure.Error.Message, want) {
			t.Fatalf("the error should mention %q: %s", want, failure.Error.Message)
		}
	}
}

// TestModelsAppearAfterFirstUse covers discovery: the catalog comes from the
// agent and only exists once it has been talked to.
func TestModelsAppearAfterFirstUse(t *testing.T) {
	srv := newTestServer(t, nil, "")

	before := listModels(t, srv)
	if containsString(before, "fake/fake-model-1") {
		t.Fatal("the catalog should not be known before any request")
	}

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()

	after := listModels(t, srv)
	for _, want := range []string{"fake", "fake/fake-model-1", "fake/fake-model-2"} {
		if !containsString(after, want) {
			t.Fatalf("model %q is missing from %v", want, after)
		}
	}
}

func TestModelsAreSortedAndNamed(t *testing.T) {
	srv := newTestServer(t, nil, "")
	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()

	ids := listModels(t, srv)
	var models []string
	for _, id := range ids {
		if strings.HasPrefix(id, "fake/") {
			models = append(models, id)
		}
	}
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
	if models[0] != "fake/fake-model-1" || models[1] != "fake/fake-model-2" {
		t.Fatalf("models are not sorted: %v", models)
	}
}

func TestGetOneDiscoveredModel(t *testing.T) {
	srv := newTestServer(t, nil, "")
	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()

	// The slash is encoded so it stays inside one path segment.
	got, err := srv.Client().Get(srv.URL + "/v1/models/fake%2Ffake-model-1")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", got.StatusCode)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
}
