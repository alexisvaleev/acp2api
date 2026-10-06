package openai

import (
	"encoding/json"
	"fmt"
	"math"
)

// ValidateSchema checks a decoded JSON value against a subset of JSON Schema.
//
// The subset is deliberately partial: `type`, `properties`, `required`, `items`,
// `enum`, and `additionalProperties: false`. A complete JSON Schema validator is
// a project of its own, and claiming full conformance while checking less would
// be exactly the kind of silent lie this gateway is built to avoid. Keywords
// outside the subset are ignored, and the README says so.
func ValidateSchema(value any, schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var node schemaNode
	if err := json.Unmarshal(schema, &node); err != nil {
		return fmt.Errorf("schema is not valid JSON: %w", err)
	}
	return node.validate(value, "")
}

// schemaNode is one node of the supported schema subset.
type schemaNode struct {
	Type                 string                `json:"type"`
	Properties           map[string]schemaNode `json:"properties"`
	Required             []string              `json:"required"`
	Items                *schemaNode           `json:"items"`
	Enum                 []any                 `json:"enum"`
	AdditionalProperties json.RawMessage       `json:"additionalProperties"`
}

// validate checks value against this node.
func (s schemaNode) validate(value any, path string) error {
	if len(s.Enum) > 0 && !enumContains(s.Enum, value) {
		return fmt.Errorf("%s: value is not one of the allowed enum values", describe(path))
	}

	switch s.Type {
	case "":
		// No type constraint.
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object", describe(path))
		}
		for _, name := range s.Required {
			if _, present := object[name]; !present {
				return fmt.Errorf("%s: missing required property %q", describe(path), name)
			}
		}
		for name, sub := range s.Properties {
			if child, present := object[name]; present {
				if err := sub.validate(child, join(path, name)); err != nil {
					return err
				}
			}
		}
		if isFalse(s.AdditionalProperties) {
			for name := range object {
				if _, known := s.Properties[name]; !known {
					return fmt.Errorf("%s: unexpected property %q", describe(path), name)
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected an array", describe(path))
		}
		if s.Items != nil {
			for i, item := range items {
				if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected a string", describe(path))
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: expected a number", describe(path))
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != math.Trunc(number) {
			return fmt.Errorf("%s: expected an integer", describe(path))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected a boolean", describe(path))
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s: expected null", describe(path))
		}
	default:
		// An unrecognised type keyword is not enforced.
	}
	return nil
}

// enumContains reports whether value equals one of the enum entries.
func enumContains(enum []any, value any) bool {
	for _, candidate := range enum {
		if fmt.Sprint(candidate) == fmt.Sprint(value) {
			return true
		}
	}
	return false
}

// isFalse reports whether a raw JSON value is the literal false.
func isFalse(raw json.RawMessage) bool {
	return string(raw) == "false"
}

// describe renders a validation path for an error message.
func describe(path string) string {
	if path == "" {
		return "value"
	}
	return path
}

// join extends a validation path.
func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
