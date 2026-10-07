package openai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// decode builds a request from raw JSON, the way the handler does, so presence
// detection is exercised end to end rather than through Go literals.
func decode(t *testing.T, body string) *ChatCompletionRequest {
	t.Helper()
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return &req
}

func TestUnsupportedParametersAreRejected(t *testing.T) {
	cases := map[string]string{
		"functions":          `{"functions":[{"name":"f"}]}`,
		"function_call":      `{"function_call":"auto"}`,
		"logprobs":           `{"logprobs":true}`,
		"top_logprobs":       `{"top_logprobs":5}`,
		"n":                  `{"n":9}`,
		"audio":              `{"audio":{"voice":"alloy","format":"wav"}}`,
		"web_search_options": `{"web_search_options":{}}`,
		"modalities":         `{"modalities":["text","audio"]}`,
	}

	for want, body := range cases {
		t.Run(want, func(t *testing.T) {
			req := decode(t, `{"model":"devin","messages":[{"role":"user","content":"hi"}],`+strings.TrimPrefix(body, "{"))

			ignored, err := ValidateRequest(req)
			if err == nil {
				t.Fatalf("expected %q to be rejected, ignored=%v", want, ignored)
			}
			if err.Param != want {
				t.Fatalf("rejected param = %q, want %q", err.Param, want)
			}
			if err.Reason == "" {
				t.Fatalf("%q was rejected without a reason", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error text %q does not name the parameter", err.Error())
			}
		})
	}
}

func TestIgnoredParametersAreReported(t *testing.T) {
	cases := []string{
		"temperature", "top_p", "seed", "presence_penalty", "frequency_penalty", "logit_bias",
		"verbosity", "service_tier", "prediction", "store", "metadata",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"model": "devin", "messages": []map[string]string{{"role": "user", "content": "hi"}}}
			switch name {
			case "logit_bias":
				body[name] = map[string]int{"123": 5}
			case "seed":
				body[name] = 7
			case "prediction", "metadata":
				body[name] = map[string]any{"type": "content", "content": "x"}
			case "store":
				body[name] = true
			case "verbosity", "service_tier":
				body[name] = "medium"
			default:
				body[name] = 0.5
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}

			ignored, paramErr := ValidateRequest(decode(t, string(raw)))
			if paramErr != nil {
				t.Fatalf("%q must be accepted, got %v", name, paramErr)
			}
			if len(ignored) != 1 || ignored[0] != name {
				t.Fatalf("ignored = %v, want [%s]", ignored, name)
			}
		})
	}
}

func TestStrictToolsAreReportedAsIgnored(t *testing.T) {
	body := `{
		"model":"devin",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"f","strict":true,"parameters":{"type":"object"}}}]
	}`

	ignored, err := ValidateRequest(decode(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0] != ParamToolStrict {
		t.Fatalf("ignored = %v, want [%s]", ignored, ParamToolStrict)
	}
}

func TestNonStrictToolsAreNotReported(t *testing.T) {
	body := `{
		"model":"devin",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"function","function":{"name":"a"}},
			{"type":"function","function":{"name":"b","strict":false}}
		]
	}`

	ignored, err := ValidateRequest(decode(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 {
		t.Fatalf("ignored = %v, want none", ignored)
	}
}

func TestParallelToolCallsAndTextModalitiesAreSupported(t *testing.T) {
	for _, body := range []string{
		`{"parallel_tool_calls":false}`,
		`{"parallel_tool_calls":true}`,
		`{"modalities":["text"]}`,
	} {
		req := decode(t, `{"model":"devin","messages":[{"role":"user","content":"hi"}],`+strings.TrimPrefix(body, "{"))
		ignored, err := ValidateRequest(req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(ignored) != 0 {
			t.Fatalf("%s: ignored = %v, want none", body, ignored)
		}
	}
}

func TestIgnoredListIsSorted(t *testing.T) {
	req := decode(t, `{
		"model":"devin",
		"messages":[{"role":"user","content":"hi"}],
		"top_p":0.9,
		"temperature":0.1,
		"seed":3
	}`)

	ignored, err := ValidateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"seed", "temperature", "top_p"}
	if !reflect.DeepEqual(ignored, want) {
		t.Fatalf("ignored = %v, want %v", ignored, want)
	}
}

func TestTemperatureZeroIsPresentNotAbsent(t *testing.T) {
	withZero := decode(t, `{"model":"devin","messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	ignored, err := ValidateRequest(withZero)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0] != "temperature" {
		t.Fatalf("temperature 0 must count as present, ignored = %v", ignored)
	}

	without := decode(t, `{"model":"devin","messages":[{"role":"user","content":"hi"}]}`)
	ignored, err = ValidateRequest(without)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 {
		t.Fatalf("an absent temperature must not be reported, ignored = %v", ignored)
	}
}

func TestNIsBounded(t *testing.T) {
	for _, tc := range []struct {
		body    string
		wantErr bool
	}{
		{body: `{"n":1}`},
		{body: `{"n":3}`},
		{body: `{"n":0}`},
		{body: `{"n":9}`, wantErr: true},
		{body: `{"n":100}`, wantErr: true},
	} {
		req := decode(t, `{"model":"devin","messages":[{"role":"user","content":"hi"}],`+strings.TrimPrefix(tc.body, "{"))
		_, err := ValidateRequest(req)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected rejection", tc.body)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: unexpected rejection %v", tc.body, err)
		}
	}
}

func TestSupportedRequestIsClean(t *testing.T) {
	req := decode(t, `{
		"model":"devin",
		"messages":[{"role":"user","content":"hi"}],
		"stream":true,
		"stream_options":{"include_usage":true},
		"conversation_id":"c1",
		"workspace":"/tmp"
	}`)

	ignored, err := ValidateRequest(req)
	if err != nil {
		t.Fatalf("a fully supported request must validate: %v", err)
	}
	if len(ignored) != 0 {
		t.Fatalf("ignored = %v, want none", ignored)
	}
}

// requestStructs lists every request type the shared policy applies to.
func requestStructs() []any {
	return []any{ChatCompletionRequest{}, CompletionRequest{}, ResponsesRequest{}}
}

// TestPolicyCoversEveryPolicedField guards against drift: a field added to a
// request struct without a rule would be silently ignored, which is exactly the
// failure this stage exists to remove.
func TestPolicyCoversEveryPolicedField(t *testing.T) {
	fields := map[string]bool{}
	for _, v := range requestStructs() {
		for name := range jsonFieldNames(v) {
			fields[name] = true
		}
	}
	for _, name := range PolicyNames() {
		if !fields[name] {
			t.Fatalf("policy names %q but no request struct has that field", name)
		}
	}
}

// TestPolicedFieldsArePointersOrContainers asserts the modelling rule the
// presence detection depends on: a value-dependent parameter must be a pointer,
// slice, map, or RawMessage, never a bare scalar.
func TestPolicedFieldsArePointersOrContainers(t *testing.T) {
	for _, name := range PolicyNames() {
		field, ok := fieldInAnyStruct(name)
		if !ok {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.String, reflect.Bool:
			// Fine: pointers and containers detect presence; string and bool are
			// already unambiguous.
		default:
			t.Fatalf("field %q is %s; a value-dependent parameter must be a pointer or container",
				name, field.Type.Kind())
		}
	}
}

// fieldInAnyStruct finds a policy field in whichever request type declares it.
func fieldInAnyStruct(name string) (reflect.StructField, bool) {
	for _, v := range requestStructs() {
		if field, ok := fieldByJSONName(reflect.TypeOf(v), name); ok {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// jsonFieldNames returns the JSON names of a struct's exported fields.
func jsonFieldNames(v any) map[string]bool {
	typ := reflect.TypeOf(v)
	names := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if name := strings.Split(field.Tag.Get("json"), ",")[0]; name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// fieldByJSONName finds a struct field by its JSON tag.
func fieldByJSONName(typ reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if strings.Split(field.Tag.Get("json"), ",")[0] == name {
			return field, true
		}
	}
	return reflect.StructField{}, false
}
