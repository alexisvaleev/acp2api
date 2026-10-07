# acp2api — Agent Brief

`acp2api` is an OpenAI-compatible HTTP gateway for Agent Client Protocol (ACP)
agents. Clients use the ordinary OpenAI API (`/v1/models`,
`/v1/chat/completions`, `/v1/responses`); the gateway spawns an ACP agent CLI
(`devin acp`, `agent acp`, `claude-agent-acp`, `codex-acp`, `opencode acp`) as a
subprocess and speaks newline-delimited JSON-RPC 2.0 to it over stdio.

The problem it solves: ACP connects *tools to tools* (editor ↔ agent). Most web
applications, scripts, and SDKs speak HTTP and expect an OpenAI-shaped API. This
project is the converter between the two.

## Mental model

```
OpenAI client ──HTTP/SSE──▶ gateway ──JSON-RPC over stdio──▶ ACP agent CLI
                                │                                  │
                                └◀── fs/*, terminal/*, permission ─┘
```

- The gateway is the ACP **client**; the CLI is the ACP **agent**.
- An agent is a long-lived, stateful process — not a stateless model. One ACP
  process serves many sessions; a session is one conversation.
- Sessions run in parallel, but **one session runs one turn at a time**: a
  session has a single update stream, so a second turn on the same conversation
  queues behind the first instead of taking its stream. ACP v1 cannot delete a
  session, so the process, not the session, is what the manager reclaims.
- **A session that has just been created gets the whole transcript.** The client
  remembers the conversation key; the gateway does not, so a restart or the idle
  reaper can leave it with no session for a key the client is still sending.
  Sending only the newest turn then makes the agent answer with no context,
  which looks like a normal answer. The reverse is equally a bug: replaying into
  a session that already holds the history duplicates it.
- The agent calls *back* into the gateway to read/write files, run commands, and
  ask permission. Those callbacks are the whole point; refusing them cripples
  the agent. See `internal/client/`.

## Repository map

- `cmd/acp2api/` — entry point and wiring. The CLI is the embedded Lota engine
  (`cli.yml` plus native handlers): `serve` runs the gateway, `version` prints
  the version, and no arguments prints help.
- `internal/acp/` — JSON-RPC 2.0 client over stdio + ACP protocol types.
- `internal/agent/` — agent registry (command, args, env, capabilities) and the
  `Module` interface that carries per-agent knowledge.
- `internal/agent/<name>/` — one module per agent that needs one; currently
  `devin`. The core never imports these.
- `internal/client/` — client-side ACP handlers: fs, terminal, permission.
- `internal/session/` — conversation ↔ ACP session, process lifecycle. Its log
  lines name the ACP session (`session=`); `session opened` ties it to the
  caller's conversation key, and per-turn handler records carry both
  (`conversation=` and `session=`, bound via `Request.OnSession`). Turn traffic
  — `prompt` sent and `reply` received — is logged under the `turn` module.
- `internal/openai/` — OpenAI types, the parameter policy, the tool-call
  envelope contract, and the ACP→OpenAI mapping.
- `internal/handler/` — thin HTTP handlers and middleware.
- `internal/config/` — config loading: strict YAML, so an unknown key stops the
  process instead of being silently ignored.
- `internal/logger/` — the slog handler and console format for the process log:
  `LEVEL [module] | message | key=value`, module lifted from the `module`
  attribute, level colored under `--verbose`. Level and module are fixed-width
  (`INFO`/`WARN`/`DEBU`/`ERRO`, bracketed module padded to nine) so the message
  column holds down the page. Tool calls are logged at debug
  level as `tool_calling:<external|internal|from rest>`
  (`internal/handler/toollog.go`): an MCP server the agent CLI wired in, one of
  the agent's own tools, or a caller function relayed over REST.
- `.pre-commit-config.yaml` — the git hook: gofmt, vet, golangci-lint and the
  race tests, plus file hygiene. `.golangci.yml` is the linter's configuration
  and the reason a run is green.
- `.github/workflows/` — CI (`ci.yml`) and the tag-triggered release
  (`release.yml`).
- Deployment is not containerised for the gateway: it runs on the host
  (`lota dev`), because an agent CLI reads the user's home and needs the
  runtimes its MCP servers call. `docker-compose.yml` (gitignored, like
  `config.yaml` and `workspace/`) runs only the web UI — Open WebUI —
  with host networking so it can reach the gateway on the host's loopback.

## Commands

`lota.yml` carries the dev, build and push commands:

```sh
lota dev     # run the gateway in development mode (air: rebuild + restart)
lota build   # produce bin/acp2api
lota push    # push the current branch to GitHub
```

`.air.toml` is the air configuration `lota dev` drives: build command,
entrypoint, watched and excluded directories. `lota dev` forwards only its
`addr`/`workspace`/`permission` flags, which air appends after the entrypoint's
own arguments.

Verification is plain Go:

```bash
go build ./... && go vet ./... && go test ./... -race

# Run a single package's tests
go test ./internal/openai/ -v
```

`.pre-commit-config.yaml` wires the same checks, plus `golangci-lint`, into a
git hook. Install it once per clone with `pre-commit install`; `pre-commit run
--all-files` runs the whole set over the tree. The hook and the CI job execute
identical commands, so a green hook means a green runner — and a change that
loosens one belongs in the other.

The gateway binary runs as `bin/acp2api serve`; its command line is the embedded
Lota engine (`cmd/acp2api/cli.yml`), not the `flag` package.

A release is cut by pushing a `v*` tag; `.github/workflows/release.yml` builds
the cross-platform binaries and publishes them. `.github/workflows/ci.yml` runs
the same check on every push to `main` and on pull requests.

> When writing a `lota.yml` script, remember Lota interpolates `$name`: a shell
> variable is only recognised as local when the assignment starts a line (or
> follows `;`), and loop variables only in `for`/`select`. `if x=...` is not
> recognised and fails with "variable 'x' is required".

## Non-negotiables

- **No parameter is silently ignored.** Every OpenAI parameter is honoured,
  rejected with `unsupported_parameter`, or accepted and reported in
  `acp.ignored_params`. `internal/openai/params.go` is the single source of
  truth; a field added to a request struct without a rule is a bug, and a test
  asserts it cannot happen.
- **The tool-call contract fails open.** ACP has no caller-defined functions, so
  the contract lives in the prompt (`internal/openai/preamble.go`) and is
  best-effort. Text held back by the stream must always be released as content
  when it turns out not to be an envelope — losing an answer is worse than
  missing a tool call. `internal/openai/toolstream.go` owns that rule.
- **Reasoning is not the answer.** ACP thoughts (`agent_thought_chunk`) travel
  in `reasoning_content` on the message and the stream delta, the convention
  reasoning-aware clients render; they are never mixed into `content`, and are
  mirrored in `acp.steps` (type `thought`). On a stream they bypass the tool
  hold and the output limit: a thought is neither a possible envelope nor part
  of the `max_tokens` budget. `internal/openai/mapping.go` owns that rule.
- **The ACP handshake order is fixed.** `initialize` → `authenticate` (when the
  agent advertises auth methods) → `session/new`. The Devin CLI refuses
  `session/new` with "ACP host has not authenticated" until `authenticate` is
  called, even when the CLI is already logged in. Do not reorder or skip it.
- **TDD.** New behavior and bug fixes start with a failing test. The full suite
  is green before work is reported.
- **No real agents in tests.** Use the fake stdio agent fixture. No network, no
  API keys, no dependence on a real CLI being installed.
- **Security is part of the feature.** fs paths are jailed to the workspace;
  the `filesystem` mode decides whether `fs/*` is served at all, and is withheld
  at `initialize` as well as refused in the handler; permissions run through an
  explicit policy; the server binds localhost and requires a token by default.
- **Docs in the same change.** Behavior, config, or agent-support changes update
  this file and the README together.
- **English** for code comments, commit messages, and rule files.

## Agent modules

The core drives any ACP agent through one generic path. Everything specific to
one agent lives in a module under `internal/agent/<name>/`, behind the
`agent.Module` interface:

```go
type Module interface {
	ID() string
	Augment(Agent) Agent
	Credential(Source) (string, error)
}
```

**The dependency direction is the rule.** The core never imports a module;
`cmd/acp2api` assembles them in `builtinModules()` and passes them in.
Adding an agent's knowledge means adding a package and one line there — never a
switch on an agent's name in the core.

A module is the right place for knowledge that is true of one agent and false of
the others: where it keeps its credentials, how to authenticate without opening
a window, which model catalog it advertises. `devin` is the worked example: it
reads `windsurf_api_key` from the CLI's own store, because under ACP the Devin
CLI refuses its own login and its only advertised method starts a browser flow.

Credentials are resolved **once, at startup**, and held on the `Agent` for every
spawn. A configured `api_key_env` wins; otherwise the module is asked. A
configured-but-unset variable stops the process at startup rather than surfacing
on the first request.

## Detailed rules

`.devin/rules/global_rules.md` is the source of truth for architecture, Go
conventions, file-size limits, and the commit format. Read it before writing code.

## Prior art

No upstream checkout is vendored in this repository. The design was informed by
three projects, which are worth reading on their own:

- [`acp-to-api`](https://github.com/pingu1m/acp-to-api) (Python) — OpenAI,
  Responses and Anthropic fronts, daemon mode, dashboard.
- [`acpbox`](https://github.com/EvilFreelancer/acpbox) (Python) — the clearest
  reference for ACP↔OpenAI mapping and agent adapters.
- [`cli-agent-gateway`](https://github.com/chaojimct/cli-agent-gateway) (Go) —
  the closest sibling; multi-protocol fronts and a tool-loop translator.

We borrow their *contracts* — endpoint shapes, the `acp` extension field, the
config format — and their *test ideas*, not their code. If you want a local
checkout while working, clone one into a directory of your own; do not commit it.
