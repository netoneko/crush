package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSubagentRoles(t *testing.T) {
	require.Nil(t, subagentRoles(nil))
	require.Nil(t, subagentRoles(&config.Options{}))

	roles := map[string][]string{"reviewer": {"r.md"}}
	require.Equal(t, roles, subagentRoles(&config.Options{SubagentRoles: roles}))
}

func TestSortedRoleNames(t *testing.T) {
	got := sortedRoleNames(map[string][]string{
		"reviewer": {"r.md"},
		"explorer": {"e.md"},
		"debugger": {"d.md"},
	})
	require.Equal(t, []string{"debugger", "explorer", "reviewer"}, got)
	require.Empty(t, sortedRoleNames(nil))
}

// list_roles is granted to the coder only when roles are configured, and never
// when it has been explicitly disabled. The sub-agent never gets it.
func TestSetupAgentsGrantsListRolesOnlyWhenRolesConfigured(t *testing.T) {
	t.Run("no roles -> coder does not get list_roles", func(t *testing.T) {
		cfg := &config.Config{Options: &config.Options{}}
		cfg.SetupAgents()
		require.NotContains(t, cfg.Agents[config.AgentCoder].AllowedTools, config.ListRolesToolName)
	})

	t.Run("roles set -> coder gets list_roles", func(t *testing.T) {
		cfg := &config.Config{Options: &config.Options{
			SubagentRoles: map[string][]string{"reviewer": {"r.md"}},
		}}
		cfg.SetupAgents()
		require.Contains(t, cfg.Agents[config.AgentCoder].AllowedTools, config.ListRolesToolName)
		// The sub-agent (read-only default tool set) must not get it.
		require.NotContains(t, cfg.Agents[config.AgentTask].AllowedTools, config.ListRolesToolName)
	})

	t.Run("roles set but tool disabled -> coder does not get list_roles", func(t *testing.T) {
		cfg := &config.Config{Options: &config.Options{
			SubagentRoles: map[string][]string{"reviewer": {"r.md"}},
			DisabledTools: []string{config.ListRolesToolName},
		}}
		cfg.SetupAgents()
		require.NotContains(t, cfg.Agents[config.AgentCoder].AllowedTools, config.ListRolesToolName)
	})
}

func TestRoleSummary(t *testing.T) {
	wd := t.TempDir()
	store, err := config.Init(wd, t.TempDir(), false)
	require.NoError(t, err)
	c := &coordinator{cfg: store}

	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(wd, name), []byte(content), 0o644))
	}

	t.Run("empty paths -> empty summary", func(t *testing.T) {
		require.Empty(t, c.roleSummary(nil))
	})

	t.Run("missing file -> empty summary", func(t *testing.T) {
		require.Empty(t, c.roleSummary([]string{"nope.md"}))
	})

	t.Run("first non-blank line, markdown heading stripped", func(t *testing.T) {
		write("reviewer.md", "\n\n# Code reviewer role\n\nFull body here.")
		require.Equal(t, "Code reviewer role", c.roleSummary([]string{"reviewer.md"}))
	})

	t.Run("summary comes from the first file only", func(t *testing.T) {
		write("a.md", "Primary summary")
		write("b.md", "Secondary")
		require.Equal(t, "Primary summary", c.roleSummary([]string{"a.md", "b.md"}))
	})

	t.Run("long line is truncated", func(t *testing.T) {
		long := "x"
		for len(long) < 200 {
			long += "x"
		}
		write("long.md", long)
		got := c.roleSummary([]string{"long.md"})
		require.LessOrEqual(t, len([]rune(got)), 120)
		require.Contains(t, got, "…")
	})

	// Guard: a fresh ConfigStore exposes the role map round-trip we rely on.
	require.True(t, slices.Contains(sortedRoleNames(map[string][]string{"x": nil}), "x"))
}
