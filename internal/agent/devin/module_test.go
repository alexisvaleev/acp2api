package devin

import (
	"testing"

	"github.com/quonaro/acp2api/internal/agent"
)

func TestModuleID(t *testing.T) {
	if got := New().ID(); got != "devin" {
		t.Fatalf("ID() = %q", got)
	}
}

// devinCredentials mirrors the shape the CLI actually writes.
const devinCredentials = `# Devin CLI credentials
windsurf_api_key = "abc123secret"
api_server_url = "https://api.devin.ai"
devin_webapp_host = "app.devin.ai"
`

func sourceWith(files map[string]string) agent.Source {
	return agent.Source{
		Home: "/home/test",
		Env:  func(string) string { return "" },
		ReadFile: func(path string) ([]byte, error) {
			body, ok := files[path]
			if !ok {
				return nil, errNotFound
			}
			return []byte(body), nil
		},
	}
}

var errNotFound = errNoFile{}

type errNoFile struct{}

func (errNoFile) Error() string { return "no such file" }

func TestCredentialReadsTheAgentsOwnStore(t *testing.T) {
	source := sourceWith(map[string]string{
		"/home/test/.local/share/devin/credentials.toml": devinCredentials,
	})

	key, err := New().Credential(source)
	if err != nil {
		t.Fatal(err)
	}
	if key != "abc123secret" {
		t.Fatalf("key = %q", key)
	}
}

func TestCredentialHonoursXDGDataHome(t *testing.T) {
	source := sourceWith(map[string]string{
		"/custom/data/devin/credentials.toml": `windsurf_api_key="xdg-key"`,
	})
	source.Env = func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/custom/data"
		}
		return ""
	}

	key, err := New().Credential(source)
	if err != nil {
		t.Fatal(err)
	}
	if key != "xdg-key" {
		t.Fatalf("key = %q", key)
	}
}

// TestCredentialMissingFileIsNotAnError matters because the agent may simply
// not be installed; the core then falls back to the configured variable.
func TestCredentialMissingFileIsNotAnError(t *testing.T) {
	key, err := New().Credential(sourceWith(nil))
	if err != nil {
		t.Fatalf("a missing store must not be an error: %v", err)
	}
	if key != "" {
		t.Fatalf("key = %q, want empty", key)
	}
}

func TestCredentialWithoutTheKeyReturnsEmpty(t *testing.T) {
	source := sourceWith(map[string]string{
		"/home/test/.local/share/devin/credentials.toml": "api_server_url = \"https://x\"\n",
	})
	key, err := New().Credential(source)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		t.Fatalf("key = %q, want empty", key)
	}
}

func TestParseCredentials(t *testing.T) {
	got := parseCredentials(`
# comment
[section]
windsurf_api_key = "quoted"
plain = unquoted
empty =
  spaced   =   "trimmed"
malformed line without equals
`)
	want := map[string]string{
		"windsurf_api_key": "quoted",
		"plain":            "unquoted",
		"empty":            "",
		"spaced":           "trimmed",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all: %+v)", k, got[k], v, got)
		}
	}
	if _, ok := got["malformed line without equals"]; ok {
		t.Fatal("a line without '=' must be skipped")
	}
}
