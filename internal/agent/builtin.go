package agent

// Builtins returns the agents this gateway knows out of the box.
//
// The commands mirror what the ecosystem uses: some CLIs expose ACP as a
// subcommand (`devin acp`, `agent acp`, `opencode acp`), others ship a
// dedicated ACP binary (`claude-agent-acp`, `codex-acp`). None of these need to
// be installed to run the gateway — an agent is spawned only when a request
// selects it, and a missing binary surfaces as a clear spawn error.
func Builtins() []Agent {
	return []Agent{
		{ID: "devin", Name: "Devin", Command: "devin", Args: []string{"acp"}},
		{ID: "cursor", Name: "Cursor Agent", Command: "agent", Args: []string{"acp"}},
		{ID: "claude", Name: "Claude Code", Command: "claude-agent-acp"},
		{ID: "codex", Name: "Codex", Command: "codex-acp"},
		{ID: "opencode", Name: "OpenCode", Command: "opencode", Args: []string{"acp"}},
		{ID: "kiro", Name: "Kiro", Command: "kiro-cli", Args: []string{"acp"}},
	}
}

// BuiltinRegistry returns a registry preloaded with Builtins.
func BuiltinRegistry() *Registry {
	return NewRegistry(Builtins()...)
}
