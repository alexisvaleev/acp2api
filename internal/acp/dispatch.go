package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

/* ---- read side ---- */

// readLoop reads newline-delimited JSON from the agent's stdout until EOF.
func (c *Client) readLoop() {
	reader := bufio.NewReaderSize(c.stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			c.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

// stderrLoop retains the agent's stderr tail for diagnostics.
func (c *Client) stderrLoop() {
	_, _ = io.Copy(c.stderrBuf, c.stderr)
}

// waitLoop waits for the agent to exit, then settles every pending request.
func (c *Client) waitLoop() {
	err := c.cmd.Wait()

	c.mu.Lock()
	c.closed = true
	c.exitErr = err
	pending := c.pending
	c.pending = make(map[int64]chan Message)
	c.mu.Unlock()

	close(c.done)
	c.cancel()

	cause := exitCause(err)
	for _, ch := range pending {
		select {
		case ch <- Message{Error: errorf(CodeInternalError, "%s", cause)}:
		default:
		}
	}
}

// handleLine decodes and routes one JSON-RPC message.
func (c *Client) handleLine(line []byte) {
	var msg Message
	if err := json.Unmarshal(bytes.TrimSpace(line), &msg); err != nil {
		slog.Debug("acp: dropping non-JSON line from agent", "line", truncate(line, 200))
		return
	}
	c.handleMessage(&msg)
}

// handleMessage routes a decoded message.
//
// Requests are dispatched on their own goroutine so a handler that blocks on a
// policy decision cannot stall the read loop and, with it, the responses to
// requests this client is still waiting for. Notifications stay on the read
// loop to preserve their order.
func (c *Client) handleMessage(msg *Message) {
	switch {
	case msg.Method != "" && msg.ID != nil:
		go c.dispatchRequest(msg)
	case msg.Method != "":
		c.dispatchNotification(msg)
	case msg.ID != nil:
		c.dispatchResponse(msg)
	}
}

// dispatchResponse delivers a response to the waiting Request.
func (c *Client) dispatchResponse(msg *Message) {
	var id int64
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		ch <- *msg
	}
}

// dispatchRequest runs the request handler and writes its result back.
func (c *Client) dispatchRequest(msg *Message) {
	if c.opts.OnRequest == nil {
		_ = c.write(Message{JSONRPC: jsonRPCVersion, ID: msg.ID, Error: errorf(CodeMethodNotFound, "no request handler registered")})
		return
	}

	result, err := c.opts.OnRequest(c.ctx, msg.Method, msg.Params)
	if err != nil {
		var rpcErr *Error
		if errors.As(err, &rpcErr) {
			_ = c.write(Message{JSONRPC: jsonRPCVersion, ID: msg.ID, Error: rpcErr})
		} else {
			_ = c.write(Message{JSONRPC: jsonRPCVersion, ID: msg.ID, Error: errorf(CodeInternalError, "%s", err.Error())})
		}
		return
	}

	// A nil result becomes {} rather than null: several agents deserialize the
	// result into a struct and reject null.
	raw := json.RawMessage("{}")
	if result != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			_ = c.write(Message{JSONRPC: jsonRPCVersion, ID: msg.ID, Error: errorf(CodeInternalError, "marshal result: %s", err.Error())})
			return
		}
		raw = encoded
	}
	_ = c.write(Message{JSONRPC: jsonRPCVersion, ID: msg.ID, Result: raw})
}

// dispatchNotification forwards a notification to the handler.
func (c *Client) dispatchNotification(msg *Message) {
	if c.opts.OnNotification != nil {
		c.opts.OnNotification(msg.Method, msg.Params)
	}
}

/* ---- write side ---- */

// write sends a message to the agent's stdin as one newline-terminated line.
func (c *Client) write(msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("acp: marshal message: %w", err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("acp: write to closed connection")
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("acp: write to agent: %w", err)
	}
	return nil
}

/* ---- helpers ---- */

// forget drops a pending request slot.
func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// exitCause returns the stored process-exit error, or nil.
func (c *Client) exitCause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitErr
}

// exitCause normalises a process-wait error into a wrapped cause.
func exitCause(err error) error {
	if err == nil {
		return errors.New("agent exited")
	}
	return fmt.Errorf("agent exited: %w", err)
}

// truncate clips a byte slice for logging.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
