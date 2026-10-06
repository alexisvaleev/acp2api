package openai

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Error codes this package assigns. They are part of the public contract: a
// client switches on them.
const (
	CodeUnsupportedParameter = "unsupported_parameter"
	CodeUnsupportedEndpoint  = "unsupported_endpoint"
)

// Disposition says what the gateway does with a request parameter.
type Disposition int

const (
	// Supported means the gateway honours the parameter.
	Supported Disposition = iota
	// Ignored means the parameter is accepted and reported back to the caller.
	// It is used where ignoring it is not detectable as an error: the agent owns
	// its own sampling, so the caller cannot tell that temperature was dropped.
	Ignored
	// Unsupported means the parameter is rejected with an explicit error,
	// because ignoring it would make the response violate the request.
	Unsupported
)

// ParamRule is the disposition of one request parameter.
type ParamRule struct {
	// Name is the JSON field name.
	Name string
	// Disposition applies when Check is nil, or when Check defers to it.
	Disposition Disposition
	// Reason is shown to the caller for an Unsupported parameter.
	Reason string
	// Check refines the disposition for value-dependent parameters, such as `n`
	// where only values above one are unsupported. Nil means presence alone
	// decides.
	Check func(value any) (Disposition, string)
}

// ParamError reports a parameter the gateway refuses to ignore.
type ParamError struct {
	Param  string
	Reason string
}

// Error implements the error interface.
func (e *ParamError) Error() string {
	return fmt.Sprintf("parameter %q is not supported: %s", e.Param, e.Reason)
}

// Reasons that more than one rule shares, kept as constants so the table and
// the value-dependent checks cannot drift apart.
const (
	reasonSampling   = "the agent owns its own sampling"
	reasonSteering   = "the agent does not expose this control, and dropping it cannot change the shape of the response"
	reasonLogprobs   = "an ACP agent does not expose token probabilities, and synthesising them would be fabrication"
	reasonChoices    = "multiple choices are not yet supported; they arrive in stage 5"
	reasonLegacyFns  = "the legacy functions API is not translated to ACP; use tools instead"
	reasonStructured = "structured outputs are not yet enforced; they arrive in stage 4"
	reasonStop       = "stop sequences are not yet applied to agent output; they arrive in stage 4"
	reasonLength     = "output length is not yet capped; it arrives in stage 4"
	reasonAudio      = "an ACP agent produces text, not audio"
	reasonWebSearch  = "built-in server-side tools have no ACP equivalent; declare your own with tools"
	reasonModalities = "only text output is supported; an ACP agent cannot produce audio"
	reasonToolStrict = "strict schema enforcement is not applied; the schema is passed to the agent as a description"
)

// paramPolicy is the single source of truth for how every policed parameter is
// treated. A parameter absent from this table is dropped silently and logged by
// the caller: rejecting unknown fields outright would break forward
// compatibility with new OpenAI parameters, which is the worse trade.
var paramPolicy = map[string]ParamRule{
	/* Supported: honoured by the gateway. */
	"model":               {Name: "model", Disposition: Supported},
	"messages":            {Name: "messages", Disposition: Supported},
	"stream":              {Name: "stream", Disposition: Supported},
	"stream_options":      {Name: "stream_options", Disposition: Supported},
	"conversation_id":     {Name: "conversation_id", Disposition: Supported},
	"user":                {Name: "user", Disposition: Supported},
	"workspace":           {Name: "workspace", Disposition: Supported},
	"tools":               {Name: "tools", Disposition: Supported},
	"tool_choice":         {Name: "tool_choice", Disposition: Supported},
	"parallel_tool_calls": {Name: "parallel_tool_calls", Disposition: Supported},

	/* Accepted and reported: the agent owns its own sampling. */
	"temperature":       {Name: "temperature", Disposition: Ignored, Reason: reasonSampling},
	"top_p":             {Name: "top_p", Disposition: Ignored, Reason: reasonSampling},
	"seed":              {Name: "seed", Disposition: Ignored, Reason: reasonSampling},
	"presence_penalty":  {Name: "presence_penalty", Disposition: Ignored, Reason: reasonSampling},
	"frequency_penalty": {Name: "frequency_penalty", Disposition: Ignored, Reason: reasonSampling},
	"logit_bias":        {Name: "logit_bias", Disposition: Ignored, Reason: reasonSampling},

	/* Accepted and reported: they steer the agent but cannot change the shape
	   of the response, so a caller cannot detect that they were dropped. */
	"reasoning_effort": {Name: "reasoning_effort", Disposition: Ignored, Reason: reasonSteering},
	"verbosity":        {Name: "verbosity", Disposition: Ignored, Reason: reasonSteering},
	"service_tier":     {Name: "service_tier", Disposition: Ignored, Reason: reasonSteering},
	"prediction":       {Name: "prediction", Disposition: Ignored, Reason: reasonSteering},
	"store":            {Name: "store", Disposition: Ignored, Reason: reasonSteering},
	"metadata":         {Name: "metadata", Disposition: Ignored, Reason: reasonSteering},

	/* Unsupported: ignoring these would make the response violate the request. */
	"functions":             {Name: "functions", Disposition: Unsupported, Reason: reasonLegacyFns},
	"function_call":         {Name: "function_call", Disposition: Unsupported, Reason: reasonLegacyFns},
	"response_format":       {Name: "response_format", Disposition: Unsupported, Reason: reasonStructured},
	"stop":                  {Name: "stop", Disposition: Unsupported, Reason: reasonStop},
	"max_tokens":            {Name: "max_tokens", Disposition: Unsupported, Reason: reasonLength},
	"max_completion_tokens": {Name: "max_completion_tokens", Disposition: Unsupported, Reason: reasonLength},
	"logprobs":              {Name: "logprobs", Disposition: Unsupported, Reason: reasonLogprobs},
	"top_logprobs":          {Name: "top_logprobs", Disposition: Unsupported, Reason: reasonLogprobs},
	"audio":                 {Name: "audio", Disposition: Unsupported, Reason: reasonAudio},
	"web_search_options":    {Name: "web_search_options", Disposition: Unsupported, Reason: reasonWebSearch},
	"n": {
		Name: "n", Disposition: Unsupported, Reason: reasonChoices, Check: checkN,
	},
	"modalities": {
		Name: "modalities", Disposition: Supported, Check: checkModalities,
	},
}

// checkModalities accepts text-only output and rejects a request for anything
// an agent cannot produce.
func checkModalities(value any) (Disposition, string) {
	modalities, ok := value.([]string)
	if !ok {
		return Unsupported, reasonModalities
	}
	for _, modality := range modalities {
		if modality != "text" {
			return Unsupported, reasonModalities
		}
	}
	return Supported, ""
}

// checkN allows the default single choice and rejects a request for more.
func checkN(value any) (Disposition, string) {
	if n, ok := value.(*int); ok && n != nil && *n <= 1 {
		return Supported, ""
	}
	return Unsupported, reasonChoices
}

// ParamToolStrict names the nested setting reported when a caller asks for
// strict schema enforcement on a tool. It is not a top-level parameter, so it
// is handled separately from the policy table.
const ParamToolStrict = "tools[].function.strict"

// ValidateRequest applies the parameter policy to a decoded request.
//
// It returns the parameters that were accepted but not honoured, sorted for
// stable output, and the first parameter the gateway refuses to ignore.
func ValidateRequest(req *ChatCompletionRequest) (ignored []string, bad *ParamError) {
	walkPresent(req, func(name string, value any) {
		rule, ok := paramPolicy[name]
		if !ok {
			return
		}
		disposition, reason := rule.Disposition, rule.Reason
		if rule.Check != nil {
			disposition, reason = rule.Check(value)
		}
		switch disposition {
		case Ignored:
			ignored = append(ignored, name)
		case Unsupported:
			if bad == nil {
				bad = &ParamError{Param: name, Reason: reason}
			}
		}
	})

	// A tool may ask for strict schema enforcement. The gateway passes the
	// schema to the agent as a description and does not validate the arguments
	// against it, so the caller has to be told.
	if requestsStrictTools(req.Tools) {
		ignored = append(ignored, ParamToolStrict)
	}

	sort.Strings(ignored)
	return ignored, bad
}

// requestsStrictTools reports whether any declared tool asked for strict
// schema enforcement.
func requestsStrictTools(tools []Tool) bool {
	for _, tool := range tools {
		if tool.Function.Strict != nil && *tool.Function.Strict {
			return true
		}
	}
	return false
}

// PolicyNames returns every parameter the policy covers, sorted. Tests use it
// to assert the table stays in step with the request struct.
func PolicyNames() []string {
	names := make([]string, 0, len(paramPolicy))
	for name := range paramPolicy {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// walkPresent calls fn for each JSON field of v that carries a value. A field
// counts as present when it is a non-empty pointer, slice, or map, or a non-zero
// scalar — which is why value-dependent parameters are modelled as pointers.
func walkPresent(v any, fn func(name string, value any)) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		value := rv.Field(i)
		if !isPresent(value) {
			continue
		}
		fn(name, value.Interface())
	}
}

// isPresent reports whether a field carries a value the caller supplied.
func isPresent(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.Len() > 0
	case reflect.String:
		return v.String() != ""
	case reflect.Bool:
		return v.Bool()
	default:
		return !v.IsZero()
	}
}
