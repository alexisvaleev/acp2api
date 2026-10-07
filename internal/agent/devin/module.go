// Package devin carries the Devin CLI knowledge the core deliberately does not
// have. The core drives Devin through the same generic ACP path as any other
// agent; everything specific to Devin lives here.
//
// What this module owns, and why it is not in the core:
//
//   - Credentials. Under ACP the Devin CLI refuses to use its own login — it
//     logs "ACP host is the sole source of credentials" — and its only
//     advertised method, devin-browser, starts a browser PKCE flow. A daemon
//     must not open windows, so the key has to be supplied. Devin already
//     stores one at ~/.local/share/devin/credentials.toml, and this module
//     reads it once at startup instead of making the operator copy it into the
//     service environment.
//   - Model selection. Devin advertises its catalog through session/new
//     configOptions, which is where a future revision of this module will
//     resolve `model: "devin/<id>"` and report the available ids.
//
// The core never imports this package; cmd assembles it.
package devin

import (
	"path/filepath"
	"strings"

	"github.com/quonaro/acp2api/internal/agent"
)

// ID is the agent this module augments.
const ID = "devin"

// credentialsFile is the name Devin uses inside its data directory.
const credentialsFile = "credentials.toml"

// credentialsKey is the field holding the API key.
const credentialsKey = "windsurf_api_key"

// Module augments the Devin agent.
type Module struct{}

// New returns the Devin module.
func New() Module { return Module{} }

// ID implements agent.Module.
func (Module) ID() string { return ID }

// Augment implements agent.Module.
//
// Devin needs nothing beyond the generic spawn shape: `devin acp` with the
// default client capabilities. The method exists so the module can grow — model
// selection will be resolved here — without the core changing.
func (m Module) Augment(a agent.Agent) agent.Agent { return a }

// Credential implements agent.Module.
//
// It reads the key Devin already stored, so the operator does not have to
// duplicate it. A missing or unreadable file is not an error: the agent may
// simply not be installed, and the core falls back to APIKeyEnv.
func (m Module) Credential(source agent.Source) (string, error) {
	dataDir := source.Env("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(source.Home, ".local", "share")
	}

	raw, err := source.ReadFile(filepath.Join(dataDir, "devin", credentialsFile))
	if err != nil {
		return "", nil //nolint:nilerr // a missing credentials file is not an error
	}
	return parseCredentials(string(raw))[credentialsKey], nil
}

// parseCredentials reads Devin's flat `key = "value"` credentials file.
//
// A minimal parser is enough and preferable to a TOML dependency: the file is
// flat, and a dependency here would pull a parser into the build for four
// lines of data.
func parseCredentials(text string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			out[key] = value
		}
	}
	return out
}
