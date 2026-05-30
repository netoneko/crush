package memory

import (
	"context"
	"errors"
	"fmt"

	"charm.land/fantasy"
)

const MemoryScrollToolName = "memory_scroll"

const maxScrollLimit = 50

type memoryScrollParams struct {
	ID     string `json:"id" description:"Memory reference ID returned by a previous tool call (e.g. mem_42)"`
	Offset int    `json:"offset,omitempty" description:"Line offset to start reading from, 0-indexed (default: 0)"`
	Limit  int    `json:"limit,omitempty" description:"Number of lines to return, max 50 (default: 50)"`
}

// NewMemoryScrollTool returns a tool that reads a window of lines from a
// stored memory reference.
func NewMemoryScrollTool(store Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemoryScrollToolName,
		"Read a window of lines from a large tool result that was stored in memory. Use memory_list to see available references. Use offset and limit to page through the content.",
		func(ctx context.Context, params memoryScrollParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ID == "" {
				return fantasy.NewTextErrorResponse("id is required"), nil
			}

			limit := params.Limit
			if limit <= 0 {
				limit = 50
			}
			if limit > maxScrollLimit {
				limit = maxScrollLimit
			}

			entry, err := store.Get(ctx, params.ID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("no memory entry found with id %q", params.ID)), nil
				}
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to retrieve memory entry: %s", err)), nil
			}

			window, err := store.Scroll(ctx, params.ID, params.Offset, limit)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to scroll memory entry: %s", err)), nil
			}

			total := len(entry.Lines)
			end := params.Offset + limit
			if end > total {
				end = total
			}

			header := fmt.Sprintf("[%s | source: %s | lines %d–%d of %d]\n\n",
				params.ID, entry.Source, params.Offset+1, end, total)

			return fantasy.NewTextResponse(header + window), nil
		},
	)
}
