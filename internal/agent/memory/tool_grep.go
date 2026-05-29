package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"charm.land/fantasy"
)

const MemoryGrepToolName = "memory_grep"

const maxGrepContextLines = 10

type memoryGrepParams struct {
	ID           string `json:"id" description:"Memory reference ID returned by a previous tool call (e.g. mem_42)"`
	Pattern      string `json:"pattern" description:"Regular expression to search for within the stored content"`
	ContextLines int    `json:"context_lines,omitempty" description:"Lines of context to show around each match, max 10 (default: 2)"`
}

// NewMemoryGrepTool returns a tool that searches within a stored memory
// reference by regex pattern, returning only matching lines with context.
// Prefer this over repeated memory_scroll calls to keep responses small.
func NewMemoryGrepTool(store Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemoryGrepToolName,
		"Search within a stored memory reference by regex pattern. Returns only matching lines with surrounding context instead of paging through all content. Use this instead of memory_scroll when looking for specific information.",
		func(ctx context.Context, params memoryGrepParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ID == "" {
				return fantasy.NewTextErrorResponse("id is required"), nil
			}
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			re, err := regexp.Compile(params.Pattern)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid pattern: %s", err)), nil
			}

			entry, err := store.Get(ctx, params.ID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("no memory entry found with id %q", params.ID)), nil
				}
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to retrieve memory entry: %s", err)), nil
			}

			contextLines := params.ContextLines
			if contextLines <= 0 {
				contextLines = 2
			}
			if contextLines > maxGrepContextLines {
				contextLines = maxGrepContextLines
			}

			lines := entry.Lines
			// Collect indices of matching lines.
			var matchIndices []int
			for i, line := range lines {
				if re.MatchString(line) {
					matchIndices = append(matchIndices, i)
				}
			}

			if len(matchIndices) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("[%s | grep: %q | 0 matches]", params.ID, params.Pattern)), nil
			}

			const maxMatches = 100
			truncated := len(matchIndices) > maxMatches
			if truncated {
				matchIndices = matchIndices[:maxMatches]
			}

			// Build merged windows to avoid duplicating lines in overlapping ranges.
			type window struct{ start, end int }
			var windows []window
			for _, idx := range matchIndices {
				s := idx - contextLines
				if s < 0 {
					s = 0
				}
				e := idx + contextLines
				if e >= len(lines) {
					e = len(lines) - 1
				}
				if len(windows) > 0 && s <= windows[len(windows)-1].end+1 {
					// Merge with previous window.
					if e > windows[len(windows)-1].end {
						windows[len(windows)-1].end = e
					}
				} else {
					windows = append(windows, window{s, e})
				}
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "[%s | source: %s | grep: %q | %d match(es)]\n", params.ID, entry.Source, params.Pattern, len(matchIndices))
			if truncated {
				sb.WriteString("(results truncated to 100 matches)\n")
			}
			sb.WriteString("\n")

			for wi, w := range windows {
				if wi > 0 {
					sb.WriteString("...\n")
				}
				for i := w.start; i <= w.end; i++ {
					if re.MatchString(lines[i]) {
						fmt.Fprintf(&sb, "L%d* %s\n", i+1, lines[i])
					} else {
						fmt.Fprintf(&sb, "L%d  %s\n", i+1, lines[i])
					}
				}
			}

			return fantasy.NewTextResponse(sb.String()), nil
		},
	)
}
