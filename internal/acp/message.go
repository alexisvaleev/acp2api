// Package acp implements the client half of the Agent Client Protocol:
// a JSON-RPC 2.0 connection to an agent subprocess over stdio.
//
// It is the only package in this module that knows the wire format. Callers
// above it (client, session, handler) deal in typed requests and handler
// callbacks, never in raw envelopes.
//
// The package deliberately mirrors the ACP reference SDKs: newline-delimited
// JSON-RPC 2.0 on the agent's stdin/stdout, with the client answering
// agent→client requests such as fs/read_text_file and session/request_permission.
package acp

import (
	"encoding/json"
	"fmt"
)

// jsonRPCVersion is the only protocol version this package emits.
const jsonRPCVersion = "2.0"

// Message is a JSON-RPC 2.0 envelope, used in both directions.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error renders the error the way the reference SDKs do, so log lines and
// surfaced HTTP errors read the same as in the TypeScript and Rust clients.
func (e *Error) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("[%d] %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Standard JSON-RPC 2.0 error codes. Agents are expected to use these.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// errorf builds a *Error with the given code and formatted message.
func errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// marshalParams encodes request params, tolerating a nil value.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("acp: marshal params: %w", err)
	}
	return raw, nil
}
