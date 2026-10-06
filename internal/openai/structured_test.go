package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseResponseFormat(t *testing.T) {
	t.Run("absent is text", func(t *testing.T) {
		format, err := ParseResponseFormat(nil)
		if err != nil || format.Active() {
			t.Fatalf("format = %+v, err = %v", format, err)
		}
	})

	t.Run("json object", func(t *testing.T) {
		format, err := ParseResponseFormat(json.RawMessage(`{"type":"json_object"}`))
		if err != nil || format.Kind != FormatJSONObject || !format.Active() {
			t.Fatalf("format = %+v, err = %v", format, err)
		}
	})

	t.Run("json schema", func(t *testing.T) {
		format, err := ParseResponseFormat(json.RawMessage(
			`{"type":"json_schema","json_schema":{"name":"weather","schema":{"type":"object"}}}`))
		if err != nil || format.Kind != FormatJSONSchema || format.Name != "weather" {
			t.Fatalf("format = %+v, err = %v", format, err)
		}
		if !strings.Contains(format.Instruction(), "weather") {
			t.Fatalf("instruction does not name the schema: %q", format.Instruction())
		}
	})

	t.Run("rejects nonsense", func(t *testing.T) {
		for _, raw := range []string{
			`{"type":"yaml"}`,
			`{"type":"json_schema"}`,
			`not json`,
		} {
			if _, err := ParseResponseFormat(json.RawMessage(raw)); err == nil {
				t.Fatalf("ParseResponseFormat(%s) succeeded, want an error", raw)
			}
		}
	})
}

func TestResponseFormatValidate(t *testing.T) {
	format := ResponseFormat{Kind: FormatJSONObject}

	t.Run("plain object", func(t *testing.T) {
		raw, err := format.Validate(`{"answer":42}`)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"answer":42}` {
			t.Fatalf("raw = %s", raw)
		}
	})

	t.Run("fenced", func(t *testing.T) {
		if _, err := format.Validate("```json\n{\"a\":1}\n```"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("wrapped in prose", func(t *testing.T) {
		if _, err := format.Validate("Sure!\n{\"a\":1}\nHope that helps."); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects prose", func(t *testing.T) {
		if _, err := format.Validate("I cannot do that."); err == nil {
			t.Fatal("expected prose to be rejected")
		}
	})

	t.Run("rejects empty", func(t *testing.T) {
		if _, err := format.Validate(""); err == nil {
			t.Fatal("expected an empty reply to be rejected")
		}
	})
}

func TestResponseFormatValidateAgainstSchema(t *testing.T) {
	format := ResponseFormat{
		Kind: FormatJSONSchema,
		Name: "weather",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {"city": {"type": "string"}, "temp": {"type": "integer"}},
			"required": ["city"],
			"additionalProperties": false
		}`),
	}

	t.Run("accepts a matching value", func(t *testing.T) {
		if _, err := format.Validate(`{"city":"Paris","temp":18}`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects a missing required property", func(t *testing.T) {
		_, err := format.Validate(`{"temp":18}`)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rejects a wrong type", func(t *testing.T) {
		_, err := format.Validate(`{"city":42}`)
		if err == nil || !strings.Contains(err.Error(), "expected a string") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rejects an unexpected property", func(t *testing.T) {
		_, err := format.Validate(`{"city":"Paris","extra":true}`)
		if err == nil || !strings.Contains(err.Error(), "unexpected property") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rejects a non-integer", func(t *testing.T) {
		_, err := format.Validate(`{"city":"Paris","temp":1.5}`)
		if err == nil || !strings.Contains(err.Error(), "integer") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestValidateSchema(t *testing.T) {
	t.Run("enum", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"string","enum":["a","b"]}`)
		if err := ValidateSchema("a", schema); err != nil {
			t.Fatal(err)
		}
		if err := ValidateSchema("c", schema); err == nil {
			t.Fatal("expected an out-of-enum value to be rejected")
		}
	})

	t.Run("array items", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)
		if err := ValidateSchema([]any{float64(1), float64(2)}, schema); err != nil {
			t.Fatal(err)
		}
		if err := ValidateSchema([]any{float64(1), "two"}, schema); err == nil {
			t.Fatal("expected a bad item to be rejected")
		}
	})

	t.Run("no schema is no constraint", func(t *testing.T) {
		if err := ValidateSchema(map[string]any{"anything": true}, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unknown keywords are ignored", func(t *testing.T) {
		// The subset is documented as partial: unsupported keywords must not
		// silently fail a value that is otherwise fine.
		schema := json.RawMessage(`{"type":"object","minProperties":5}`)
		if err := ValidateSchema(map[string]any{}, schema); err != nil {
			t.Fatalf("an unsupported keyword must not reject: %v", err)
		}
	})
}

func TestCorrectionMentionsTheReason(t *testing.T) {
	format := ResponseFormat{Kind: FormatJSONObject}
	got := format.Correction("the reply is not valid JSON")
	if !strings.Contains(got, "the reply is not valid JSON") {
		t.Fatalf("correction = %q", got)
	}
}
