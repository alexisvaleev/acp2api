package acp

import "sync"

// boundedBuffer is a concurrency-safe writer that retains only the most recent
// N bytes. Agent stderr is unbounded and mostly noise, but the tail is what
// matters when a session dies: it usually carries the real failure reason.
type boundedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

// newBoundedBuffer returns a buffer that keeps at most max trailing bytes.
func newBoundedBuffer(max int) *boundedBuffer {
	if max <= 0 {
		max = 1
	}
	return &boundedBuffer{max: max}
}

// Write appends p, discarding the oldest bytes beyond the cap. It never fails.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

// String returns the retained tail as a string.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Len reports how many bytes are retained.
func (b *boundedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}
