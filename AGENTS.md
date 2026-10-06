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
- The agent calls *back* into the gateway to read/write files, run commands, and
  ask permission. Those callbacks are the whole point; refusing them cripples
  the agent. See `internal/client/`.

## Repository map

- `cmd/acp2api/` — entry point, wiring, graceful shutdown.
- `internal/acp/` — JSON-RPC 2.0 client over stdio + ACP protocol types.
- `internal/agent/` — agent registry (command, args, env, capabilities).
- `internal/client/` — client-side ACP handlers: fs, terminal, permission.
- `internal/session/` — conversation ↔ ACP session, process lifecycle.
- `internal/openai/` — OpenAI types, the parameter policy, the tool-call
  envelope contract, and the ACP→OpenAI mapping.
- `internal/handler/` — thin HTTP handlers and middleware.
- `internal/config/` — config loading.
- `reference/` — gitignored upstream checkouts used for design reference.

## Commands

`lota.yml` wraps everything; prefer it over raw commands.

```sh
lota check       # full verification: format, vet, race tests
lota dev         # run the gateway in development mode
lota agents      # which agent CLIs are installed
lota smoke       # build, start, curl the API, stop
lota conformance # assert the whole HTTP surface against a live server
lota build       # produce bin/acp2api
```

Raw equivalents, for reference:

```bash
go build ./... && go vet ./... && go test ./... -race

# Run a single package's tests
go test ./internal/openai/ -v
```

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
- **The ACP handshake order is fixed.** `initialize` → `authenticate` (when the
  agent advertises auth methods) → `session/new`. The Devin CLI refuses
  `session/new` with "ACP host has not authenticated" until `authenticate` is
  called, even when the CLI is already logged in. Do not reorder or skip it.
- **TDD.** New behavior and bug fixes start with a failing test. The full suite
  is green before work is reported.
- **No real agents in tests.** Use the fake stdio agent fixture. No network, no
  API keys, no dependence on a real CLI being installed.
- **Security is part of the feature.** fs paths are jailed to the workspace;
  permissions run through an explicit policy; the server binds localhost and
  requires a token by default.
- **Docs in the same change.** Behavior, config, or agent-support changes update
  this file and the README together.
- **English** for code comments, commit messages, and rule files.

## Detailed rules

`.devin/rules/global_rules.md` is the source of truth for architecture, Go
conventions, file-size limits, and the commit format. Read it before writing code.

## Prior art (in `reference/`)

- `acp-to-api` (Python) — OpenAI/Responses/Anthropic fronts, daemon mode, dashboard.
- `acpbox` (Python) — the clearest reference for ACP↔OpenAI mapping and agent adapters.
- `cli-agent-gateway` (Go) — the closest sibling; multi-protocol fronts and a tool-loop translator.

We borrow their *contracts* (endpoint shapes, the `acp` extension field, config
format) and their *test ideas*, not their code.
