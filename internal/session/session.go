package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/client"
)

// updateBuffer bounds how many session updates may queue for one consumer.
// A full buffer means the consumer stopped draining — the HTTP client is gone
// or wedged — so the turn is aborted rather than growing memory without bound.
const updateBuffer = 1024

// errBackpressure reports a consumer that stopped reading updates.
var errBackpressure = errors.New("session: consumer stopped reading updates")

// state is one ACP session on a connection.
type state struct {
	id      string
	client  *acp.Client
	handler *client.Handler

	mu         sync.Mutex
	updates    chan acp.SessionUpdate
	abortErr   error
	cancelTurn context.CancelFunc
	lastUsed   time.Time
}

// deliver queues one session update for the active consumer.
//
// It runs on the connection's read loop and must never block: a blocking
// delivery would stall every other session on the same agent process.
func (s *state) deliver(u acp.SessionUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.updates == nil {
		// No turn in flight; the agent sent an update out of band. Dropping it
		// is correct: there is no consumer to attribute it to.
		return
	}
	select {
	case s.updates <- u:
	default:
		if s.abortErr == nil {
			s.abortErr = errBackpressure
		}
		if s.cancelTurn != nil {
			s.cancelTurn()
		}
	}
}

// busy reports whether a turn is currently running on this session.
func (s *state) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates != nil
}

// touch records activity, for the idle reaper.
func (s *state) touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

// idleFor reports how long the session has been unused.
func (s *state) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastUsed)
}

// run sends one prompt and streams the turn's updates to onUpdate.
//
// The prompt request runs on its own goroutine so updates can be delivered
// while the turn is still in flight; without that, streaming would only start
// once the agent had already finished.
func (s *state) run(ctx context.Context, req Request, onUpdate func(acp.SessionUpdate) error) (string, error) {
	content := req.Parts
	if len(content) == 0 {
		content = []acp.ContentBlock{acp.TextBlock(req.Prompt)}
	}

	ch := make(chan acp.SessionUpdate, updateBuffer)
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	s.mu.Lock()
	s.updates = ch
	s.abortErr = nil
	s.cancelTurn = cancelTurn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.updates = nil
		s.cancelTurn = nil
		s.lastUsed = time.Now()
		s.mu.Unlock()
	}()

	type outcome struct {
		raw json.RawMessage
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, err := s.client.Request(turnCtx, acp.MethodSessionPrompt, acp.PromptRequest{
			SessionID: s.id,
			Prompt:    content,
		})
		done <- outcome{raw: raw, err: err}
	}()

	var res outcome
loop:
	for {
		select {
		case u := <-ch:
			if err := onUpdate(u); err != nil {
				s.cancel()
				return "", fmt.Errorf("session: deliver update: %w", err)
			}
		case res = <-done:
			break loop
		case <-turnCtx.Done():
			s.cancel()
			return "", turnCtx.Err()
		}
	}

	// The agent's response follows the turn's updates on the wire, but the
	// updates may still sit in the queue; flush them before reporting.
drain:
	for {
		select {
		case u := <-ch:
			if err := onUpdate(u); err != nil {
				return "", fmt.Errorf("session: deliver update: %w", err)
			}
		default:
			break drain
		}
	}

	if res.err != nil {
		return "", res.err
	}
	if err := s.abortError(); err != nil {
		return "", err
	}

	var resp acp.PromptResponse
	if err := json.Unmarshal(res.raw, &resp); err != nil {
		return "", fmt.Errorf("session: decode prompt response: %w", err)
	}
	return resp.StopReason, nil
}

// cancel asks the agent to stop the current turn.
func (s *state) cancel() {
	_ = s.client.Notify(acp.MethodSessionCancel, acp.CancelNotification{SessionID: s.id})
}

// abortError returns the recorded abort cause, if any.
func (s *state) abortError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortErr
}
