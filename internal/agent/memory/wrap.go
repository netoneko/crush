package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
)

// ToolStrategy controls how a specific tool's oversized output is handled.
type ToolStrategy int

const (
	// StrategyStore stores the full result and returns a compact reference to
	// the model. The model can query it via memory_scroll or memory_grep.
	// This is the default.
	StrategyStore ToolStrategy = iota

	// StrategyRefuse rejects the oversized result and tells the model to
	// re-call the tool with smaller parameters (e.g. offset/limit for view).
	// Useful for tools that already support pagination — storing is redundant.
	StrategyRefuse
)

// WrapConfig controls when tool results are moved into the memory store.
type WrapConfig struct {
	// HardLimit is the byte length above which results are candidates for
	// memory storage. Default: 8192 (8 KB).
	HardLimit int
	// Overspill is the fraction of HardLimit that is tolerated inline.
	// Results whose byte length falls within [HardLimit, HardLimit*(1+Overspill)]
	// are passed through unchanged; only results strictly above that threshold
	// are stored. Default: 0.20 (20 %).
	Overspill float64
	// PreviewLines is the number of lines included in the truncated reference
	// response shown to the model. Default: 10.
	PreviewLines int
	// ToolStrategies overrides the handling strategy per tool name.
	// Tools not listed use StrategyStore.
	ToolStrategies map[string]ToolStrategy
}

// DefaultWrapConfig returns sensible defaults: 8 KB hard limit, 20 % overspill,
// 10 preview lines, store strategy for all tools.
func DefaultWrapConfig() WrapConfig {
	return WrapConfig{
		HardLimit:    8192,
		Overspill:    0.20,
		PreviewLines: 10,
	}
}

func (c WrapConfig) strategyFor(toolName string) ToolStrategy {
	if c.ToolStrategies == nil {
		return StrategyStore
	}
	if s, ok := c.ToolStrategies[toolName]; ok {
		return s
	}
	return StrategyStore
}

// WrapWithMemory returns a copy of tools where every tool's Run output is
// intercepted: when the text response exceeds the configured threshold the
// full content is stored in store and a compact reference is returned to the
// model instead.
func WrapWithMemory(tools []fantasy.AgentTool, store Store, cfg WrapConfig) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(tools))
	for i, t := range tools {
		out[i] = &memoryTool{inner: t, store: store, cfg: cfg}
	}
	return out
}

type memoryTool struct {
	inner fantasy.AgentTool
	store Store
	cfg   WrapConfig
}

func (m *memoryTool) Info() fantasy.ToolInfo                   { return m.inner.Info() }
func (m *memoryTool) ProviderOptions() fantasy.ProviderOptions { return m.inner.ProviderOptions() }
func (m *memoryTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	m.inner.SetProviderOptions(opts)
}

func (m *memoryTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := m.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}

	// Only intercept non-error text responses.
	if resp.IsError || resp.Content == "" {
		return resp, nil
	}

	effectiveLimit := float64(m.cfg.HardLimit) * (1 + m.cfg.Overspill)
	slog.Debug("memory wrap: tool result", "tool", call.Name, "bytes", len(resp.Content), "effective_limit", int(effectiveLimit))
	if float64(len(resp.Content)) <= effectiveLimit {
		return resp, nil
	}

	switch m.cfg.strategyFor(call.Name) {
	case StrategyRefuse:
		slog.Debug("memory wrap: refusing large result", "tool", call.Name, "bytes", len(resp.Content))
		msg := fmt.Sprintf(
			"[Result too large: %d bytes exceeds %d byte limit]\n"+
				"Re-call %s with a narrower range. For view, use offset and limit parameters to read in sections.",
			len(resp.Content), m.cfg.HardLimit, call.Name,
		)
		return fantasy.NewTextErrorResponse(msg), nil

	default: // StrategyStore
		id, storeErr := m.store.Store(ctx, call.Name, KindGeneric, resp.Content)
		if storeErr != nil {
			slog.Warn("memory wrap: store failed", "tool", call.Name, "error", storeErr)
			// Storage failure is non-fatal: return the original (large) response.
			return resp, nil
		}
		slog.Debug("memory wrap: stored reference", "tool", call.Name, "id", id, "bytes", len(resp.Content))

		lines := strings.Split(resp.Content, "\n")
		previewEnd := m.cfg.PreviewLines
		if previewEnd > len(lines) {
			previewEnd = len(lines)
		}
		preview := strings.Join(lines[:previewEnd], "\n")

		summary := fmt.Sprintf(
			"[Large result stored as memory reference %s]\nSource: %s | Kind: %s | Bytes: %d | Lines: %d\n\nPreview (first %d lines):\n---\n%s\n---\n\nUse memory_grep(id=%q, pattern=\"...\") to search or memory_scroll(id=%q, offset=0, limit=50) to page through the content.",
			id, call.Name, string(KindGeneric), len(resp.Content), len(lines),
			previewEnd, preview, id, id,
		)

		return fantasy.NewTextResponse(summary), nil
	}
}
