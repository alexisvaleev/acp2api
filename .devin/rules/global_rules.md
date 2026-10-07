# acp2api — Project Rules

OpenAI-compatible HTTP gateway that drives Agent Client Protocol (ACP) agents.
A client speaks the ordinary OpenAI API; the gateway spawns an ACP agent CLI as a
subprocess and translates the conversation to ACP JSON-RPC over stdio.

## What this is (and is not)

- **Is:** a translator between two API shapes. OpenAI in, ACP out.
- **Is not:** a model server, an editor, or an agent implementation. It owns no
  inference and no tool loop of its own.
- The agent is a *process*, not a *model*. Every call is a full agent turn.

## Repository map

- `cmd/acp2api/` — binary entry point: flag/env parsing, wiring, graceful shutdown.
- `internal/acp/` — ACP JSON-RPC 2.0 client over stdio and the protocol types.
  The only package that knows the wire format.
- `internal/agent/` — agent registry: command, args, env, client capabilities.
  Adding an agent must never touch another package.
- `internal/client/` — the *client side* of ACP: `fs/*`, `terminal/*`,
  `session/request_permission` handlers and the policy that governs them.
- `internal/session/` — conversation ↔ ACP session mapping and process lifecycle.
- `internal/openai/` — OpenAI request/response/SSE types and the ACP→OpenAI mapping.
- `internal/handler/` — thin HTTP handlers. No protocol details leak here.
- `internal/config/` — config file + env loading and validation. Parsing is
  strict: an unknown key is an error, never silently ignored.

## Layers and dependency direction

Dependencies point inward and downward. Never upward.

```
cmd → handler → openai → session → client → acp
                     ↘ agent ↗
```

- `acp` imports nothing from this module except stdlib.
- `handler` never imports `acp` directly; it goes through `session`/`openai`.
- `agent` is a leaf: it is pure data plus capability builders, and the `Module`
  interface that per-agent packages implement.

## The module boundary

Agent-specific knowledge never enters the core. It lives in
`internal/agent/<name>/` behind `agent.Module`, and `cmd` assembles it.

- The core must not import a module. `internal/agent` defines the interface;
  `internal/agent/devin` implements it; `cmd/acp2api` wires them.
- The core must never switch on an agent's name, command, or id.
- A module names an agent that is not registered → startup error, not a no-op.
- Credentials are resolved once, at startup, and held on the `Agent`. A module
  reads the agent's own store; an explicit `api_key_env` wins over discovery.

## Non-negotiables

- **TDD.** New behavior starts with a failing test describing the observable
  outcome. The full suite is green before work is reported.
- **No real agents in tests.** Use a fake stdio agent (a Go test binary or a
  script fixture). No network, no API keys, no real CLI on `PATH`.
- **Security is a feature, not a follow-up.** Every `fs/*` path is jailed to the
  session workspace; the `filesystem` mode (`full`/`readonly`/`none`) decides
  whether `fs/*` is served, is withheld at `initialize` and refused in the
  handler, and an unrecognised mode fails closed; every permission request goes
  through an explicit policy;
  the server binds `127.0.0.1` and requires a token unless explicitly disabled.
- **Docs move with the code.** Behavior, config, or agent-support changes update
  `AGENTS.md` and the README in the same change.
- **English** for code comments, commit messages, and every rule file.

## Go conventions

- One package = one concern. Do not mix unrelated responsibilities.
- Wrap errors: `fmt.Errorf("do thing: %w", err)`. Never swallow an error.
- `context.Context` is the first parameter on anything that can block or be
  cancelled. Every agent call is bounded by a context.
- `gofmt` formatting; receiver names consistent within a type; accept interfaces
  at call sites, return concrete types.
- No goroutine outlives the thing that started it. Every spawned goroutine is
  tied to a context or a `Close`.
- Prefer the standard library. A new dependency needs a justification in the PR.

## File size limits

| Layer | Max lines | Notes |
| ----- | --------- | ----- |
| Go (`.go`) | 400 | Split by concern when approaching the limit |
| Markdown docs | — | Keep it factual; no marketing prose |

Do not split just to hit a number. Group by cohesive responsibility.

## Verification before committing

```sh
test -z "$(gofmt -l .)" && go vet ./... && golangci-lint run ./... && go test ./... -race -count=1
```

`gofmt -l` must print nothing and `golangci-lint` must report no issues.
`.pre-commit-config.yaml` runs exactly these commands on `git commit` (`pre-commit
install` once per clone), and `.golangci.yml` is the linter's configuration —
loosening it is a change to the gate, so it moves with the code and the README.
`lota.yml` covers only `dev`, `build` and `push`.

## Commits

- Conventional-commit format: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`.
- Describe the change, not the process. No bot signatures, no co-author trailers.
