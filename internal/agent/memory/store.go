// Package memory provides an in-process store for large tool results.
// Phase 1: InMemoryStore (map, lost on process exit).
// Phase 2: SQLite backend (cross-session persistence) — same interface.
package memory

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested entry does not exist in the store.
var ErrNotFound = errors.New("memory: entry not found")

// Kind classifies stored entries for future SQL table routing.
type Kind string

const (
	KindGeneric Kind = "generic"
	KindLogs    Kind = "logs"
	KindCode    Kind = "code"
)

// Entry holds a stored tool result.
type Entry struct {
	ID        string
	Kind      Kind
	Source    string    // tool name that produced this entry
	Lines     []string  // full content split by newline
	CreatedAt time.Time
}

// Store is the abstraction for tool-result memory.
// Both the in-memory and the upcoming SQLite backend satisfy this interface.
type Store interface {
	// Store persists content and returns the assigned reference ID.
	Store(ctx context.Context, source string, kind Kind, content string) (id string, err error)
	// Get retrieves an entry by its reference ID.
	Get(ctx context.Context, id string) (Entry, error)
	// Scroll returns lines [offset, offset+limit) from the stored entry joined
	// by newlines. Clamps safely to the available range.
	Scroll(ctx context.Context, id string, offset, limit int) (string, error)
	// List returns all stored entries sorted by CreatedAt ascending.
	List(ctx context.Context) ([]Entry, error)
}
