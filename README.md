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
lota agents      # which agent CLIs are installed on this machine
lota dev         # run the gateway with the race-free dev token, verbose
lota smoke       # build, start, curl the API, stop
lota check       # format, vet, race tests
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

| Method | Path | Purpose |
| ------ | ---- | ------- |
| `GET` | `/healthz` | Liveness. Never requires a token. |
| `GET` | `/v1/models` | The configured agents, as OpenAI models. |
| `POST` | `/v1/chat/completions` | A turn. Streaming and non-streaming. |

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
| Supported | `model`, `messages`, `stream`, `stream_options`, `conversation_id`, `user`, `workspace`, `tools`, `tool_choice` | Honoured. |
| Accepted and reported | `temperature`, `top_p`, `seed`, `presence_penalty`, `frequency_penalty`, `logit_bias` | The agent owns its own sampling, so these cannot be honoured — and you cannot detect that as an error. They are listed in `acp.ignored_params` and in the `X-Acp2api-Ignored-Params` header. |
| Rejected | `functions`, `function_call`, `response_format`, `stop`, `max_tokens`, `max_completion_tokens`, `logprobs`, `top_logprobs`, `n > 1` | `400` with code `unsupported_parameter`, naming the offending field. Ignoring these would make the response violate your request. |

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
