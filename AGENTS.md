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
- `internal/openai/` — OpenAI types and the ACP→OpenAI mapping.
- `internal/handler/` — thin HTTP handlers and middleware.
- `internal/config/` — config loading.
- `reference/` — gitignored upstream checkouts used for design reference.

## Commands

```bash
# Build and test (the full check)
go build ./... && go vet ./... && go test ./...

# Run against a local agent
go run ./cmd/acp2api --config ./config.yaml

# Run a single package's tests
go test ./internal/openai/ -v
```

## Non-negotiables

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
