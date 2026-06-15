package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The coder (prompt_paths) and sub-agent (subagent_prompt_paths) prompt overrides
// are independent flat options; neither inherits the other. These tests pin that
// parsing contract.

func TestPromptPaths_ParsedFromConfig(t *testing.T) {
	data := []byte(`{
		"options": {
			"prompt_paths": ["prompts/base.md", "prompts/rules.md"],
			"subagent_prompt_paths": ["prompts/subagent.md"]
		}
	}`)

	cfg, err := loadFromBytes([][]byte{data})
	require.NoError(t, err)
	require.NotNil(t, cfg.Options)
	require.Equal(t, []string{"prompts/base.md", "prompts/rules.md"}, cfg.Options.PromptPaths)
	require.Equal(t, []string{"prompts/subagent.md"}, cfg.Options.SubagentPromptPaths)
}

func TestPromptPaths_Independent(t *testing.T) {
	t.Run("only coder override set", func(t *testing.T) {
		data := []byte(`{"options": {"prompt_paths": ["prompts/base.md"]}}`)
		cfg, err := loadFromBytes([][]byte{data})
		require.NoError(t, err)
		require.Equal(t, []string{"prompts/base.md"}, cfg.Options.PromptPaths)
		require.Empty(t, cfg.Options.SubagentPromptPaths, "sub-agent must not inherit the coder override")
	})

	t.Run("only sub-agent override set", func(t *testing.T) {
		data := []byte(`{"options": {"subagent_prompt_paths": ["prompts/subagent.md"]}}`)
		cfg, err := loadFromBytes([][]byte{data})
		require.NoError(t, err)
		require.Equal(t, []string{"prompts/subagent.md"}, cfg.Options.SubagentPromptPaths)
		require.Empty(t, cfg.Options.PromptPaths, "coder must not inherit the sub-agent override")
	})
}

func TestPromptPaths_OmittedDefaultsToNil(t *testing.T) {
	data := []byte(`{"options": {"compact_prompt": true}}`)
	cfg, err := loadFromBytes([][]byte{data})
	require.NoError(t, err)
	require.Nil(t, cfg.Options.PromptPaths)
	require.Nil(t, cfg.Options.SubagentPromptPaths)
}

func TestPromptPaths_MergedAcrossConfigsInOrder(t *testing.T) {
	// Slice options concatenate across config files (same merge behavior as
	// context_paths), preserving order — they do NOT replace like scalar/map keys.
	first := []byte(`{"options": {"prompt_paths": ["a.md"]}}`)
	second := []byte(`{"options": {"prompt_paths": ["b.md", "c.md"]}}`)

	cfg, err := loadFromBytes([][]byte{first, second})
	require.NoError(t, err)
	require.Equal(t, []string{"a.md", "b.md", "c.md"}, cfg.Options.PromptPaths)
}
