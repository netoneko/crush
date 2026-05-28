package memory

import (
	"context"
	"sync"
	"testing"
)

func TestInMemoryStore_StoreAndGet(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	id, err := s.Store(ctx, "bash", KindLogs, "line1\nline2\nline3")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	e, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.Source != "bash" {
		t.Errorf("Source: want %q, got %q", "bash", e.Source)
	}
	if e.Kind != KindLogs {
		t.Errorf("Kind: want %q, got %q", KindLogs, e.Kind)
	}
	if len(e.Lines) != 3 {
		t.Errorf("Lines: want 3, got %d", len(e.Lines))
	}
}

func TestInMemoryStore_GetNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, err := s.Get(context.Background(), "mem_99")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInMemoryStore_Scroll(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	content := "a\nb\nc\nd\ne"
	id, _ := s.Store(ctx, "tool", KindGeneric, content)

	// Basic window.
	got, err := s.Scroll(ctx, id, 1, 3)
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	want := "b\nc\nd"
	if got != want {
		t.Errorf("Scroll(1,3): want %q, got %q", want, got)
	}

	// Clamped past end.
	got, err = s.Scroll(ctx, id, 3, 100)
	if err != nil {
		t.Fatalf("Scroll clamp: %v", err)
	}
	want = "d\ne"
	if got != want {
		t.Errorf("Scroll clamp: want %q, got %q", want, got)
	}

	// Offset past end.
	got, err = s.Scroll(ctx, id, 100, 10)
	if err != nil {
		t.Fatalf("Scroll past end: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty result for offset past end")
	}
}

func TestInMemoryStore_List(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()

	s.Store(ctx, "bash", KindLogs, "x")
	s.Store(ctx, "grep", KindCode, "y")

	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("List: want 2, got %d", len(entries))
	}
	// Should be sorted oldest-first.
	if entries[0].Source != "bash" {
		t.Errorf("first entry should be bash, got %q", entries[0].Source)
	}
}

func TestInMemoryStore_ConcurrentSafety(t *testing.T) {
	s := NewInMemoryStore()
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, _ := s.Store(ctx, "tool", KindGeneric, "data")
			s.Get(ctx, id)
			s.Scroll(ctx, id, 0, 10)
			_ = i
		}(i)
	}
	wg.Wait()

	entries, _ := s.List(ctx)
	if len(entries) != 50 {
		t.Errorf("want 50 entries, got %d", len(entries))
	}
}
