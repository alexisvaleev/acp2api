package handler_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

func TestReadOnlyAgentRefusesWrites(t *testing.T) {
	workspace := t.TempDir()
	srv := newTestServerWithOptions(t, testOptions{
		env: map[string]string{
			"FAKE_AGENT_WRITE_PATH":    "should-not-exist.txt",
			"FAKE_AGENT_WRITE_CONTENT": "written",
		},
		readOnly:  true,
		workspace: workspace,
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "write a file"}},
	})
	defer resp.Body.Close()

	// The turn still completes: the agent is told the write is refused and
	// carries on, which is what a well-behaved agent does.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// And the file is provably absent — the refusal is enforced here, not left
	// to the agent's cooperation.
	if _, err := os.Stat(filepath.Join(workspace, "should-not-exist.txt")); err == nil {
		t.Fatal("a read-only agent wrote a file")
	}
}

// TestWritableAgentStillWrites is the control: the refusal must be the
// read-only setting, not something that broke writes generally.
func TestWritableAgentStillWrites(t *testing.T) {
	workspace := t.TempDir()
	srv := newTestServerWithOptions(t, testOptions{
		env: map[string]string{
			"FAKE_AGENT_WRITE_PATH":    "written.txt",
			"FAKE_AGENT_WRITE_CONTENT": "yes",
		},
		workspace: workspace,
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "write a file"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(workspace, "written.txt")); err != nil {
		t.Fatalf("a writable agent should have written: %v", err)
	}
}

func TestReadOnlyAgentDoesNotAdvertiseWrite(t *testing.T) {
	srv := newTestServerWithOptions(t, testOptions{readOnly: true})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestModeIsSelectedOnSessionOpen(t *testing.T) {
	srv := newTestServerWithOptions(t, testOptions{mode: "plan"})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestUnknownModeIsRejected covers the honesty rule: an agent that does not
// offer the configured mode must say so, not quietly run in its own default
// while the operator believes it is read-only.
func TestUnknownModeIsRejected(t *testing.T) {
	srv := newTestServerWithOptions(t, testOptions{mode: "read-only-ish"})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read-only-ish", "build", "plan"} {
		if !strings.Contains(failure.Error.Message, want) {
			t.Fatalf("the error should mention %q: %s", want, failure.Error.Message)
		}
	}
}
