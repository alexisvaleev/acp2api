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

`lota.yml` carries the dev, build and push commands:

```sh
lota dev     # run the gateway under air: rebuilds and restarts on change
lota build   # build the binary to bin/acp2api
lota push    # push the current branch to GitHub
```

`lota dev` takes flags:

```sh
lota dev -w ~/code/some-repo          # workspace the agents operate in
lota dev --addr 127.0.0.1:9000
lota dev -p deny                      # reject every permission request
```

`lota dev` runs the gateway through [air](https://github.com/air-verse/air),
configured by `.air.toml`: it builds to `bin/acp2api` and rebuilds and restarts
the gateway whenever a watched file changes. `bin/` and `workspace/` are
excluded from the watcher — the agents write into the workspace, and a rebuild
per agent-written file would be a reload loop.

`lota dev` forwards only its `-w`, `--addr` and `-p` flags to `serve`; the build
command, entrypoint and watched/excluded directories live in `.air.toml`, so
`air -- --workspace ~/code/some-repo` runs the same loop without `lota`.

Without `lota`, the binary is ordinary:

```sh
go build -o bin/acp2api ./cmd/acp2api
ACP2API_TOKEN=dev-token ./bin/acp2api serve --workspace ~/code/some-repo --verbose
```

The command line is the embedded Lota engine (`cmd/acp2api/cli.yml`), so the
binary carries its own commands: `serve` starts the gateway, `version` prints
the version, and running `acp2api` with no arguments prints help. `serve` takes
`--config`, `--addr`, `--workspace`, `--permission` and `--verbose`.
Environment: `ACP2API_ADDR`, `ACP2API_TOKEN`, `ACP2API_WORKSPACE`,
`ACP2API_PERMISSION`.

### Logs

The gateway logs to stderr, one record per line:

```text
INFO  [session] | agent ready | agent=devin pid=91240 workspace=. images=true
DEBU  [agent]   | credential resolved | agent=devin source=module
INFO  [acp2api] | listening | addr=127.0.0.1:8720 agents=devin,opencode
```

The subsystem — `acp2api`, `session`, `agent`, `acp`, `handler` — is shown in
brackets, followed by the message and its `key=value` attributes. `--verbose` on
`serve` adds debug records and colors the level name.

Both the level and the module are rendered at a fixed width, so the message
column does not move from line to line: `INFO`, `WARN`, `DEBU` and `ERRO` are
four characters each (the last two truncated), and the bracketed subsystem is
padded to nine. A longer subsystem — `tool_calling:*` is the one — overflows the
column rather than stretching every other line.

Lines that belong to one conversation are tagged with it. `conversation=` is
the caller's key (`conversation_id`, the conversation header, or `user`), and
`session=` is the ACP session id — the same value a response carries in
`acp.session_id`, so a log line correlates end to end. The line that ties the
two together is `session opened`; the idle reaper reports `session expired`
when it forgets one:

```text
INFO [session] | session opened  | agent=devin session=sess_… conversation=chat-42
INFO [session] | session expired | agent=devin session=sess_… conversation=chat-42
```

Every turn logs what crossed the wire under the `turn` module: `prompt` is the
text handed to the agent (`replayed=true` when a fresh session received the
whole transcript instead of the newest turn), `reply` is what it answered.
Text is quoted, so a record always stays one line:

```text
INFO [turn]    | prompt | agent=devin session=sess_… conversation=chat-42 chars=128 text="…"
INFO [turn]    | reply  | conversation=chat-42 session=sess_… stop=end_turn chars=512 text="…"
```

Tool calls are logged under their own subsystem, one line per call, split by
where the tool lives:

```text
DEBU [tool_calling:external] | tool_call | conversation=chat-42 session=sess_… tool_call_id=tc-1 name=mcp__github__create_issue title="Create issue" kind=other status=in_progress
DEBU [tool_calling:internal] | tool_call | conversation=chat-42 session=sess_… tool_call_id=tc-2 name=exec title="Run the tests" kind=execute status=in_progress
DEBU [tool_calling:from rest] | tool_call | conversation=chat-42 session=sess_… tool_call_id=call_get_weather_1 name=get_weather arguments={"city":"Paris"}
```

- `external` — an MCP server wired into the agent CLI itself. Recognised by the
  `mcp__<server>__<tool>` namespace on the tool's programmatic name, falling back
  to the title when the agent reports no name.
- `internal` — one of the agent's own built-in tools (`exec`, `edit`, …).
- `from rest` — a caller-declared function the gateway relays back over the
  OpenAI API.

A `tool_call_update` is a patch keyed by id and usually omits the name, so it
inherits the classification its initial `tool_call` established. These records
are debug level, so they appear only under `--verbose`.

### Quick start

```sh
go build -o bin/acp2api ./cmd/acp2api

export ACP2API_TOKEN=change-me
./bin/acp2api serve --workspace /path/to/your/repo
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

### A web UI next to it

The gateway stays on the host — `lota dev` — and only the UI is
containerised. That is deliberate: an agent CLI is a *process* that reads the
user's home (credentials, MCP config, caches) and needs the runtimes its MCP
servers call (`npx`, `go`), all of which are already set up on the host and
would otherwise have to be mounted into an image.

A worked `docker-compose.yml` is local-only (gitignored). It runs Open WebUI —
multi-user, with an admin panel — and reaches the host's gateway through host
networking:

```yaml
network_mode: host
environment:
  OPENAI_API_BASE_URLS: http://127.0.0.1:8720/v1
  OPENAI_API_KEYS: dev-token      # must match the gateway's token
  HOST: 127.0.0.1                 # uvicorn binds here, not by a ports mapping
  PORT: "3000"
```

Host networking is not a shortcut: a bridge container cannot reach the host's
loopback — `127.0.0.1` inside it is the container itself — and the gateway
binds loopback by default. The UI's own bind is kept on `127.0.0.1` so it does
not land on the LAN.

`config.yaml`, `docker-compose.yml` and `workspace/` are machine-local too,
and they hold their values literally — no environment indirection.

### Reaching the agents' APIs through a proxy

If the host needs a proxy to reach an agent's API — a corporate egress, or an
API that is not served in the host's region — name it in the `proxy:` block,
fed from the environment:

```yaml
proxy:
  url: ${ACP2API_PROXY_URL:-}      # e.g. http://127.0.0.1:2080
  no_proxy: localhost,127.0.0.1
```

For the common single-url case the block collapses to a bare string —
`proxy: http://127.0.0.1:2080` is shorthand for `proxy: {url: …}`. A scheme is
still required; a bare `host:port` is rejected rather than guessed at.

The gateway renders it as `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` and
`NO_PROXY` (both cases) on every agent it spawns, and reports the redacted
value in its startup line. Note that the agents inherit the process
environment as well, so running the gateway from a shell that already exports
`HTTPS_PROXY` is enough; the `proxy:` block is what makes it explicit and
per-agent.

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
- `devin/swe-2-high` — run Devin and select that model.

The list after the slash is **the agent's own catalog**, discovered from the
agent, not configured here. It appears on `/v1/models` as `agent/model` entries
once the agent has been talked to, because ACP has no separate discovery call —
the catalog arrives with the first session.

An unknown model is refused with the list of ones the agent does offer. It is
never quietly replaced by the agent's default: a caller who named a model asked a
specific question, and answering a different one is worse than failing.

Set `discover_models: true` to read every agent's catalog in the background at
startup, so `/v1/models` is complete before the first request. It is off by
default because discovery starts each agent, which costs its cold start.

Agents vary in what they advertise. Devin lists display names (`swe-2-high`,
`claude-opus-5-5-medium`) alongside internal enum ids (`MODEL_CLAUDE_4_5_OPUS`);
both are selectable, and both are listed, because filtering what an agent
advertises would mean guessing which of its ids are real.

## Conversations

An ACP session is stateful, which the OpenAI request shape does not express.

- **With a conversation key** the turn runs on a persistent session: the agent
  keeps the history, and only the newest user message is sent to it.
- **Without one** the session is ephemeral and the whole `messages` transcript
  is flattened into the prompt, because the agent starts from nothing.

The key is resolved from the most explicit source available:

1. `conversation_id` in the body — this gateway's own extension.
2. the header named by `conversation_header` (default `X-Chat-Id`).
3. `user` in the body, for clients that cannot set a custom field at all.

The header exists for clients that can send a header per chat but cannot add a
field to the body. **Open WebUI** is the motivating one: set
`ENABLE_FORWARD_USER_INFO_HEADERS=true` on the container and it sends
`X-OpenWebUI-Chat-Id`, so point the setting at that name:

```yaml
conversation_header: X-OpenWebUI-Chat-Id
```

Set `conversation_header: ""` to read no header at all.

The key is trusted input from an authenticated caller, and it *is* the session:
anyone holding the token can join a chat by naming its key. Locally that is the
point; it is not an isolation boundary.

### A key is a promise the gateway cannot always keep

The client remembers a conversation key; the gateway does not. A restart, or the
idle reaper, can leave the gateway with no session for a key the client is still
sending. When that happens the session is created fresh and **the whole
transcript is replayed into it** — a session that has just been created holds no
history, so sending only the newest turn would leave the agent answering with no
context at all, which reads as a confident answer to a question nobody asked.

The reverse holds too: a session the gateway already had holds the history
itself, so replaying the transcript into it would duplicate what the agent can
already see. Both cases are covered by tests.

### Concurrency

One agent process serves many sessions, so **different conversations run in
parallel**: nothing in the gateway serialises them, and the agent CLI is free to
run them at once. Measured against the Devin CLI on `swe-2-high`, six concurrent
conversations finished in the time of one.

**One conversation does not parallelise.** A session has a single update stream
and a single history, so two turns on the same `conversation_id` are queued: the
second waits for the first, then runs and gets its own answer. It is not allowed
to take the first turn's stream, which is what would otherwise truncate one
answer and leak its text into the other. A request that gives up while queued
fails with its own context error rather than running late.

The trade-off to plan for: the wait counts against the queued request's own
timeout, so a client that fires two messages into one conversation at once can
see the second time out behind a long first one.

Sessions are never deleted — ACP v1 has no `session/delete`. The gateway forgets
an idle session, but the CLI holds it until the process exits, and a connection
is only reclaimed once it has no sessions and has itself gone idle
(`session_ttl_seconds`). A client that never sends `conversation_id` therefore
grows the agent's session count one request at a time.

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

Rejections carry the reason and the offending parameter:

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

## Reasoning

An agent's thoughts arrive as ACP `agent_thought_chunk` updates. They are not
part of the answer, so they never enter `content`. Instead they travel in
`reasoning_content`, the field DeepSeek introduced and clients such as Open WebUI
render, on both the message and the streaming delta:

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "reasoning_content": "The user wants …",
      "content": "Here is the answer."
    },
    "finish_reason": "stop"
  }]
}
```

`reasoning_content` is not part of the canonical OpenAI schema, but it is the de
facto convention for exposing a model's thinking, and it is the only shape a
reasoning-aware client will display. A client that does not know it ignores the
field, which is exactly how an additive field should behave.

On a stream the reasoning deltas arrive before the answer, and they are sent
outside both the tool-call hold and the `max_tokens` / `stop` limits: a thought
is not the reply, so it is neither held back as a possible envelope nor counted
against the output cap. The thoughts are also kept in `acp.steps` (type
`thought`), so the `acp` extension still carries a complete activity summary.

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

YAML, loaded from `--config`, then overridden by environment variables, then by
the `serve` flags. See `config.example.yaml`. JSON is accepted too — YAML is a
superset, so an existing `config.json` keeps working through the same parser.

`${VAR}` and `${VAR:-default}` are substituted before parsing, so one file can
serve as a template for several environments. An unset variable with no default
is an error rather than an empty string: a typo in `${ACP2API_TOKEN}` would
otherwise leave the gateway running without authentication. Write `${VAR:-}` to
say that empty is intended. Lines that start with `#` are comments and are not
expanded.

| Key | Default | Notes |
| --- | ------- | ----- |
| `addr` | `127.0.0.1:8720` | Listen address. Binding off-loopback requires a token. |
| `workspace` | process cwd | Default agent working directory. Under `filesystem: none` the default is a private scratch directory instead. |
| `token` | — | Bearer token for `/v1/*`. |
| `permission` | `allow` | `allow` or `deny` for agent permission requests. |
| `request_timeout_seconds` | `120` | Bounds one ACP request. |
| `session_ttl_seconds` | `1800` | Idle sessions and agent processes are reaped. Negative disables. |
| `agents` | built-ins | Overrides by id, or new agents. |
| `disable_builtins` | `false` | `true` serves only the agents listed above, so `/v1/models` matches what the host can run. |
| `filesystem` | `full` | `full`, `readonly` or `none`. Decides what `fs/*` callbacks are served, and which capabilities are advertised at `initialize`. |
| `mode` | agent default | Session mode selected after opening a session, e.g. `plan` or `ask`. Checked against what the agent advertises. |
| `conversation_header` | `X-Chat-Id` | Request header that keys a session, for clients that cannot set a body field. Empty disables it. |
| `proxy` | none | Routes the agents' outbound traffic; `url`, optional `http`/`https`/`no_proxy`. |
| `discover_models` | `false` | Read each agent's model catalog in the background at startup, so `/v1/models` lists models as well as agents. |

Every agent entry may override `workspace`, `filesystem`, `mode` and `proxy`, and
may add its own `env`. A per-agent proxy or workspace replaces the global one
rather than merging with it.

The file is parsed strictly: an unknown key is an error at startup, not a
warning. A typo — or a key a previous version had — must not quietly change what
the gateway does.

### The filesystem mode, and what a workspace is for

An ACP agent is not a model. It is a process with tools, and it uses them: ask
it about the weather and it may still look around the directory it was started
in. `filesystem` decides how much of that the gateway will do on its behalf, in
three levels that nest:

| Mode | `fs/read_text_file` | `fs/write_text_file` |
| ---- | ------------------- | -------------------- |
| `full` | served | served |
| `readonly` | served | refused |
| `none` | refused | refused |

The capability is dropped from the `initialize` handshake as well, so a
well-behaved agent never asks. The refusal in the handler is what answers the
ones that ask anyway, and it is the line that holds, because it does not depend
on the agent's cooperation. `permission: deny` does **not** cover this:
permission requests and filesystem callbacks are separate paths.

`none` is the provider-style mode: the agent is told the client has no
filesystem, so it answers instead of exploring. With `workspace` unset it also
gets a private scratch directory as its working directory — created at startup
and removed at exit — instead of the process's own directory, which would
otherwise be the project the operator was withholding.

Be clear about what `none` is. It removes the **gateway's** filesystem surface,
not the agent process's own access to the host: an agent CLI is a real process
running with the operator's permissions. It is a contract with a cooperative
agent, not a sandbox.

`mode` is the third lever, and it works on the agent's side: `plan` and `ask`
are read-only on most agents. Unlike `filesystem`, it depends on the agent
cooperating, which is why both exist.

Environment: `ACP2API_ADDR`, `ACP2API_TOKEN`, `ACP2API_WORKSPACE`,
`ACP2API_PERMISSION`, `ACP2API_FILESYSTEM`.

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
- **A filesystem mode, chosen up front.** `filesystem` is `full`, `readonly` or
  `none`. The capability is withheld at `initialize` *and* the call is refused,
  so the agent is neither misled nor trusted. `none` removes the surface
  entirely and gives the agent a private scratch directory instead of the
  process's own.
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
go build ./... && go vet ./... && go test ./... -race
```

Those checks, plus `golangci-lint`, are wired into a git hook. Install it once
per clone:

```sh
pre-commit install          # run the checks on every `git commit`
pre-commit run --all-files  # run them over the whole tree
```

`.pre-commit-config.yaml` defines the hooks and `.golangci.yml` configures the
linter. The hook and the CI job run the same commands, so a green hook means a
green runner.

Tests never touch a real agent: they run a scriptable fake ACP agent
(`internal/fakeagent`) inside the test binary.

Architecture, conventions, and file-size limits live in
`.devin/rules/global_rules.md`. The agent-facing brief is `AGENTS.md`.

## Releases

A release is cut by pushing a `v*` tag — nothing is built by hand:

```sh
git tag v0.2.0
git push origin v0.2.0        # or: lota push v0.2.0
```

`.github/workflows/release.yml` then builds `acp2api` for linux, macOS and
Windows (amd64 and arm64), writes `checksums.txt`, and publishes a GitHub
Release with the binaries attached. The tag is compiled in, so `acp2api
version` and the version the gateway reports to an agent match the release.

`.github/workflows/ci.yml` runs `gofmt`, `go vet`, `golangci-lint` and the race
tests on every push to `main` and on every pull request.

## Prior art

`acp-to-api` and `acpbox` (Python) and `cli-agent-gateway` (Go) solve the same
problem. This project borrows their contracts — endpoint shapes, the `acp`
extension field — not their code.

## License

MIT
