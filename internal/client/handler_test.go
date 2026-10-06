package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/quonaro/acp2api/internal/acp"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newHandler(t *testing.T, policy Policy, onWrite func(string, string, string)) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	h, err := New(Options{Workspace: root, Policy: policy, OnWrite: onWrite})
	if err != nil {
		t.Fatal(err)
	}
	return h, root
}

func TestHandleReadFile(t *testing.T) {
	h, root := newHandler(t, AllowAll(), nil)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello\nworld"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := h.Handle(context.Background(), acp.MethodReadTextFile, mustJSON(t, acp.ReadTextFileRequest{
		SessionID: "s1",
		Path:      "note.txt",
	}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp, ok := result.(acp.ReadTextFileResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if resp.Content != "hello\nworld" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestHandleReadFileRejectsEscape(t *testing.T) {
	h, _ := newHandler(t, AllowAll(), nil)
	_, err := h.Handle(context.Background(), acp.MethodReadTextFile, mustJSON(t, acp.ReadTextFileRequest{
		SessionID: "s1",
		Path:      "../../etc/passwd",
	}))
	var rpcErr *acp.Error
	if err == nil {
		t.Fatal("expected an error for a path escaping the workspace")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != acp.CodeInvalidParams {
		t.Fatalf("error = %v, want invalid params", err)
	}
}

func TestHandleWriteFileNotifiesAndPersists(t *testing.T) {
	var gotPath, gotOld, gotNew string
	h, root := newHandler(t, AllowAll(), func(path, old, new string) {
		gotPath, gotOld, gotNew = path, old, new
	})
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Handle(context.Background(), acp.MethodWriteTextFile, mustJSON(t, acp.WriteTextFileRequest{
		SessionID: "s1",
		Path:      "a.txt",
		Content:   "new",
	})); err != nil {
		t.Fatalf("write: %v", err)
	}

	if gotOld != "old" || gotNew != "new" {
		t.Fatalf("onWrite got old=%q new=%q", gotOld, gotNew)
	}
	if gotPath != filepath.Join(root, "a.txt") {
		t.Fatalf("onWrite path = %q", gotPath)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(data) != "new" {
		t.Fatalf("file content = %q, err=%v", data, err)
	}
}

func TestHandlePermissionAllowAndDeny(t *testing.T) {
	req := mustJSON(t, acp.RequestPermissionRequest{
		SessionID: "s1",
		ToolCall:  acp.ToolCall{ToolCallID: "t1", Title: "run"},
		Options: []acp.PermissionOption{
			{OptionID: "y", Name: "Allow", Kind: acp.PermAllowOnce},
			{OptionID: "n", Name: "Deny", Kind: acp.PermRejectOnce},
		},
	})

	t.Run("allow", func(t *testing.T) {
		h, _ := newHandler(t, AllowAll(), nil)
		result, err := h.Handle(context.Background(), acp.MethodRequestPerm, req)
		if err != nil {
			t.Fatal(err)
		}
		resp := result.(acp.RequestPermissionResponse)
		if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "y" {
			t.Fatalf("outcome = %+v, want selected/y", resp.Outcome)
		}
	})

	t.Run("deny", func(t *testing.T) {
		h, _ := newHandler(t, DenyAll(), nil)
		result, err := h.Handle(context.Background(), acp.MethodRequestPerm, req)
		if err != nil {
			t.Fatal(err)
		}
		resp := result.(acp.RequestPermissionResponse)
		if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "n" {
			t.Fatalf("outcome = %+v, want selected/n", resp.Outcome)
		}
	})

	t.Run("nil policy cancels", func(t *testing.T) {
		h, _ := newHandler(t, nil, nil)
		result, err := h.Handle(context.Background(), acp.MethodRequestPerm, req)
		if err != nil {
			t.Fatal(err)
		}
		resp := result.(acp.RequestPermissionResponse)
		if resp.Outcome.Outcome != "cancelled" {
			t.Fatalf("outcome = %+v, want cancelled", resp.Outcome)
		}
	})
}

func TestHandleTerminalIsRefused(t *testing.T) {
	h, _ := newHandler(t, AllowAll(), nil)
	_, err := h.Handle(context.Background(), acp.MethodTerminalCreate, mustJSON(t, acp.CreateTerminalRequest{
		SessionID: "s1",
		Command:   "rm",
	}))
	var rpcErr *acp.Error
	if err == nil {
		t.Fatal("expected terminal/create to be refused")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != acp.CodeMethodNotFound {
		t.Fatalf("error = %v, want method not found", err)
	}
}

func TestReadOnlyHandlerRefusesWrites(t *testing.T) {
	root := t.TempDir()
	h, err := New(Options{Workspace: root, Policy: AllowAll(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.Handle(context.Background(), acp.MethodWriteTextFile, mustJSON(t, acp.WriteTextFileRequest{
		SessionID: "s1",
		Path:      "nope.txt",
		Content:   "nope",
	}))
	if err == nil {
		t.Fatal("a read-only handler must refuse a write")
	}
	var rpcErr *acp.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != acp.CodeMethodNotFound {
		t.Fatalf("error = %v, want method not found", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "nope.txt")); statErr == nil {
		t.Fatal("the file was written anyway")
	}
}

// TestReadOnlyHandlerStillReads is the control: read-only must not disable reads.
func TestReadOnlyHandlerStillReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Workspace: root, Policy: AllowAll(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}

	result, err := h.Handle(context.Background(), acp.MethodReadTextFile, mustJSON(t, acp.ReadTextFileRequest{
		SessionID: "s1", Path: "a.txt",
	}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.(acp.ReadTextFileResponse).Content != "readable" {
		t.Fatalf("content = %q", result.(acp.ReadTextFileResponse).Content)
	}
}

func TestParsePolicy(t *testing.T) {
	if _, err := ParsePolicy("allow"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePolicy("deny"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePolicy("maybe"); err == nil {
		t.Fatal("expected an unknown policy name to fail")
	}
}
