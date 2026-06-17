package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// TestBudgetDefaults pins the chosen defaults so an accidental edit is caught.
func TestBudgetDefaults(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0.70, contextBudgetWarnDefault)
	require.Equal(t, 0.85, contextBudgetHardDefault)
	require.Equal(t, 0.75, timeBudgetWarnDefault)
}

func TestResolveContextBudget_DefaultsAndGlobal(t *testing.T) {
	t.Parallel()

	// All nil: off, defaults.
	on, warn, hard := resolveContextBudget(nil, nil, nil, nil)
	require.False(t, on)
	require.Equal(t, contextBudgetWarnDefault, warn)
	require.Equal(t, contextBudgetHardDefault, hard)

	// Global enabled with explicit, valid tuning.
	enabled := true
	on, warn, hard = resolveContextBudget(nil, nil, &enabled, &config.ContextBudgetConfig{
		WarnPercent: 0.5, HardPercent: 0.9,
	})
	require.True(t, on)
	require.Equal(t, 0.5, warn)
	require.Equal(t, 0.9, hard)

	// Out-of-range values fall back to defaults.
	on, warn, hard = resolveContextBudget(nil, nil, &enabled, &config.ContextBudgetConfig{
		WarnPercent: 0, HardPercent: 1.5,
	})
	require.True(t, on)
	require.Equal(t, contextBudgetWarnDefault, warn)
	require.Equal(t, contextBudgetHardDefault, hard)
}

func TestResolveContextBudget_SubAgentMerging(t *testing.T) {
	t.Parallel()

	globalEnabled := true
	globalCfg := &config.ContextBudgetConfig{WarnPercent: 0.6, HardPercent: 0.9}

	// Sub-agent overrides only warn; hard inherits from the global tuning.
	on, warn, hard := resolveContextBudget(
		nil, &config.ContextBudgetConfig{WarnPercent: 0.4},
		&globalEnabled, globalCfg,
	)
	require.True(t, on, "enable inherited from global")
	require.Equal(t, 0.4, warn, "warn overridden by sub")
	require.Equal(t, 0.9, hard, "hard inherited from global")

	// Sub-agent can disable while global is on.
	subOff := false
	on, _, _ = resolveContextBudget(&subOff, nil, &globalEnabled, globalCfg)
	require.False(t, on)
}

func TestResolveTimeBudget_DefaultsAndGlobal(t *testing.T) {
	t.Parallel()

	// All nil: off, no budget, default warn.
	on, budget, warn := resolveTimeBudget(nil, nil, nil, nil)
	require.False(t, on)
	require.Equal(t, time.Duration(0), budget, "no default budget — inert until set")
	require.Equal(t, timeBudgetWarnDefault, warn)

	// Global enabled with a budget.
	enabled := true
	on, budget, warn = resolveTimeBudget(nil, nil, &enabled, &config.TimeBudgetConfig{
		BudgetMinutes: 10, WarnPercent: 0.5,
	})
	require.True(t, on)
	require.Equal(t, 10*time.Minute, budget)
	require.Equal(t, 0.5, warn)

	// A non-positive budget stays inert (zero duration) even when enabled.
	on, budget, _ = resolveTimeBudget(nil, nil, &enabled, &config.TimeBudgetConfig{BudgetMinutes: 0})
	require.True(t, on)
	require.Equal(t, time.Duration(0), budget)
}

func TestResolveTimeBudget_SubAgentMerging(t *testing.T) {
	t.Parallel()

	globalEnabled := true
	globalCfg := &config.TimeBudgetConfig{BudgetMinutes: 20, WarnPercent: 0.8}

	// Sub-agent tightens just the budget; warn inherits from the global tuning.
	on, budget, warn := resolveTimeBudget(
		nil, &config.TimeBudgetConfig{BudgetMinutes: 5},
		&globalEnabled, globalCfg,
	)
	require.True(t, on)
	require.Equal(t, 5*time.Minute, budget, "budget overridden by sub")
	require.Equal(t, 0.8, warn, "warn inherited from global")
}

func TestBuildContextBudgetPrompt_WarnVsHard(t *testing.T) {
	t.Parallel()

	warn := buildContextBudgetPrompt(0.72, false)
	require.Contains(t, warn, "~72%")
	require.Contains(t, strings.ToLower(warn), "wrapping up")
	require.NotContains(t, strings.ToLower(warn), "stop investigating now")

	hard := buildContextBudgetPrompt(0.91, true)
	require.Contains(t, hard, "~91%")
	require.Contains(t, strings.ToLower(hard), "stop investigating now")
	require.Contains(t, strings.ToLower(hard), "write your findings")
}

func TestBuildTimeBudgetPrompt_WarnVsHard(t *testing.T) {
	t.Parallel()

	warn := buildTimeBudgetPrompt(8*time.Minute, 10*time.Minute, false)
	require.Contains(t, warn, "~8 min")
	require.Contains(t, warn, "~10 min")
	require.Contains(t, strings.ToLower(warn), "pace yourself")
	require.NotContains(t, strings.ToLower(warn), "time is up")

	hard := buildTimeBudgetPrompt(10*time.Minute, 10*time.Minute, true)
	require.Contains(t, hard, "~10 min")
	require.Contains(t, strings.ToLower(hard), "time is up")
	require.Contains(t, strings.ToLower(hard), "write your findings")
}
