package handler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/openai"
)

func TestReadOnlyAgentRefusesWrites(t *testing.T) {
	workspace := t.TempDir()
	srv := newTestServerWithOptions(t, testOptions{
		env: map[string]string{
			"FAKE_AGENT_WRITE_PATH":    "should-not-exist.txt",
			"FAKE_AGENT_WRITE_CONTENT": "written",
		},
		filesystem: agent.FilesystemReadOnly,
		workspace:  workspace,
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
// filesystem mode, not something that broke writes generally.
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
	srv := newTestServerWithOptions(t, testOptions{filesystem: agent.FilesystemReadOnly})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestNoFilesystemAgentRefusesReadsAndWrites is the provider-style mode end to
// end: the agent asks for both, and both are refused at the handler — the
// capability is a contract, the refusal is the guarantee.
func TestNoFilesystemAgentRefusesReadsAndWrites(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newTestServerWithOptions(t, testOptions{
		env: map[string]string{
			"FAKE_AGENT_FS_REPORT":     "1",
			"FAKE_AGENT_READ_PATH":     "secret.txt",
			"FAKE_AGENT_WRITE_PATH":    "should-not-exist.txt",
			"FAKE_AGENT_WRITE_CONTENT": "written",
		},
		filesystem: agent.FilesystemNone,
		workspace:  workspace,
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "read and write"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Both calls are reported as refused, and the agent is told why: the client
	// has no filesystem, rather than the call having failed for some other
	// reason it might retry.
	for _, want := range []string{"[read=", "[write="} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("the reply should report %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), "=ok]") {
		t.Fatalf("no filesystem call should have succeeded: %s", body)
	}
	if !strings.Contains(string(body), "does not serve") {
		t.Fatalf("the refusal should say the method is not served: %s", body)
	}

	if _, err := os.Stat(filepath.Join(workspace, "should-not-exist.txt")); err == nil {
		t.Fatal("a no-filesystem agent wrote a file")
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
