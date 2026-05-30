package memory

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"
)

// mockTool is a minimal AgentTool that returns a fixed response.
type mockTool struct {
	name    string
	content string
	opts    fantasy.ProviderOptions
}

func (m *mockTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: m.name, Description: "mock"}
}
func (m *mockTool) ProviderOptions() fantasy.ProviderOptions     { return m.opts }
func (m *mockTool) SetProviderOptions(o fantasy.ProviderOptions) { m.opts = o }
func (m *mockTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse(m.content), nil
}

func TestWrapWithMemory_SmallResponse_Passthrough(t *testing.T) {
	store := NewInMemoryStore()
	cfg := WrapConfig{HardLimit: 100, Overspill: 0.20, PreviewLines: 3}

	tool := &mockTool{name: "bash", content: "hello world"}
	wrapped := WrapWithMemory([]fantasy.AgentTool{tool}, store, cfg)

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello world" {
		t.Errorf("expected passthrough, got %q", resp.Content)
	}

	entries, _ := store.List(context.Background())
	if len(entries) != 0 {
		t.Errorf("expected no stored entries for small response, got %d", len(entries))
	}
}

func TestWrapWithMemory_WithinOverspill_Passthrough(t *testing.T) {
	store := NewInMemoryStore()
	// HardLimit=100, Overspill=0.20 → effective limit = 120 bytes
	cfg := WrapConfig{HardLimit: 100, Overspill: 0.20, PreviewLines: 3}

	// 110 bytes — above HardLimit but within overspill window
	content := strings.Repeat("x", 110)
	tool := &mockTool{name: "bash", content: content}
	wrapped := WrapWithMemory([]fantasy.AgentTool{tool}, store, cfg)

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != content {
		t.Error("expected passthrough for response within overspill window")
	}

	entries, _ := store.List(context.Background())
	if len(entries) != 0 {
		t.Errorf("expected no stored entries within overspill window, got %d", len(entries))
	}
}

func TestWrapWithMemory_LargeResponse_Stored(t *testing.T) {
	store := NewInMemoryStore()
	cfg := WrapConfig{HardLimit: 100, Overspill: 0.20, PreviewLines: 3}

	// 200 bytes — well above effective limit of 120
	content := strings.Repeat("a", 200)
	tool := &mockTool{name: "bash", content: content}
	wrapped := WrapWithMemory([]fantasy.AgentTool{tool}, store, cfg)

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "mem_1") {
		t.Errorf("expected reference ID in response, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "memory_scroll") {
		t.Errorf("expected memory_scroll hint in response, got %q", resp.Content)
	}

	entries, _ := store.List(context.Background())
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored entry, got %d", len(entries))
	}
	if entries[0].Source != "bash" {
		t.Errorf("stored source: want %q, got %q", "bash", entries[0].Source)
	}
}

func TestWrapWithMemory_CustomConfig(t *testing.T) {
	store := NewInMemoryStore()
	// Very tight limit, 0% overspill
	cfg := WrapConfig{HardLimit: 10, Overspill: 0.0, PreviewLines: 2}

	content := strings.Repeat("z", 11) // just over limit
	tool := &mockTool{name: "grep", content: content}
	wrapped := WrapWithMemory([]fantasy.AgentTool{tool}, store, cfg)

	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "grep"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "mem_1") {
		t.Errorf("expected ref ID in response, got %q", resp.Content)
	}
}

func TestWrapWithMemory_ErrorResponse_Passthrough(t *testing.T) {
	store := NewInMemoryStore()
	cfg := DefaultWrapConfig()

	// Error responses should never be stored regardless of size.
	errTool := &errorTool{content: strings.Repeat("e", 100000)}
	wrapped := WrapWithMemory([]fantasy.AgentTool{errTool}, store, cfg)

	resp, _ := wrapped[0].Run(context.Background(), fantasy.ToolCall{Name: "bash"})
	if !resp.IsError {
		t.Error("expected error response to pass through")
	}

	entries, _ := store.List(context.Background())
	if len(entries) != 0 {
		t.Errorf("expected no stored entries for error response, got %d", len(entries))
	}
}

type errorTool struct {
	content string
	opts    fantasy.ProviderOptions
}

func (e *errorTool) Info() fantasy.ToolInfo                       { return fantasy.ToolInfo{Name: "bash"} }
func (e *errorTool) ProviderOptions() fantasy.ProviderOptions     { return e.opts }
func (e *errorTool) SetProviderOptions(o fantasy.ProviderOptions) { e.opts = o }
func (e *errorTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextErrorResponse(e.content), nil
}
