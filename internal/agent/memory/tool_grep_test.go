package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func storeEntry(t *testing.T, store Store, source, content string) string {
	t.Helper()
	id, err := store.Store(context.Background(), source, KindGeneric, content)
	require.NoError(t, err)
	return id
}

func runMemoryGrep(t *testing.T, store Store, id, pattern string, contextLines int) fantasy.ToolResponse {
	t.Helper()
	tool := NewMemoryGrepTool(store)
	params := memoryGrepParams{ID: id, Pattern: pattern, ContextLines: contextLines}
	input := mustMarshal(t, params)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: MemoryGrepToolName, Input: input})
	require.NoError(t, err)
	return resp
}

func TestMemoryGrep_MatchingLines(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	content := "line one\nfind me here\nline three\nfind me too\nline five"
	id := storeEntry(t, store, "bash", content)

	resp := runMemoryGrep(t, store, id, "find me", 0)

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "find me here")
	require.Contains(t, resp.Content, "find me too")
	require.Contains(t, resp.Content, "2 match")
}

func TestMemoryGrep_NoMatches(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	id := storeEntry(t, store, "bash", "hello world\ngoodbye world")

	resp := runMemoryGrep(t, store, id, "notfound", 0)

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "0 match")
}

func TestMemoryGrep_ContextLines(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	content := "before1\nbefore2\nMATCH\nafter1\nafter2"
	id := storeEntry(t, store, "bash", content)

	resp := runMemoryGrep(t, store, id, "MATCH", 2)

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "before1")
	require.Contains(t, resp.Content, "before2")
	require.Contains(t, resp.Content, "MATCH")
	require.Contains(t, resp.Content, "after1")
	require.Contains(t, resp.Content, "after2")
}

func TestMemoryGrep_MissingID(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	tool := NewMemoryGrepTool(store)
	input := mustMarshal(t, memoryGrepParams{Pattern: "foo"})
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: MemoryGrepToolName, Input: input})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "id is required")
}

func TestMemoryGrep_MissingPattern(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	id := storeEntry(t, store, "bash", "hello")
	tool := NewMemoryGrepTool(store)
	input := mustMarshal(t, memoryGrepParams{ID: id})
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: MemoryGrepToolName, Input: input})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "pattern is required")
}

func TestMemoryGrep_InvalidPattern(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	id := storeEntry(t, store, "bash", "hello")

	resp := runMemoryGrep(t, store, id, "[invalid(", 0)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "invalid pattern")
}

func TestMemoryGrep_UnknownID(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	tool := NewMemoryGrepTool(store)
	input := mustMarshal(t, memoryGrepParams{ID: "mem_999", Pattern: "foo"})
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: MemoryGrepToolName, Input: input})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "mem_999")
}

func TestMemoryGrep_ContextLinesClampedToMax(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	content := strings.Repeat("line\n", 50) + "MATCH\n" + strings.Repeat("line\n", 50)
	id := storeEntry(t, store, "bash", content)

	// Request 999 context lines — should be clamped to maxGrepContextLines (10).
	resp := runMemoryGrep(t, store, id, "MATCH", 999)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "MATCH")
}

func TestMemoryGrep_MergesOverlappingWindows(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	// Two matches 3 lines apart with context=2 — windows should merge, no "..." separator.
	content := "a\nb\nMATCH1\nd\nMATCH2\nf\ng"
	id := storeEntry(t, store, "bash", content)

	resp := runMemoryGrep(t, store, id, "MATCH", 2)

	require.False(t, resp.IsError)
	// No separator between merged windows.
	require.NotContains(t, resp.Content, "...\n")
}

func TestWrapWithMemory_HintIncludesMemoryGrep(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	cfg := WrapConfig{HardLimit: 10, Overspill: 0.0, PreviewLines: 2}

	content := strings.Repeat("x", 50)
	tool := &mockTool{name: "bash", content: content}
	wrapped := WrapWithMemory([]fantasy.AgentTool{tool}, store, cfg)

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "bash"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "memory_grep")
}
