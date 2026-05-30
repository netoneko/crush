package tools

import (
	"fmt"
	"strings"

	"charm.land/fantasy"
)

// compactTool wraps an AgentTool and substitutes a shorter description.
// Used when compact_tools is enabled to reduce tool schema token cost.
type compactTool struct {
	fantasy.AgentTool
	desc string
}

func (t compactTool) Info() fantasy.ToolInfo {
	info := t.AgentTool.Info()
	info.Description = t.desc
	return info
}

func withCompact(tool fantasy.AgentTool, desc string) fantasy.AgentTool {
	return compactTool{AgentTool: tool, desc: desc}
}

// CompactBashTool wraps a bash tool with a shorter description that keeps
// all behavioral rules (banned commands, background execution, file-editing
// discouragement) but drops cross-platform notes, git commit format, and PR
// format blocks.
func CompactBashTool(tool fantasy.AgentTool) fantasy.AgentTool {
	bannedStr := strings.Join(bannedCommands, ", ")
	rgNote := ""
	if getRg() != "" {
		rgNote = " rg available; prefer over grep."
	}
	desc := fmt.Sprintf(
		"bash(command, working_dir, run_in_background) → output or shell_id. "+
			"File editing highly discouraged — use edit, write, multiedit instead. "+
			"Banned: %s. "+
			"Each call is an independent shell; no state persists between calls. "+
			"Long commands (>1 min) auto-background. "+
			"Use run_in_background=true (never &) for servers/watchers; use job_output/job_kill to manage.%s",
		bannedStr, rgNote,
	)
	return withCompact(tool, desc)
}

// CompactGrepTool wraps grep with a one-line description.
func CompactGrepTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "grep(pattern, path, include) → matching file paths and lines; respects .gitignore.")
}

// CompactLsTool wraps ls with a one-line description.
func CompactLsTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "ls(path) → directory tree.")
}

// CompactViewTool wraps view with a one-line description.
func CompactViewTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "view(path, offset, limit) → file contents with line numbers.")
}

// CompactEditTool wraps edit with a one-line description.
func CompactEditTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "edit(path, old_str, new_str) → find-and-replace; use write for large edits.")
}

// CompactWriteTool wraps write with a one-line description.
func CompactWriteTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "write(path, content) → create/overwrite file; use edit for surgical changes.")
}

// CompactMultiEditTool wraps multiedit with a one-line description.
func CompactMultiEditTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "multiedit(path, edits[]) → multiple find-and-replaces in one pass.")
}

// CompactTodosTool wraps todos with a one-line description.
func CompactTodosTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "todos(action, ...) → task list (pending/in_progress/completed); one in_progress at a time.")
}

// CompactJobOutputTool wraps job_output with a one-line description.
func CompactJobOutputTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "job_output(id, wait) → stdout/stderr from background shell.")
}

// CompactJobKillTool wraps job_kill with a one-line description.
func CompactJobKillTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "job_kill(id) → terminates background shell.")
}

// CompactFileWriteTool wraps file_write with a one-line description.
func CompactFileWriteTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "file_write(path, content) → writes file. No shell escaping; parent dirs created automatically.")
}

// CompactFileEditTool wraps file_edit with a one-line description.
func CompactFileEditTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "file_edit(path, old_str, new_str) → replaces first occurrence. Use file_write for full rewrites.")
}

// CompactFileGrepTool wraps file_grep with a one-line description.
func CompactFileGrepTool(tool fantasy.AgentTool) fantasy.AgentTool {
	return withCompact(tool, "file_grep(pattern, path, include) → matching file paths and line numbers.")
}

// ApplyCompact wraps a slice of tools, replacing descriptions for tools that
// have a compact variant. Tools without a compact variant are returned as-is.
func ApplyCompact(toolList []fantasy.AgentTool) []fantasy.AgentTool {
	wrappers := map[string]func(fantasy.AgentTool) fantasy.AgentTool{
		BashToolName:      CompactBashTool,
		GrepToolName:      CompactGrepTool,
		LSToolName:        CompactLsTool,
		ViewToolName:      CompactViewTool,
		EditToolName:      CompactEditTool,
		WriteToolName:     CompactWriteTool,
		MultiEditToolName: CompactMultiEditTool,
		TodosToolName:     CompactTodosTool,
		JobOutputToolName: CompactJobOutputTool,
		JobKillToolName:   CompactJobKillTool,
		FileWriteToolName: CompactFileWriteTool,
		FileEditToolName:  CompactFileEditTool,
		FileGrepToolName:  CompactFileGrepTool,
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
