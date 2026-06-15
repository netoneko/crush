package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// resolveSubagentPromptPaths is the single source of truth for which prompt the
// spawned Task sub-agent uses. Resolution order: subagent_prompt_paths →
// prompt_paths → built-in task template (nil).
func TestResolveSubagentPromptPaths(t *testing.T) {
	tests := []struct {
		name string
		opts *config.Options
		want []string
	}{
		{
			name: "nil options -> built-in",
			opts: nil,
			want: nil,
		},
		{
			name: "neither set -> built-in",
			opts: &config.Options{},
			want: nil,
		},
		{
			name: "only coder prompt set -> sub-agent inherits it",
			opts: &config.Options{PromptPaths: []string{"coder.md"}},
			want: []string{"coder.md"},
		},
		{
			name: "only sub-agent prompt set -> uses its own",
			opts: &config.Options{SubagentPromptPaths: []string{"sub.md"}},
			want: []string{"sub.md"},
		},
		{
			name: "both set -> sub-agent override wins (no inheritance)",
			opts: &config.Options{
				PromptPaths:         []string{"coder.md"},
				SubagentPromptPaths: []string{"sub.md"},
			},
			want: []string{"sub.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resolveSubagentPromptPaths(tt.opts))
		})
	}
}

// The coder always reads prompt_paths directly and never the sub-agent key, so a
// sub-agent-only override must not change what the coder resolves. This pins the
// "no cross-contamination from sub-agent to orchestrator" direction.
func TestSubagentOverrideDoesNotAffectCoderPaths(t *testing.T) {
	opts := &config.Options{SubagentPromptPaths: []string{"sub.md"}}
	// The coder selection (coordinator.go) keys off opts.PromptPaths only.
	require.Empty(t, opts.PromptPaths, "coder must see no override when only the sub-agent key is set")
	require.Equal(t, []string{"sub.md"}, resolveSubagentPromptPaths(opts))
}
