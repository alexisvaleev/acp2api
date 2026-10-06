package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// postPath sends a JSON body to any path and returns the response.
func postPath(t *testing.T, srv *httptest.Server, token, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func decodeResponse(t *testing.T, resp *http.Response) openai.Response {
	t.Helper()
	defer resp.Body.Close()
	var out openai.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestResponsesStringInput(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "hello there",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	out := decodeResponse(t, resp)
	if out.Object != openai.ObjectResponse || out.ID == "" {
		t.Fatalf("response = %+v", out)
	}
	if out.Status != openai.StatusCompleted {
		t.Fatalf("status = %q", out.Status)
	}
	if len(out.Output) != 1 || out.Output[0].Type != openai.ItemMessage {
		t.Fatalf("output = %+v", out.Output)
	}
	if out.Output[0].Role != "assistant" || out.Output[0].Status != openai.StatusCompleted {
		t.Fatalf("message item = %+v", out.Output[0])
	}
	if out.OutputText == "" || out.OutputText != out.Output[0].Content[0].Text {
		t.Fatalf("output_text = %q", out.OutputText)
	}
	if out.Usage == nil || out.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %+v", out.Usage)
	}
	if out.ACP == nil || out.ACP.Agent != "fake" {
		t.Fatalf("acp meta = %+v", out.ACP)
	}
}

func TestResponsesArrayInputWithMessageItems(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "hello"}}},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	out := decodeResponse(t, resp)
	if len(out.Output) != 1 || out.OutputText == "" {
		t.Fatalf("response = %+v", out)
	}
}

func TestResponsesAreStoredAndRetrievable(t *testing.T) {
	srv := newTestServer(t, nil, "")

	created := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "remember me",
	}))

	get, err := srv.Client().Get(srv.URL + "/v1/responses/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", get.StatusCode)
	}
	var fetched openai.Response
	if err := json.NewDecoder(get.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.ID != created.ID || fetched.OutputText != created.OutputText {
		t.Fatalf("fetched = %+v, want %+v", fetched, created)
	}
}

func TestResponsesStoreFalseIsNotRetrievable(t *testing.T) {
	srv := newTestServer(t, nil, "")

	created := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "do not keep me",
		"store": false,
	}))

	get, err := srv.Client().Get(srv.URL + "/v1/responses/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status = %d, want 404", get.StatusCode)
	}
}

func TestResponsesDelete(t *testing.T) {
	srv := newTestServer(t, nil, "")

	created := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "delete me",
	}))

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/responses/"+created.ID, nil)
	del, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d", del.StatusCode)
	}

	get, err := srv.Client().Get(srv.URL + "/v1/responses/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", get.StatusCode)
	}
}

func TestResponsesPreviousResponseIDResumesTheSession(t *testing.T) {
	srv := newTestServer(t, nil, "")

	first := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "first turn",
	}))
	if first.ACP == nil || first.ACP.SessionID == "" {
		t.Fatal("expected a session id on the first response")
	}

	second := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":                "fake",
		"input":                "second turn",
		"previous_response_id": first.ID,
	}))

	if second.ACP == nil {
		t.Fatal("expected acp meta on the second response")
	}
	if second.ACP.SessionID != first.ACP.SessionID {
		t.Fatalf("previous_response_id did not resume the session: %q then %q",
			first.ACP.SessionID, second.ACP.SessionID)
	}
	if second.PreviousResponseID != first.ID {
		t.Fatalf("previous_response_id = %q, want %q", second.PreviousResponseID, first.ID)
	}
}

func TestResponsesUnknownPreviousResponseIDIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":                "fake",
		"input":                "hi",
		"previous_response_id": "resp-does-not-exist",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Param != "previous_response_id" {
		t.Fatalf("error = %+v", failure.Error)
	}
}

func TestResponsesInstructionsReachTheAgent(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_ECHO": "1"}, "")

	out := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":        "fake",
		"instructions": "Always answer in haiku.",
		"input":        "tell me about rain",
	}))

	if !strings.Contains(out.OutputText, "Always answer in haiku.") {
		t.Fatalf("the instructions never reached the agent: %q", out.OutputText)
	}
}

func TestResponsesFunctionCall(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	out := decodeResponse(t, postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": "weather in Paris?",
		"tools": weatherTool(),
	}))

	if len(out.Output) != 1 || out.Output[0].Type != openai.ItemFunctionCall {
		t.Fatalf("output = %+v", out.Output)
	}
	call := out.Output[0]
	if call.Name != "get_weather" || call.CallID == "" || !strings.Contains(call.Arguments, "Paris") {
		t.Fatalf("function call item = %+v", call)
	}
	if out.OutputText != "" {
		t.Fatalf("output_text = %q, want empty for a call", out.OutputText)
	}
}
