package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// tinyPNG is a one-pixel PNG as a data URL, the smallest valid image payload.
const tinyPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func imageMessage() []map[string]any {
	return []map[string]any{{
		"role": "user",
		"content": []map[string]any{
			{"type": "text", "text": "what is in this image?"},
			{"type": "image_url", "image_url": map[string]string{"url": tinyPNG}},
		},
	}}
}

func TestImageReachesTheAgentAsAnImageBlock(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_ECHO":   "1",
		"FAKE_AGENT_IMAGES": "1",
	}, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": imageMessage(),
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}

	// The agent echoes what it received: the placeholder text plus an image
	// block proves the image travelled as an ACP image content block.
	content := completion.Choices[0].Message.ContentString()
	if !strings.Contains(content, "[image]") {
		t.Fatalf("the image never reached the agent as a block: %q", content)
	}
	if !strings.Contains(content, "what is in this image?") {
		t.Fatalf("the text part was lost: %q", content)
	}
}

func TestImageIsRefusedWhenTheAgentCannotReadIt(t *testing.T) {
	// The agent does not advertise image support.
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": imageMessage(),
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != openai.CodeUnsupportedParameter {
		t.Fatalf("error code = %q", failure.Error.Code)
	}
	if failure.Error.Param != openai.ParamImageURL {
		t.Fatalf("error param = %q, want %q", failure.Error.Param, openai.ParamImageURL)
	}
	if !strings.Contains(failure.Error.Message, "did not advertise image") {
		t.Fatalf("error message = %q", failure.Error.Message)
	}
}

func TestRemoteImageURLIsRefused(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_IMAGES": "1"}, "")

	resp := post(t, srv, "", map[string]any{
		"model": "fake",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "look"},
				{"type": "image_url", "image_url": map[string]string{"url": "https://example.com/cat.png"}},
			},
		}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure.Error.Message, "data url") {
		t.Fatalf("error message = %q", failure.Error.Message)
	}
}

func TestNonImageDataURLIsRefused(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_IMAGES": "1"}, "")

	resp := post(t, srv, "", map[string]any{
		"model": "fake",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "look"},
				{"type": "image_url", "image_url": map[string]string{"url": "data:application/pdf;base64,AAAA"}},
			},
		}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestTextOnlyRequestIsUnaffectedByImageHandling(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_ECHO": "1"}, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "no image here"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(completion.Choices[0].Message.ContentString(), "no image here") {
		t.Fatalf("content = %q", completion.Choices[0].Message.ContentString())
	}
}

func TestResponsesImageIsRefusedWithoutCapability(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model": "fake",
		"input": []map[string]any{{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": "look"},
				{"type": "input_image", "image_url": tinyPNG},
			},
		}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
