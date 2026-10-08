package openai

import (
	"encoding/json"
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
	reasonChoices    = "n above 8 is refused; every choice is a separate agent turn, so the cost is unbounded"
	reasonLegacyFns  = "the legacy functions API is not translated to ACP; use tools instead"
	reasonAudio      = "an ACP agent produces text, not audio"
	reasonWebSearch  = "built-in server-side tools have no ACP equivalent; declare your own with tools"
	reasonModalities = "only text output is supported; an ACP agent cannot produce audio"
	reasonToolStrict = "strict schema enforcement is not applied; the schema is passed to the agent as a description"
	reasonSuffix     = "a completion suffix cannot be produced by an agent that answers rather than continues text"
	reasonBestOf     = "best_of would require sampling candidates the agent does not expose; use n instead"
	reasonReasonSum  = "an ACP agent's reasoning stream is forwarded as reasoning_content; it cannot be summarised on demand"
	reasonReasonCaps = "an ACP agent's reasoning is not billed or bounded the way this field assumes, so the limit would be ignored"
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

	/* Supported: honoured by post-processing the agent's output, since the
	   agent owns its own generation and cannot be told to stop. */
	"prompt":                {Name: "prompt", Disposition: Supported},
	"echo":                  {Name: "echo", Disposition: Supported},
	"stop":                  {Name: "stop", Disposition: Supported},
	"max_tokens":            {Name: "max_tokens", Disposition: Supported},
	"max_completion_tokens": {Name: "max_completion_tokens", Disposition: Supported},
	"max_output_tokens":     {Name: "max_output_tokens", Disposition: Supported},
	"response_format":       {Name: "response_format", Disposition: Supported},
	/* Accepted and reported: the agent owns its own sampling. */
	"temperature":       {Name: "temperature", Disposition: Ignored, Reason: reasonSampling},
	"top_p":             {Name: "top_p", Disposition: Ignored, Reason: reasonSampling},
	"seed":              {Name: "seed", Disposition: Ignored, Reason: reasonSampling},
	"presence_penalty":  {Name: "presence_penalty", Disposition: Ignored, Reason: reasonSampling},
	"frequency_penalty": {Name: "frequency_penalty", Disposition: Ignored, Reason: reasonSampling},
	"logit_bias":        {Name: "logit_bias", Disposition: Ignored, Reason: reasonSampling},

	/* Honoured: the effort level selects the agent's model variant —
	   "devin/swe-2" + effort "max" resolves to "swe-2-max" in the agent's
	   advertised catalog. reasoning_effort is chat's flat form; reasoning is
	   the Responses object, whose only understood key today is effort, so
	   checkReasoning refuses the keys it cannot honour rather than letting
	   them vanish. */
	"reasoning_effort": {Name: "reasoning_effort", Disposition: Supported},
	"reasoning": {
		Name: "reasoning", Disposition: Supported, Check: checkReasoning,
	},

	/* Accepted and reported: they steer the agent but cannot change the shape
	   of the response, so a caller cannot detect that they were dropped. */
	"verbosity":    {Name: "verbosity", Disposition: Ignored, Reason: reasonSteering},
	"service_tier": {Name: "service_tier", Disposition: Ignored, Reason: reasonSteering},
	"prediction":   {Name: "prediction", Disposition: Ignored, Reason: reasonSteering},
	"store":        {Name: "store", Disposition: Ignored, Reason: reasonSteering},
	"metadata":     {Name: "metadata", Disposition: Ignored, Reason: reasonSteering},

	/* Unsupported: ignoring these would make the response violate the request. */
	"functions":          {Name: "functions", Disposition: Unsupported, Reason: reasonLegacyFns},
	"function_call":      {Name: "function_call", Disposition: Unsupported, Reason: reasonLegacyFns},
	"logprobs":           {Name: "logprobs", Disposition: Unsupported, Reason: reasonLogprobs},
	"top_logprobs":       {Name: "top_logprobs", Disposition: Unsupported, Reason: reasonLogprobs},
	"audio":              {Name: "audio", Disposition: Unsupported, Reason: reasonAudio},
	"web_search_options": {Name: "web_search_options", Disposition: Unsupported, Reason: reasonWebSearch},
	"suffix":             {Name: "suffix", Disposition: Unsupported, Reason: reasonSuffix},
	"best_of":            {Name: "best_of", Disposition: Unsupported, Reason: reasonBestOf},
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

// checkReasoning polices the Responses reasoning object. Only `effort` maps
// onto ACP — it picks the agent's model variant — so the other keys are
// refused rather than dropped. A request that names a summary or a reasoning
// token budget is asking for something this gateway cannot produce, and a
// caller cannot tell the difference unless it is told.
//
// The value is a json.RawMessage because the object has to be readable when it
// holds keys no struct field models: a typed struct would hide them from the
// policy entirely, which is the failure this check exists to prevent.
func checkReasoning(value any) (Disposition, string) {
	keys, ok := reasoningKeys(value)
	if !ok {
		// A caller that sent something other than an object named nothing we
		// could honour, but there is also nothing to report as ignored.
		return Unsupported, "reasoning must be an object"
	}

	effort := false
	for key := range keys {
		switch key {
		case "effort":
			effort = true
		case "summary", "generate_summary":
			return Unsupported, reasonReasonSum
		case "max_tokens":
			return Unsupported, reasonReasonCaps
		}
	}
	if !effort && len(keys) > 0 {
		// Unrecognised keys only: the object carries no effort to honour and no
		// known control to refuse, so the honest answer is that it was dropped.
		return Ignored, reasonSteering
	}
	return Supported, ""
}

// reasoningKeys reads the key set of a reasoning value. It accepts both the raw
// message a request decodes into and an already-decoded map, so the check can
// be exercised without going through JSON.
func reasoningKeys(value any) (map[string]bool, bool) {
	var body map[string]json.RawMessage
	switch v := value.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return nil, false
		}
		if err := json.Unmarshal(v, &body); err != nil {
			return nil, false
		}
	case map[string]json.RawMessage:
		body = v
	case map[string]any:
		keys := make(map[string]bool, len(v))
		for key := range v {
			keys[key] = true
		}
		return keys, true
	default:
		return nil, false
	}
	keys := make(map[string]bool, len(body))
	for key := range body {
		keys[key] = true
	}
	return keys, true
}

// ReasoningEffort reads the effort level out of a reasoning object, or the
// empty string when it names none. It is the one key the gateway honours, so
// the reader lives beside the check that guarantees the rest are refused.
func ReasoningEffort(value any) string {
	var body map[string]json.RawMessage
	switch v := value.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return ""
		}
		if err := json.Unmarshal(v, &body); err != nil {
			return ""
		}
	case map[string]json.RawMessage:
		body = v
	default:
		return ""
	}
	raw, ok := body["effort"]
	if !ok {
		return ""
	}
	var effort string
	if err := json.Unmarshal(raw, &effort); err != nil {
		return ""
	}
	return effort
}

// EffortLevels are the effort words a model catalog spells its variants with,
// sorted for a stable error message. ReasoningEffortIsKnown is the validation
// the handler runs before an agent is spawned: a level no catalog can carry is
// a request we can refuse for free.
var EffortLevels = []string{"high", "low", "max", "medium", "minimal", "none", "xhigh"}

// ReasoningEffortIsKnown reports whether an effort names a level a model
// catalog can carry. An empty effort means "not given" and is accepted.
func ReasoningEffortIsKnown(effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return true
	}
	for _, level := range EffortLevels {
		if effort == level {
			return true
		}
	}
	return false
}

// MaxChoices caps how many completions one request may ask for. Every choice is
// a separate agent turn, so an unbounded n would be an unbounded cost.
const MaxChoices = 8

// checkN accepts a single choice and a bounded number of extra ones.
func checkN(value any) (Disposition, string) {
	n, ok := value.(*int)
	if !ok || n == nil || *n <= MaxChoices {
		return Supported, ""
	}
	return Unsupported, reasonChoices
}

// ParamToolStrict names the nested setting reported when a caller asks for
// strict schema enforcement on a tool. It is not a top-level parameter, so it
// is handled separately from the policy table.
const ParamToolStrict = "tools[].function.strict"

// ParamImageURL names the nested setting reported for an image part that cannot
// be sent.
const ParamImageURL = "messages[].content[].image_url"

// ValidateParams applies the parameter policy to any decoded request struct.
//
// It returns the parameters that were accepted but not honoured, sorted for
// stable output, and the first parameter the gateway refuses to ignore.
func ValidateParams(v any) (ignored []string, bad *ParamError) {
	walkPresent(v, func(name string, value any) {
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
	sort.Strings(ignored)
	return ignored, bad
}

// ValidateRequest applies the parameter policy to a chat request, plus the
// checks that only make sense with tools.
func ValidateRequest(req *ChatCompletionRequest) (ignored []string, bad *ParamError) {
	ignored, bad = ValidateParams(req)

	// A tool may ask for strict schema enforcement. The gateway passes the
	// schema to the agent as a description and does not validate the arguments
	// against it, so the caller has to be told.
	if requestsStrictTools(req.Tools) {
		ignored = append(ignored, ParamToolStrict)
		sort.Strings(ignored)
	}
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
