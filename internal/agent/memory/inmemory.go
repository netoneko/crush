package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type inMemoryStore struct {
	mu      sync.RWMutex
	entries map[string]Entry
	counter atomic.Int64
}

// NewInMemoryStore returns a Store backed by an in-process map.
// All data is lost when the process exits.
func NewInMemoryStore() Store {
	return &inMemoryStore{
		entries: make(map[string]Entry),
	}
}

func (s *inMemoryStore) Store(_ context.Context, source string, kind Kind, content string) (string, error) {
	id := fmt.Sprintf("mem_%d", s.counter.Add(1))
	entry := Entry{
		ID:        id,
		Kind:      kind,
		Source:    source,
		Lines:     strings.Split(content, "\n"),
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.entries[id] = entry
	s.mu.Unlock()
	return id, nil
}

func (s *inMemoryStore) Get(_ context.Context, id string) (Entry, error) {
	s.mu.RLock()
	e, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (s *inMemoryStore) Scroll(_ context.Context, id string, offset, limit int) (string, error) {
	s.mu.RLock()
	e, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}

	total := len(e.Lines)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return fmt.Sprintf("[end of entry — total %d lines]", total), nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return strings.Join(e.Lines[offset:end], "\n"), nil
}

func (s *inMemoryStore) List(_ context.Context) ([]Entry, error) {
	s.mu.RLock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	s.mu.RUnlock()

	// stable sort by creation order (IDs are monotonically assigned)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}
