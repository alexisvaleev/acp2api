// Package client implements the client half of ACP: the handlers an agent calls
// back into when it wants to read a file, write a file, or ask permission.
//
// It is the gateway's security boundary. An agent with filesystem access is
// remote code execution with extra steps, so every path is jailed to the
// workspace and every permission request goes through an explicit policy.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/quonaro/acp2api/internal/acp"
)

// Options configures a Handler.
type Options struct {
	// Workspace is the root that every filesystem path must stay inside.
	Workspace string
	// Policy decides permission requests. A nil policy cancels every request.
	Policy Policy
	// OnWrite, if set, is notified after a file is written, with the previous
	// and new content. Used for change tracking.
	OnWrite func(path, oldContent, newContent string)
	// ReadOnly refuses fs/write_text_file.
	//
	// The write capability is also withheld at initialize, so a well-behaved
	// agent never asks. This is the second line of defence for the ones that ask
	// anyway — and it is the line that actually holds, because it does not
	// depend on the agent's cooperation.
	ReadOnly bool
}

// Handler answers agent→client requests for one session.
type Handler struct {
	workspace string
	policy    Policy
	onWrite   func(path, oldContent, newContent string)
	readOnly  bool
}

// New resolves the workspace and returns a handler.
func New(opts Options) (*Handler, error) {
	workspace, err := resolveWorkspace(opts.Workspace)
	if err != nil {
		return nil, err
	}
	return &Handler{
		workspace: workspace,
		policy:    opts.Policy,
		onWrite:   opts.OnWrite,
		readOnly:  opts.ReadOnly,
	}, nil
}

// Workspace returns the resolved workspace root.
func (h *Handler) Workspace() string { return h.workspace }

// Handle implements acp.RequestHandler for one agent request.
func (h *Handler) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case acp.MethodReadTextFile:
		return h.readFile(params)
	case acp.MethodWriteTextFile:
		if h.readOnly {
			// The same code the terminal gets: "this client does not do that",
			// which is true, and which an agent handles by adapting.
			return nil, &acp.Error{
				Code:    acp.CodeMethodNotFound,
				Message: "this client is read-only: fs/write_text_file is refused",
			}
		}
		return h.writeFile(params)
	case acp.MethodRequestPerm:
		return h.permission(ctx, params)
	default:
		// Terminal and vendor-specific methods land here. They were not
		// advertised in clientCapabilities, so a well-behaved agent will not
		// call them; refusing keeps the contract honest.
		return nil, &acp.Error{
			Code:    acp.CodeMethodNotFound,
			Message: fmt.Sprintf("method %q is not supported by this client", method),
		}
	}
}

// readFile serves fs/read_text_file.
func (h *Handler) readFile(params json.RawMessage) (any, error) {
	var req acp.ReadTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, paramsError(err)
	}
	path, err := jailPath(h.workspace, req.Path)
	if err != nil {
		return nil, paramsError(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &acp.Error{Code: acp.CodeInternalError, Message: err.Error()}
	}
	return acp.ReadTextFileResponse{Content: sliceLines(string(data), req.Line, req.Limit)}, nil
}

// writeFile serves fs/write_text_file.
func (h *Handler) writeFile(params json.RawMessage) (any, error) {
	var req acp.WriteTextFileRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, paramsError(err)
	}
	path, err := jailPath(h.workspace, req.Path)
	if err != nil {
		return nil, paramsError(err)
	}

	// Best-effort: a missing file is a create, not an error.
	oldContent := ""
	if data, err := os.ReadFile(path); err == nil {
		oldContent = string(data)
	}

	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return nil, &acp.Error{Code: acp.CodeInternalError, Message: err.Error()}
	}
	if h.onWrite != nil {
		h.onWrite(path, oldContent, req.Content)
	}
	// {} rather than null: agents deserialize this result into a struct.
	return map[string]any{}, nil
}

// permission serves session/request_permission.
func (h *Handler) permission(ctx context.Context, params json.RawMessage) (any, error) {
	var req acp.RequestPermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, paramsError(err)
	}
	if h.policy == nil {
		return cancelled(), nil
	}
	optionID, err := h.policy.Decide(ctx, req)
	if err != nil {
		return nil, &acp.Error{Code: acp.CodeInternalError, Message: err.Error()}
	}
	if optionID == "" {
		return cancelled(), nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: optionID},
	}, nil
}

// cancelled reports the ACP "no option chosen" outcome.
func cancelled() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}
}

// paramsError wraps a decoding or validation failure as an ACP invalid-params error.
func paramsError(err error) *acp.Error {
	return &acp.Error{Code: acp.CodeInvalidParams, Message: err.Error()}
}
