package handler

import (
	"sync"
	"time"

	"github.com/quonaro/acp2api/internal/openai"
)

// defaultResponseLimit bounds the in-memory response store. The OpenAI API
// keeps responses server-side; this gateway keeps a bounded window so a long
// running process cannot grow without limit.
const defaultResponseLimit = 256

// storedResponse is what the gateway remembers about a response so it can be
// retrieved and resumed.
type storedResponse struct {
	response *openai.Response
	// conversationID is the ACP conversation that produced the response, so
	// previous_response_id can resume the same agent session.
	conversationID string
	created        time.Time
}

// responseStore is a bounded, in-memory, insertion-ordered store of responses.
type responseStore struct {
	mu      sync.Mutex
	entries map[string]*storedResponse
	order   []string
	limit   int
}

// newResponseStore creates a store holding at most limit responses.
func newResponseStore(limit int) *responseStore {
	if limit <= 0 {
		limit = defaultResponseLimit
	}
	return &responseStore{
		entries: make(map[string]*storedResponse),
		limit:   limit,
	}
}

// put records a response, evicting the oldest entry when the store is full.
func (s *responseStore) put(id, conversationID string, response *openai.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[id]; !exists {
		s.order = append(s.order, id)
	}
	s.entries[id] = &storedResponse{
		response:       response,
		conversationID: conversationID,
		created:        time.Now(),
	}

	for len(s.order) > s.limit {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
}

// get returns a stored response.
func (s *responseStore) get(id string) (*storedResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	return entry, ok
}

// delete removes a response, reporting whether it was there.
func (s *responseStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[id]; !ok {
		return false
	}
	delete(s.entries, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true
}
