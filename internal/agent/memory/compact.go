package memory

import (
	"charm.land/fantasy"
)

// compactTool wraps an AgentTool and substitutes a shorter description.
type compactTool struct {
	fantasy.AgentTool
	desc string
}

func (t compactTool) Info() fantasy.ToolInfo {
	info := t.AgentTool.Info()
	info.Description = t.desc
	return info
}

// CompactMemoryListTool wraps memory_list with a one-line description.
func CompactMemoryListTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return compactTool{AgentTool: tool, desc: "memory_list() → stored refs with IDs, source, size, preview."}
}

// CompactMemoryScrollTool wraps memory_scroll with a one-line description.
func CompactMemoryScrollTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return compactTool{AgentTool: tool, desc: "memory_scroll(id, offset, limit) → lines window. Prefer memory_grep for targeted lookups."}
}

// CompactMemoryGrepTool wraps memory_grep with a one-line description.
func CompactMemoryGrepTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return compactTool{AgentTool: tool, desc: "memory_grep(id, pattern, context_lines) → matching lines with context. Use instead of memory_scroll."}
}

// ApplyCompact wraps memory tools that have compact variants, returning
// others unchanged.
func ApplyCompact(toolList []fantasy.AgentTool) []fantasy.AgentTool {
	wrappers := map[string]func(fantasy.AgentTool) fantasy.AgentTool{
		MemoryListToolName:   CompactMemoryListTool,
		MemoryScrollToolName: CompactMemoryScrollTool,
		MemoryGrepToolName:   CompactMemoryGrepTool,
	}
	result := make([]fantasy.AgentTool, len(toolList))
	for i, t := range toolList {
		if wrap, ok := wrappers[t.Info().Name]; ok {
			result[i] = wrap(t)
		} else {
			result[i] = t
		}
	}
	return result
}
