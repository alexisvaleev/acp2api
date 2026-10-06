package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// RequestHandler answers an agent→client request. Returning an error sends a
// JSON-RPC error response back to the agent; returning a *Error controls the
// error code, anything else becomes CodeInternalError.
//
// The context is cancelled when the client closes or the agent exits, so a
// handler that blocks (permission decisions, terminal waits) unblocks on
// shutdown instead of leaking a goroutine.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// NotificationHandler observes an agent→client notification. It runs on the
// read loop and must not block: notifications are ordered, and a slow handler
// stalls every session on the connection.
type NotificationHandler func(method string, params json.RawMessage)

// Options configures a Client. Command is required; everything else is optional.
type Options struct {
	// Command is the agent executable, resolved on PATH or absolute.
	Command string
	// Args are passed to the agent verbatim, e.g. []string{"acp"}.
	Args []string
	// Env is the complete child environment. Nil inherits the parent's.
	Env []string
	// Dir is the agent's working directory — its workspace root.
	Dir string
	// OnRequest handles agent→client requests. Nil answers every request with
	// CodeMethodNotFound, which is correct for a client with no capabilities.
	OnRequest RequestHandler
	// OnNotification observes agent→client notifications, e.g. session/update.
	OnNotification NotificationHandler
	// RequestTimeout bounds a request whose context carries no deadline.
	// Zero means the request is bounded only by its context.
	RequestTimeout time.Duration
	// StderrLimit caps the retained trailing stderr bytes. Zero uses a 64 KiB default.
	StderrLimit int
}

const defaultStderrLimit = 64 << 10

// Client is a JSON-RPC 2.0 connection to a running ACP agent subprocess.
//
// A Client is safe for concurrent use: Request and Notify may be called from
// any goroutine, and incoming requests are dispatched without blocking the
// read loop.
type Client struct {
	opts Options

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan Message
	closed  bool
	exitErr error

	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	stderrBuf *boundedBuffer
}

// Start spawns the agent described by opts and begins serving its messages.
//
// The returned client's lifetime is bound to ctx: cancelling ctx terminates the
// agent process tree. Close terminates it earlier and explicitly.
func Start(ctx context.Context, opts Options) (*Client, error) {
	if opts.Command == "" {
		return nil, errors.New("acp: command is required")
	}
	if opts.StderrLimit == 0 {
		opts.StderrLimit = defaultStderrLimit
	}

	clientCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		opts:      opts,
		pending:   make(map[int64]chan Message),
		done:      make(chan struct{}),
		ctx:       clientCtx,
		cancel:    cancel,
		stderrBuf: newBoundedBuffer(opts.StderrLimit),
	}

	c.cmd = exec.CommandContext(clientCtx, opts.Command, opts.Args...)
	c.cmd.Dir = opts.Dir
	if opts.Env != nil {
		c.cmd.Env = opts.Env
	}

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stdout pipe: %w", err)
	}
	stderr, err := c.cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("acp: stderr pipe: %w", err)
	}
	c.stdin, c.stdout, c.stderr = stdin, stdout, stderr

	if err := c.cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("acp: start agent %q: %w", opts.Command, err)
	}

	go c.readLoop()
	go c.stderrLoop()
	go c.waitLoop()
	return c, nil
}

// Request sends a request and waits for its response. It returns the raw result
// JSON, or an error if the agent replied with a JSON-RPC error, the context
// ended, or the agent exited first.
func (c *Client) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	paramsRaw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.closed {
		cause := c.exitErr
		c.mu.Unlock()
		return nil, fmt.Errorf("acp: request %s on closed connection: %w", method, exitCause(cause))
	}
	c.nextID++
	id := c.nextID
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	idRaw, err := json.Marshal(id)
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("acp: marshal request id: %w", err)
	}
	if err := c.write(Message{JSONRPC: jsonRPCVersion, ID: idRaw, Method: method, Params: paramsRaw}); err != nil {
		c.forget(id)
		return nil, err
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.opts.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.RequestTimeout)
		defer cancel()
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("acp: %s: %w", method, msg.Error)
		}
		return msg.Result, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, fmt.Errorf("acp: %s: %w", method, ctx.Err())
	case <-c.done:
		c.forget(id)
		return nil, fmt.Errorf("acp: %s: %w", method, exitCause(c.exitCause()))
	}
}

// Notify sends a notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	paramsRaw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(Message{JSONRPC: jsonRPCVersion, Method: method, Params: paramsRaw})
}

// Wait blocks until the agent process exits.
func (c *Client) Wait() error {
	<-c.done
	return c.exitCause()
}

// Close terminates the agent process and unblocks every pending request.
// It is idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.cancel()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("acp: kill agent: %w", err)
		}
	}
	return nil
}

// Closed reports whether the agent has exited or the client was closed.
func (c *Client) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// PID returns the agent process id, or 0 if the process never started.
func (c *Client) PID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Stderr returns the retained tail of the agent's stderr.
func (c *Client) Stderr() string { return c.stderrBuf.String() }
