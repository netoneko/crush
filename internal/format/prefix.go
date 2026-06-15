package format

import (
	"fmt"
	"strings"
)

// StreamPrefixer annotates a merged non-interactive stdout stream with
// per-agent headers so the top-level agent's output and the output of any
// spawned sub-agents (the "agent" tool, which runs in child sessions) can be
// told apart. It is only meaningful when sub-agent streaming is enabled — the
// common single-session case never switches writers and so never emits a
// header.
//
// It is stateful and not safe for concurrent use: the caller feeds it the
// session ID it is about to write output for, and Prefix returns the header
// (possibly empty) to emit immediately before that output. Headers are only
// produced when the writing session changes, so a session streaming many
// chunks in a row is uninterrupted.
type StreamPrefixer struct {
	topLevel   string
	lastWriter string
	started    bool
}

// NewStreamPrefixer returns a prefixer whose unlabeled "main" stream is
// topLevelSessionID (the top-level non-interactive session). Any other session
// is treated as a sub-agent and labeled by its sub-agent id (see labelFor).
func NewStreamPrefixer(topLevelSessionID string) *StreamPrefixer {
	return &StreamPrefixer{topLevel: topLevelSessionID}
}

// Prefix returns the header bytes to write before output from sessionID, or ""
// when the writer has not changed since the last call (a continuation of the
// same session's output). The very first write from the top-level session is
// unprefixed so output for a run with no sub-agents is byte-identical to the
// unannotated stream; every writer switch thereafter emits a labeled header on
// its own line.
func (p *StreamPrefixer) Prefix(sessionID string) string {
	if p.started && sessionID == p.lastWriter {
		return ""
	}
	firstWrite := !p.started
	p.started = true
	p.lastWriter = sessionID

	label := p.labelFor(sessionID)

	// The top-level agent's first output stays clean (no header), matching the
	// no-sub-agent stream exactly.
	if firstWrite && sessionID == p.topLevel {
		return ""
	}

	// A switch header always opens on its own line. A leading newline guards
	// against the previous session's chunk not ending in one; for the very
	// first write (a run that opens with a sub-agent) there is nothing above,
	// so the leading newline is omitted.
	var b strings.Builder
	if !firstWrite {
		b.WriteString("\n")
	}
	b.WriteString("[")
	b.WriteString(label)
	b.WriteString("]\n")
	return b.String()
}

// labelFor returns the display label for a session: "main" for the top-level
// session, or "subagent <id>" for a sub-agent. A sub-agent session ID has the
// form "messageID$$toolCallID"; the toolCallID is the sub-agent's id and the
// part that distinguishes parallel sub-agents spawned from the same parent
// message. If the ID is not in that form the whole ID is used verbatim.
func (p *StreamPrefixer) labelFor(sessionID string) string {
	if sessionID == p.topLevel {
		return "main"
	}
	id := sessionID
	if idx := strings.Index(sessionID, "$$"); idx >= 0 {
		id = sessionID[idx+len("$$"):]
	}
	return fmt.Sprintf("subagent %s", id)
}
