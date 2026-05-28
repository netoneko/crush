package memory

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

const MemoryListToolName = "memory_list"

type memoryListParams struct{}

// NewMemoryListTool returns a tool that lists all memory references for the
// current session.
func NewMemoryListTool(store Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemoryListToolName,
		"List all large tool results that have been stored in memory during this session. Returns reference IDs, source tool, size, and a short preview. Use memory_scroll to read the full content of any entry.",
		func(ctx context.Context, _ memoryListParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			entries, err := store.List(ctx)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to list memory entries: %s", err)), nil
			}

			if len(entries) == 0 {
				return fantasy.NewTextResponse("No results stored in memory yet."), nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "%-10s %-20s %-10s %-8s %s\n", "ID", "Source", "Kind", "Lines", "Preview")
			fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 80))
			for _, e := range entries {
				preview := ""
				if len(e.Lines) > 0 {
					preview = e.Lines[0]
					if len(preview) > 40 {
						preview = preview[:40] + "…"
					}
				}
				fmt.Fprintf(&sb, "%-10s %-20s %-10s %-8d %s\n",
					e.ID, truncate(e.Source, 20), string(e.Kind), len(e.Lines), preview)
			}

			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
