# acp2api

An OpenAI-compatible HTTP gateway for **Agent Client Protocol (ACP)** agents.

Clients speak the ordinary OpenAI API. The gateway spawns an ACP agent CLI
(`devin acp`, `agent acp`, `claude-agent-acp`, `codex-acp`, `opencode acp`, …)
as a subprocess and speaks ACP JSON-RPC over stdio to it.

```
OpenAI client ──HTTP/SSE──▶ acp2api ──JSON-RPC over stdio──▶ ACP agent CLI
                               │                                    │
                               └◀── fs/*, terminal/*, permission ───┘
```

ACP connects *tools to tools* (editor ↔ agent). Most web applications, scripts,
and SDKs speak HTTP and expect an OpenAI-shaped API. This is the converter.

## Running it

The dev commands live in `lota.yml`:

```sh
lota agents       # which agent CLIs are installed on this machine
lota dev          # run the gateway with the dev token, verbose
lota smoke        # build, start, curl the API, stop
lota conformance  # assert the whole HTTP surface against a live server
lota check        # format, vet, race tests
```

`lota dev` takes flags:

```sh
lota dev -w ~/code/some-repo          # workspace the agents operate in
lota dev --addr 127.0.0.1:9000
lota dev -p deny                      # reject every permission request
```

Without `lota`, the binary is ordinary:

```sh
go build -o bin/acp2api ./cmd/acp2api
ACP2API_TOKEN=dev-token ./bin/acp2api --workspace ~/code/some-repo --verbose
```

Flags: `--config`, `--addr`, `--workspace`, `--permission`, `--verbose`,
`--version`. Environment: `ACP2API_ADDR`, `ACP2API_TOKEN`, `ACP2API_WORKSPACE`,
`ACP2API_PERMISSION`.

### Quick start

```sh
go build -o bin/acp2api ./cmd/acp2api

export ACP2API_TOKEN=change-me
./bin/acp2api --workspace /path/to/your/repo
```

```sh
curl -s localhost:8720/v1/models -H "Authorization: Bearer $ACP2API_TOKEN"

curl -s localhost:8720/v1/chat/completions \
  -H "Authorization: Bearer $ACP2API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "devin",
    "conversation_id": "my-session",
    "messages": [{"role": "user", "content": "Summarise the build setup."}]
  }'
```

Any OpenAI SDK works — point `base_url` at the gateway and pass the token as the
API key.

## Endpoints

### Served

| Method | Path | Purpose |
| ------ | ---- | ------- |
| `GET` | `/healthz` | Liveness. Never requires a token. |
| `GET` | `/v1/models` | The configured agents, as OpenAI models. |
| `GET` | `/v1/models/{id}` | One agent. |
| `POST` | `/v1/chat/completions` | A turn. Streaming and non-streaming. |
| `POST` | `/v1/completions` | The legacy text surface. |
| `POST` | `/v1/responses` | The Responses API. Streaming and non-streaming. |
| `GET` | `/v1/responses/{id}` | Retrieve a stored response. |
| `DELETE` | `/v1/responses/{id}` | Delete a stored response. |

### Refused with `501`

These have no ACP equivalent. An ACP agent is a stateful coding agent: it
produces text and edits files. It cannot embed, transcribe, classify, or
generate an image, so the gateway refuses rather than fabricating a result.

| Path | Why |
| ---- | --- |
| `POST /v1/embeddings` | an ACP agent produces text, not embedding vectors |
| `POST /v1/moderations` | there is no classifier behind an agent |
| `POST /v1/images/generations` | an ACP agent does not generate images |
| `POST /v1/images/edits` | an ACP agent does not edit images |
| `POST /v1/images/variations` | an ACP agent does not generate image variations |
| `POST /v1/audio/speech` | an ACP agent produces text, not audio |
| `POST /v1/audio/transcriptions` | an ACP agent does not transcribe audio |
| `POST /v1/audio/translations` | an ACP agent does not translate audio |
| `GET /v1/files`, `POST /v1/files` | the gateway keeps no file store |
| `GET /v1/files/{id}`, `DELETE /v1/files/{id}` | the gateway keeps no file store |
| `POST /v1/batches`, `GET /v1/batches` | the gateway runs no batch queue |
| `POST /v1/fine_tuning/jobs`, `GET /v1/fine_tuning/jobs` | an ACP agent is not a trainable model |
| `POST /v1/assistants`, `GET /v1/assistants` | superseded; use `POST /v1/responses` |
| `POST /v1/threads` | superseded; use `POST /v1/responses` |
| `POST /v1/vector_stores` | the gateway keeps no vector store |

The refusal body is the standard error envelope with
`code: "unsupported_endpoint"` and a message naming the reason. A test asserts
this table and the router agree.

## The `model` field

`model` is the agent selector:

- `devin` — run the Devin agent with its own default model.
- `devin/opus` — run Devin and select `opus` through the agent's model config
  option. Best-effort: an agent that advertises no model option runs its default.

## Conversations

An ACP session is stateful, which the OpenAI request shape does not express.

- **With `conversation_id`** the turn runs on a persistent session: the agent
  keeps the history, and only the newest user message is sent.
- **Without one** the session is ephemeral and the whole `messages` transcript
  is flattened into the prompt, because the agent starts from nothing.

`user` is accepted as a fallback conversation key for clients that cannot set a
custom field.

## Parameter policy

No parameter is silently ignored. Every OpenAI parameter is either honoured,
explicitly rejected, or accepted and reported back to you.

| Disposition | Parameters | Behaviour |
| ----------- | ---------- | --------- |
| Supported | `model`, `messages`, `stream`, `stream_options`, `conversation_id`, `user`, `workspace`, `tools`, `tool_choice`, `parallel_tool_calls`, `n`, `prompt`, `echo`, `stop`, `max_tokens`, `max_completion_tokens`, `max_output_tokens`, `response_format`, `modalities: ["text"]` | Honoured. |
| Accepted and reported | `temperature`, `top_p`, `seed`, `presence_penalty`, `frequency_penalty`, `logit_bias`, `reasoning_effort`, `verbosity`, `service_tier`, `prediction`, `store`, `metadata` | The agent owns its own sampling and does not expose these controls, so they cannot be honoured — and you cannot detect that as an error. They are listed in `acp.ignored_params` and in the `X-Acp2api-Ignored-Params` header. `tools[].function.strict` is reported the same way. |
| Rejected | `functions`, `function_call`, `logprobs`, `top_logprobs`, `audio`, `web_search_options`, `suffix`, `best_of`, `modalities` containing anything but `text`, `n` above 8 | `400` with code `unsupported_parameter`, naming the offending field. Ignoring these would make the response violate your request. |

`n` is capped at 8 because every choice is a separate agent turn, so an
unbounded `n` would be an unbounded cost. Streaming is refused together with
`n > 1` and with an array prompt: the choices would interleave.

Rejections carry the reason and the stage that will implement the parameter:

```json
{
  "error": {
    "message": "parameter \"logprobs\" is not supported: an ACP agent does not expose token probabilities, and synthesising them would be fabrication",
    "type": "invalid_request_error",
    "code": "unsupported_parameter",
    "param": "logprobs"
  }
}
```

The policy table lives in `internal/openai/params.go` and is the single source
of truth.

## Tool calling

`tools` works the way an OpenAI client expects: the model answers with
`tool_calls` and `finish_reason: "tool_calls"`, you run the function, and you send
the result back as a `role: "tool"` message.

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": { "name": "get_weather", "arguments": "{\"city\":\"Paris\"}" }
      }]
    },
    "finish_reason": "tool_calls"
  }]
}
```

**How it works, and why you should know.** ACP has no notion of a
caller-defined function, so there is nothing to negotiate. The gateway puts the
contract in the prompt: it tells the agent it is answering through a host that
executes tools, lists the callable functions, and asks for a call as
`{"tool_calls":[…]}` in the message body. It then parses that envelope out of
the agent's text.

That makes the contract **best-effort**. It is prompt engineering, not a
protocol guarantee, so an agent may ignore it. The gateway fails open: if the
envelope never appears, the agent's text is returned as ordinary content and no
tool call is reported. A client should not assume a call will arrive.

While a reply could still be an envelope, the gateway holds it back so the JSON
never reaches you as prose. The hold is released as soon as the buffer provably
cannot be an envelope, so a JSON-shaped *answer* is delayed, never swallowed.

The agent keeps its own tools. This gateway lets it work in the workspace it was
given; caller tools are additional, not a replacement. That is the opposite of
`cli-agent-gateway`, whose host must never execute anything.

## The `acp` extension

An agent session, its permission decisions, and its tool activity have no OpenAI
equivalent. They travel in an additive `acp` object that clients may ignore:

```json
{
  "choices": [{ "message": { "role": "assistant", "content": "…" }, "finish_reason": "stop" }],
  "acp": {
    "agent": "devin",
    "session_id": "…",
    "conversation_id": "my-session",
    "stop_reason": "end_turn",
    "steps": [{ "type": "tool_call", "tool_call_id": "…", "title": "Run tests", "kind": "execute" }]
  }
}
```

`usage` is present but approximate: agents do not report token counts, so it is
estimated from text length.

## Configuration

JSON, loaded from `--config`, then overridden by environment variables, then by
flags. See `config.example.json`.

| Key | Default | Notes |
| --- | ------- | ----- |
| `addr` | `127.0.0.1:8720` | Listen address. Binding off-loopback requires a token. |
| `workspace` | process cwd | Default agent working directory. |
| `token` | — | Bearer token for `/v1/*`. |
| `permission` | `allow` | `allow` or `deny` for agent permission requests. |
| `request_timeout_seconds` | `120` | Bounds one ACP request. |
| `session_ttl_seconds` | `1800` | Idle sessions and agent processes are reaped. Negative disables. |
| `agents` | built-ins | Overrides by id, or new agents. |

Environment: `ACP2API_ADDR`, `ACP2API_TOKEN`, `ACP2API_WORKSPACE`,
`ACP2API_PERMISSION`.

## Agent authentication

Some agents refuse to open a session until the ACP `authenticate` method has
been called, even when the CLI itself is already logged in. The Devin CLI is one
of them: without the call, `session/new` fails with *"ACP host has not
authenticated"*.

The gateway performs the handshake in the right order —
`initialize` → `authenticate` → `session/new` — selecting the first auth method
the agent advertises. For a host that is already logged in (`devin auth login`,
`opencode auth login`, …) that is enough.

For a headless login with an API key, point the agent at the variable holding it:

```json
{
  "agents": [
    { "id": "devin", "command": "devin", "args": ["acp"], "api_key_env": "WINDSURF_API_KEY" }
  ]
}
```

The value is sent as `authenticate`'s `_meta.api_key`. If the variable is unset
the request fails with an error naming it, rather than a confusing session
error. `auth_method` overrides which advertised method is chosen.

## Images

Chat and Responses image parts are translated to ACP image content blocks:

```json
{"role": "user", "content": [
  {"type": "text", "text": "what is in this image?"},
  {"type": "image_url", "image_url": {"url": "data:image/png;base64,…"}}
]}
```

Two rules, both deliberate:

- **Only data URLs.** A remote URL would make the gateway fetch an arbitrary
  address on the agent's behalf — a request-forgery vector in a process that can
  already reach internal services. Inline the bytes instead.
- **Gated on capability.** The agent must advertise image prompt support during
  `initialize`. An agent that cannot read images is refused with a clear error
  rather than answering blind.

## Structured outputs

`response_format` is enforced, not just requested:

```json
{"response_format": {"type": "json_schema", "json_schema": {
  "name": "weather",
  "schema": {"type": "object", "required": ["city"],
             "properties": {"city": {"type": "string"}}}}}}
```

The shape is asked for in the prompt, then the reply is verified. A reply that
does not parse, or does not satisfy the schema, triggers **one retry** with a
correction; if that also fails the request returns `400`. A structured answer is
buffered rather than streamed, because streaming it and then reporting it
invalid would leave you with unusable text.

The schema check covers `type`, `properties`, `required`, `items`, `enum`, and
`additionalProperties: false`. It is a documented subset, not full JSON Schema:
claiming more would be exactly the kind of silent lie this project exists to
avoid. Unsupported keywords are ignored, never used to reject a value.

## `stop` and `max_tokens`

The agent owns its own generation, so these are enforced on the way out.
`max_tokens` reports `finish_reason: "length"`. `stop` holds back up to
`len(longest stop) - 1` characters while streaming, so a stop sequence that
straddles a chunk boundary cannot leak to you.

## Security

An agent with filesystem access is remote code execution with extra steps, so:

- **Filesystem jail.** Every `fs/read_text_file` and `fs/write_text_file` path is
  resolved and confined to the session workspace. Symlinks are resolved before
  the containment check, so a link cannot be used to escape.
- **Explicit permission policy.** `permission: allow` picks a one-shot allow
  option; `deny` picks a reject option or cancels. There is no "ask" — a headless
  gateway has nobody to ask.
- **Terminals are refused.** The gateway does not advertise the terminal
  capability, and answers `terminal/*` with a method-not-found error.
- **Loopback by default.** Binding elsewhere without a token is a configuration
  error, not a warning.

## Supported agents

Built in: `devin`, `cursor`, `claude`, `codex`, `opencode`, `kiro`. Add or
retarget any of them under `agents` in the config. An agent is spawned only when
a request selects it, so a missing CLI is a clear spawn error, not a startup
failure.

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Tests never touch a real agent: they run a scriptable fake ACP agent
(`internal/fakeagent`) inside the test binary.

Architecture, conventions, and file-size limits live in
`.devin/rules/global_rules.md`. The agent-facing brief is `AGENTS.md`.

## Prior art

`acp-to-api` and `acpbox` (Python) and `cli-agent-gateway` (Go) solve the same
problem. This project borrows their contracts — endpoint shapes, the `acp`
extension field — not their code.

## License

MIT
