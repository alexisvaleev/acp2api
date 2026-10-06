package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ImagePart is one image extracted from a message.
type ImagePart struct {
	Data     string
	MimeType string
}

// contentPart is the union of the part shapes the chat and Responses APIs use.
type contentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
	// The Responses API also allows a bare `url` on some part types.
	URL string `json:"url"`
}

// imageURLString reads the image_url field in either shape: an object with a
// `url`, as chat completions uses, or a bare string, as the Responses API does.
func (p contentPart) imageURLString() string {
	if len(p.ImageURL) == 0 {
		return p.URL
	}
	var bare string
	if err := json.Unmarshal(p.ImageURL, &bare); err == nil {
		return bare
	}
	var object struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(p.ImageURL, &object); err == nil {
		return object.URL
	}
	return ""
}

// ImageParts extracts the image content from a transcript.
//
// Only data URLs are accepted. A remote URL would make the gateway fetch an
// arbitrary address on the agent's behalf, which is a server-side request
// forgery vector in a process that can already reach internal services; the
// caller is told to inline the bytes instead.
func ImageParts(messages []Message) ([]ImagePart, error) {
	var out []ImagePart
	for _, message := range messages {
		parts, err := decodeContentParts(message.Content)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if part.Type != "image_url" && part.Type != "input_image" {
				continue
			}
			image, err := parseImageURL(part.imageURLString())
			if err != nil {
				return nil, err
			}
			out = append(out, image)
		}
	}
	return out, nil
}

// decodeContentParts decodes an array-form message content. A string content
// has no parts.
func decodeContentParts(content json.RawMessage) ([]contentPart, error) {
	if len(content) == 0 {
		return nil, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		// A plain string is the common case and carries no images.
		return nil, nil
	}
	return parts, nil
}

// parseImageURL decodes a data URL into its payload and media type.
func parseImageURL(url string) (ImagePart, error) {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return ImagePart{}, fmt.Errorf("an image part carries no url")
	}
	if !strings.HasPrefix(trimmed, "data:") {
		return ImagePart{}, fmt.Errorf(
			"remote image urls are not fetched by the gateway; inline the image as a data url instead")
	}

	rest := strings.TrimPrefix(trimmed, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return ImagePart{}, fmt.Errorf("malformed data url: no comma separating header and payload")
	}

	header, payload := rest[:comma], rest[comma+1:]
	if !strings.Contains(header, "base64") {
		return ImagePart{}, fmt.Errorf("malformed data url: only base64 payloads are supported")
	}

	mimeType := strings.TrimSuffix(header, ";base64")
	mimeType = strings.TrimSuffix(mimeType, ";")
	if mimeType == "" {
		return ImagePart{}, fmt.Errorf("malformed data url: no media type")
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return ImagePart{}, fmt.Errorf("unsupported media type %q: only images can be sent to an agent", mimeType)
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return ImagePart{}, fmt.Errorf("malformed data url: payload is not base64: %w", err)
	}

	return ImagePart{Data: payload, MimeType: mimeType}, nil
}
