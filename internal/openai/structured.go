package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Response format kinds.
const (
	FormatText       = "text"
	FormatJSONObject = "json_object"
	FormatJSONSchema = "json_schema"
)

// ResponseFormat is a decoded `response_format`.
type ResponseFormat struct {
	Kind   string
	Name   string
	Schema json.RawMessage
}

// Active reports whether the format constrains the output.
func (f ResponseFormat) Active() bool {
	return f.Kind == FormatJSONObject || f.Kind == FormatJSONSchema
}

// ParseResponseFormat decodes the OpenAI response_format field.
func ParseResponseFormat(raw json.RawMessage) (ResponseFormat, error) {
	if len(raw) == 0 {
		return ResponseFormat{Kind: FormatText}, nil
	}

	var envelope struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ResponseFormat{}, fmt.Errorf("response_format is malformed: %w", err)
	}

	switch envelope.Type {
	case FormatText:
		return ResponseFormat{Kind: FormatText}, nil
	case FormatJSONObject:
		return ResponseFormat{Kind: FormatJSONObject}, nil
	case FormatJSONSchema:
		if len(envelope.JSONSchema.Schema) == 0 {
			return ResponseFormat{}, fmt.Errorf("response_format.json_schema.schema is required")
		}
		name := envelope.JSONSchema.Name
		if name == "" {
			name = "response"
		}
		return ResponseFormat{Kind: FormatJSONSchema, Name: name, Schema: envelope.JSONSchema.Schema}, nil
	default:
		return ResponseFormat{}, fmt.Errorf("unsupported response_format type %q", envelope.Type)
	}
}

// Instruction is prepended to the prompt so the agent knows the shape required.
//
// This is prompt engineering, like the tool contract: ACP has no schema
// negotiation, so compliance is best-effort and the gateway validates what
// comes back rather than trusting it.
func (f ResponseFormat) Instruction() string {
	switch f.Kind {
	case FormatJSONObject:
		return "[system — output format]\n" +
			"Reply with a single valid JSON object and nothing else: no prose, no markdown fences.\n"
	case FormatJSONSchema:
		return "[system — output format]\n" +
			"Reply with a single valid JSON value and nothing else: no prose, no markdown fences.\n" +
			"It must satisfy this JSON Schema, named " + f.Name + ":\n" + string(f.Schema) + "\n"
	default:
		return ""
	}
}

// Correction is the instruction for the single retry after an invalid reply.
func (f ResponseFormat) Correction(reason string) string {
	return "[system — output format]\n" +
		"Your previous reply was rejected: " + reason + ".\n" +
		"Reply again with ONLY the JSON value, with no prose and no markdown fences.\n" +
		f.schemaHint()
}

// schemaHint repeats the schema, if there is one.
func (f ResponseFormat) schemaHint() string {
	if f.Kind != FormatJSONSchema {
		return ""
	}
	return "It must satisfy this JSON Schema:\n" + string(f.Schema) + "\n"
}

// Validate checks that text is the JSON the format requires and returns the
// canonical JSON.
func (f ResponseFormat) Validate(text string) (json.RawMessage, error) {
	if !f.Active() {
		return nil, nil
	}

	raw, err := extractJSON(text)
	if err != nil {
		return nil, err
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("the reply is not valid JSON: %w", err)
	}
	if f.Kind == FormatJSONSchema {
		if err := ValidateSchema(value, f.Schema); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// extractJSON finds the JSON value inside a reply, tolerating a markdown fence
// and surrounding prose.
func extractJSON(text string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(stripCodeFences(text))
	if trimmed == "" {
		return nil, fmt.Errorf("the reply was empty")
	}

	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed), nil
	}

	// Fall back to the outermost bracketed span.
	start := strings.IndexAny(trimmed, "{[")
	if start < 0 {
		return nil, fmt.Errorf("the reply contains no JSON")
	}
	end := strings.LastIndexAny(trimmed, "}]")
	if end <= start {
		return nil, fmt.Errorf("the reply contains no complete JSON value")
	}
	span := trimmed[start : end+1]
	if !json.Valid([]byte(span)) {
		return nil, fmt.Errorf("the reply contains no valid JSON value")
	}
	return json.RawMessage(span), nil
}
