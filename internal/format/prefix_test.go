package format

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamPrefixer_TopLevelOnlyIsUnannotated(t *testing.T) {
	t.Parallel()

	p := NewStreamPrefixer("S")
	require.Equal(t, "", p.Prefix("S"), "first top-level write must be unprefixed")
	require.Equal(t, "", p.Prefix("S"), "continued top-level writes must be unprefixed")
	require.Equal(t, "", p.Prefix("S"))
}

func TestStreamPrefixer_SubAgentSwitchEmitsLabel(t *testing.T) {
	t.Parallel()

	p := NewStreamPrefixer("S")
	require.Equal(t, "", p.Prefix("S"))                                // main, clean
	require.Equal(t, "\n[subagent tool-a]\n", p.Prefix("m1$$tool-a"))  // switch to sub-agent
	require.Equal(t, "", p.Prefix("m1$$tool-a"))                       // continuation, no header
	require.Equal(t, "\n[main]\n", p.Prefix("S"))                      // switch back to main
	require.Equal(t, "", p.Prefix("S"))                                // continuation
}

func TestStreamPrefixer_LabelsByToolCallID(t *testing.T) {
	t.Parallel()

	// The label is the sub-agent's toolCallID (the part after "$$"), so
	// parallel sub-agents spawned from the same parent message are
	// distinguished, and a given sub-agent keeps the same label across switches.
	p := NewStreamPrefixer("S")
	require.Equal(t, "", p.Prefix("S"))
	require.Equal(t, "\n[subagent a]\n", p.Prefix("m1$$a"))
	require.Equal(t, "\n[subagent b]\n", p.Prefix("m1$$b"))
	require.Equal(t, "\n[subagent a]\n", p.Prefix("m1$$a"))
	require.Equal(t, "\n[subagent b]\n", p.Prefix("m1$$b"))
}

func TestStreamPrefixer_FirstWriteFromSubAgentHasNoLeadingNewline(t *testing.T) {
	t.Parallel()

	// A run whose top-level agent calls the tool before emitting any text: the
	// very first thing on stdout is the sub-agent, so its header must not open
	// with a stray leading blank line.
	p := NewStreamPrefixer("S")
	require.Equal(t, "[subagent a]\n", p.Prefix("m1$$a"))
	require.Equal(t, "\n[main]\n", p.Prefix("S"))
}
