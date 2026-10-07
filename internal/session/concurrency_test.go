package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/session"
)

// TestConcurrentConversationsDoNotInterfere pins the property the gateway's
// session model rests on: one agent process serves many sessions, and two turns
// on different conversations must not see each other's output.
//
// The first turn is held mid-flight by its own update callback, so the second
// is guaranteed to start while the first is still streaming; the agent's chunks
// are spaced out so that is a real interleaving rather than a lucky one.
//
// Two turns on the *same* conversation are not covered here, because they are
// not safe: a session has one update consumer, so the second turn takes the
// first one's stream and the first turn returns without its answer.
func TestConcurrentConversationsDoNotInterfere(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_ECHO":           "1",
		"FAKE_AGENT_CHUNKS":         "2",
		"FAKE_AGENT_CHUNK_DELAY_MS": "200",
	})
	ctx := context.Background()

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once

	var mu sync.Mutex
	var first string

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := m.Prompt(ctx, session.Request{
			Model: "fake", ConversationID: "one", Prompt: "AAAA",
		}, func(u acp.SessionUpdate) error {
			holdOnce.Do(func() {
				close(inFlight)
				<-release
			})
			if u.Content != nil {
				mu.Lock()
				first += u.Content.Text
				mu.Unlock()
			}
			return nil
		})
		if err != nil {
			t.Errorf("first turn: %v", err)
		}
	}()

	<-inFlight

	var second string
	if _, err := m.Prompt(ctx, session.Request{
		Model: "fake", ConversationID: "two", Prompt: "BBBB",
	}, func(u acp.SessionUpdate) error {
		if u.Content != nil {
			second += u.Content.Text
		}
		return nil
	}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if first != "AAAA" {
		t.Fatalf("the held turn received %q, want %q", first, "AAAA")
	}
	if second != "BBBB" {
		t.Fatalf("the concurrent turn received %q, want %q", second, "BBBB")
	}
}

// TestConcurrentTurnsOnOneConversationAreSerialised covers the case that used
// to corrupt: two turns on the same conversation. A session has one update
// consumer, so the second turn must wait for the first rather than take its
// stream — otherwise the first returns without its answer and the second
// receives text that belongs to the first.
//
// The first turn is held mid-stream, so the second is guaranteed to arrive
// while the first is running; the first is released only after the second has
// had time to queue, which is what the fix has to do.
func TestConcurrentTurnsOnOneConversationAreSerialised(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_ECHO":           "1",
		"FAKE_AGENT_CHUNKS":         "2",
		"FAKE_AGENT_CHUNK_DELAY_MS": "200",
	})
	ctx := context.Background()

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once

	var mu sync.Mutex
	var first string

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := m.Prompt(ctx, session.Request{
			Model: "fake", ConversationID: "shared", Prompt: "AAAA",
		}, func(u acp.SessionUpdate) error {
			holdOnce.Do(func() {
				close(inFlight)
				<-release
			})
			if u.Content != nil {
				mu.Lock()
				first += u.Content.Text
				mu.Unlock()
			}
			return nil
		})
		if err != nil {
			t.Errorf("first turn: %v", err)
		}
	}()

	<-inFlight

	secondDone := make(chan string, 1)
	go func() {
		var text string
		_, err := m.Prompt(ctx, session.Request{
			Model: "fake", ConversationID: "shared", Prompt: "BBBB",
		}, func(u acp.SessionUpdate) error {
			if u.Content != nil {
				text += u.Content.Text
			}
			return nil
		})
		if err != nil {
			t.Errorf("second turn: %v", err)
		}
		secondDone <- text
	}()

	// Let the second turn reach the session and queue behind the first.
	time.Sleep(300 * time.Millisecond)
	close(release)

	wg.Wait()
	second := <-secondDone

	mu.Lock()
	defer mu.Unlock()
	if first != "AAAA" {
		t.Fatalf("the held turn received %q, want %q: the second turn took its stream", first, "AAAA")
	}
	if second != "BBBB" {
		t.Fatalf("the queued turn received %q, want %q", second, "BBBB")
	}
}

// TestQueuedTurnHonoursItsContext: waiting for the turn in flight must not
// outlive the request that is waiting. A client that gave up must not leave a
// goroutine queued behind a turn nobody is reading.
func TestQueuedTurnHonoursItsContext(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{
		"FAKE_AGENT_ECHO":           "1",
		"FAKE_AGENT_CHUNKS":         "2",
		"FAKE_AGENT_CHUNK_DELAY_MS": "200",
	})

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := m.Prompt(context.Background(), session.Request{
			Model: "fake", ConversationID: "shared", Prompt: "AAAA",
		}, func(u acp.SessionUpdate) error {
			holdOnce.Do(func() {
				close(inFlight)
				<-release
			})
			return nil
		})
		if err != nil {
			t.Errorf("first turn: %v", err)
		}
	}()

	<-inFlight

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := m.Prompt(ctx, session.Request{
		Model: "fake", ConversationID: "shared", Prompt: "BBBB",
	}, noop)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued turn error = %v, want deadline exceeded", err)
	}

	close(release)
	wg.Wait()
}
