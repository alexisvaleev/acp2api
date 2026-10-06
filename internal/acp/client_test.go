package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/fakeagent"
)

func TestMain(m *testing.M) {
	if fakeagent.MaybeRun() {
		return
	}
	os.Exit(m.Run())
}

// startFake spawns the fake agent as a subprocess of the test binary.
func startFake(t *testing.T, onRequest acp.RequestHandler, onNotify acp.NotificationHandler, env map[string]string) *acp.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	c, err := acp.Start(ctx, acp.Options{
		Command:        os.Args[0],
		Env:            fakeagent.Env(env),
		Dir:            t.TempDir(),
		OnRequest:      onRequest,
		OnNotification: onNotify,
	})
	if err != nil {
		t.Fatalf("start fake agent: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// handshake initializes the connection and opens a session.
func handshake(t *testing.T, c *acp.Client) acp.NewSessionResponse {
	t.Helper()
	ctx := context.Background()
	if _, err := c.Request(ctx, acp.MethodInitialize, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion,
		ClientInfo:      acp.Implementation{Name: "acp2api-test", Version: "0.0.1"},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	raw, err := c.Request(ctx, acp.MethodSessionNew, acp.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	var session acp.NewSessionResponse
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatalf("decode session/new: %v", err)
	}
	if session.SessionID == "" {
		t.Fatal("session/new returned an empty session id")
	}
	return session
}

func TestInitializeSessionPromptStreamsUpdates(t *testing.T) {
	var mu sync.Mutex
	var updates []acp.SessionUpdate

	c := startFake(t, nil, func(method string, params json.RawMessage) {
		if method != acp.MethodSessionUpdate {
			return
		}
		var n acp.SessionUpdateNotification
		if err := json.Unmarshal(params, &n); err != nil {
			t.Errorf("decode session/update: %v", err)
			return
		}
		mu.Lock()
		updates = append(updates, n.Update)
		mu.Unlock()
	}, map[string]string{"FAKE_AGENT_CHUNKS": "3"})

	session := handshake(t, c)
	if len(session.ConfigOptions) == 0 {
		t.Fatal("expected the agent to advertise configOptions")
	}

	raw, err := c.Request(context.Background(), acp.MethodSessionPrompt, acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	var prompt acp.PromptResponse
	if err := json.Unmarshal(raw, &prompt); err != nil {
		t.Fatalf("decode session/prompt: %v", err)
	}
	if prompt.StopReason != acp.StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", prompt.StopReason, acp.StopEndTurn)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 3 {
		t.Fatalf("got %d updates, want 3", len(updates))
	}
	if updates[0].SessionUpdate != acp.UpdateAgentMessageChunk {
		t.Fatalf("update kind = %q, want %q", updates[0].SessionUpdate, acp.UpdateAgentMessageChunk)
	}
	if updates[0].Content == nil || updates[0].Content.Text == "" {
		t.Fatal("expected text content on the first update")
	}
	if len(updates[0].Raw) == 0 {
		t.Fatal("expected the raw payload to be preserved")
	}
}

func TestAgentPermissionRequestIsAnsweredByClient(t *testing.T) {
	var mu sync.Mutex
	var seen acp.RequestPermissionRequest

	c := startFake(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != acp.MethodRequestPerm {
			return nil, fmt.Errorf("unexpected agent request %q", method)
		}
		var req acp.RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		mu.Lock()
		seen = req
		mu.Unlock()
		return acp.RequestPermissionResponse{
			Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "allow"},
		}, nil
	}, nil, map[string]string{"FAKE_AGENT_REQUEST_PERM": "1"})

	session := handshake(t, c)
	if _, err := c.Request(context.Background(), acp.MethodSessionPrompt, acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("do work")},
	}); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen.SessionID != session.SessionID {
		t.Fatalf("permission session = %q, want %q", seen.SessionID, session.SessionID)
	}
	if len(seen.Options) != 2 {
		t.Fatalf("permission options = %d, want 2", len(seen.Options))
	}
}

func TestAgentReadFileRequestIsAnsweredByClient(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/note.txt"
	if err := os.WriteFile(path, []byte("file-contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var readPath string

	c := startFake(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != acp.MethodReadTextFile {
			return nil, fmt.Errorf("unexpected agent request %q", method)
		}
		var req acp.ReadTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		mu.Lock()
		readPath = req.Path
		mu.Unlock()
		return acp.ReadTextFileResponse{Content: "file-contents"}, nil
	}, nil, map[string]string{"FAKE_AGENT_READ_PATH": path})

	session := handshake(t, c)
	if _, err := c.Request(context.Background(), acp.MethodSessionPrompt, acp.PromptRequest{
		SessionID: session.SessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("read it")},
	}); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if readPath != path {
		t.Fatalf("read path = %q, want %q", readPath, path)
	}
}

func TestInitializeErrorIsSurfaced(t *testing.T) {
	c := startFake(t, nil, nil, map[string]string{"FAKE_AGENT_FAIL_INIT": "1"})
	_, err := c.Request(context.Background(), acp.MethodInitialize, acp.InitializeRequest{ProtocolVersion: 1})
	if err == nil {
		t.Fatal("expected initialize to fail")
	}
	var rpcErr *acp.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error %v is not a JSON-RPC *acp.Error", err)
	}
	if rpcErr.Code != acp.CodeInternalError {
		t.Fatalf("error code = %d, want %d", rpcErr.Code, acp.CodeInternalError)
	}
}

func TestRequestAfterCloseFails(t *testing.T) {
	c := startFake(t, nil, nil, nil)
	handshake(t, c)

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := c.Request(context.Background(), acp.MethodSessionNew, acp.NewSessionRequest{}); err == nil {
		t.Fatal("expected a request on a closed client to fail")
	}
}
