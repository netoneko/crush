package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// step builds a StepResult whose content is the given tool calls (by name).
func step(toolNames ...string) fantasy.StepResult {
	content := fantasy.ResponseContent{}
	for i, name := range toolNames {
		content = append(content, fantasy.ToolCallContent{
			ToolCallID: name + string(rune('0'+i)),
			ToolName:   name,
			Input:      "{}",
		})
	}
	return fantasy.StepResult{Response: fantasy.Response{Content: content}}
}

func TestSummarizeToolUsage_CountsWithinWindow(t *testing.T) {
	t.Parallel()

	steps := []fantasy.StepResult{
		step("grep"), step("grep"), step("view"),
		step("grep"), step("ls"),
	}
	stats := summarizeToolUsage(steps, 10)
	require.Equal(t, 5, stats.total)
	require.Equal(t, 3, stats.counts["grep"])
	require.Equal(t, 1, stats.counts["view"])

	// A tighter window only counts the most recent steps.
	windowed := summarizeToolUsage(steps, 2)
	require.Equal(t, 2, windowed.total)
	require.Equal(t, 1, windowed.counts["grep"])
	require.Equal(t, 1, windowed.counts["ls"])
}

func TestSummarizeToolUsage_IgnoresTextOnlySteps(t *testing.T) {
	t.Parallel()

	steps := []fantasy.StepResult{step("grep"), step(), step("grep")}
	stats := summarizeToolUsage(steps, 10)
	require.Equal(t, 2, stats.total)
	require.Equal(t, 2, stats.counts["grep"])
}

func TestShouldAssessSpiral_TripsOnRepeatThreshold(t *testing.T) {
	t.Parallel()

	steps := []fantasy.StepResult{
		step("grep"), step("grep"), step("view"), step("grep"), step("grep"),
	}
	stats := summarizeToolUsage(steps, 10)
	require.True(t, shouldAssessSpiral(stats, 4), "grep ×4 meets threshold 4")
	require.False(t, shouldAssessSpiral(stats, 5), "grep ×4 is below threshold 5")
}

func TestBuildSpiralAssessmentPrompt_IncludesStatsAndGuidance(t *testing.T) {
	t.Parallel()

	steps := []fantasy.StepResult{
		step("grep"), step("grep"), step("grep"), step("view"),
	}
	stats := summarizeToolUsage(steps, 10)
	prompt := buildSpiralAssessmentPrompt(stats)

	require.Contains(t, prompt, "grep ×3")
	require.Contains(t, prompt, "view ×1")
	require.Contains(t, prompt, "called grep 3 times")
	// Guidance: scope tighter or abandon.
	require.Contains(t, strings.ToLower(prompt), "narrow")
	require.Contains(t, strings.ToLower(prompt), "abandon")
}

func TestBreakdown_DescendingAndStable(t *testing.T) {
	t.Parallel()

	stats := toolUsageStats{counts: map[string]int{"view": 1, "grep": 3, "ls": 1}, total: 5}
	// Descending by count; ties broken alphabetically — deterministic output.
	require.Equal(t, "grep ×3, ls ×1, view ×1", stats.breakdown())
}

// TestInjectMidRunNudge_AppendsToHistoryCacheSafe is the cache-safety
// guarantee: the nudge must be appended to the persisted history (append-only
// growth) and must NOT mutate the existing conversation prefix. A stable prefix
// is exactly what lets the prompt cache stay warm across steps; a transient,
// non-persisted injection would diverge the next step's prefix and break it.
func TestInjectMidRunNudge_AppendsToHistoryCacheSafe(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	ctx := t.Context()
	a := NewSessionAgent(SessionAgentOptions{
		Sessions: env.sessions,
		Messages: env.messages,
		IsYolo:   true,
	}).(*sessionAgent)

	sess, err := env.sessions.Create(ctx, "spiral-history-test")
	require.NoError(t, err)

	// Seed prior history.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "find the bug"}},
	})
	require.NoError(t, err)
	before, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	// The fantasy-message prefix the loop would have assembled for this step.
	prefix := []fantasy.Message{fantasy.NewUserMessage("find the bug")}
	prefixSnapshot := append([]fantasy.Message(nil), prefix...)

	const nudge = "Self-check: stop and reassess."
	got, err := a.injectMidRunNudge(ctx, sess.ID, nudge, prefix)
	require.NoError(t, err)

	// 1) The existing prefix is byte-stable — the cache prefix is preserved.
	require.Equal(t, prefixSnapshot, got[:len(prefixSnapshot)],
		"injecting the nudge must not mutate the existing prefix")
	require.Greater(t, len(got), len(prefixSnapshot), "the nudge must be appended to the tail")

	// 2) History grew append-only: prior messages are untouched (same IDs) and
	// the nudge is persisted as the new last message.
	after, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before)+1, "exactly one message appended to persisted history")
	for i := range before {
		require.Equal(t, before[i].ID, after[i].ID,
			"existing history messages must be unchanged (append-only)")
	}
	last := after[len(after)-1]
	require.Equal(t, message.User, last.Role, "nudge is persisted as a real user message")
	require.Equal(t, nudge, last.Content().String())
}

func TestResolveMidRunAssessment_DefaultsAndGlobal(t *testing.T) {
	t.Parallel()

	// All nil: off, defaults.
	on, window, threshold, maxInject := resolveMidRunAssessment(nil, nil, nil, nil)
	require.False(t, on)
	require.Equal(t, midRunAssessmentWindowDefault, window)
	require.Equal(t, midRunAssessmentThresholdDefault, threshold)
	require.Equal(t, midRunAssessmentMaxInjectDefault, maxInject)

	// Global enabled with explicit, valid tuning, no sub override.
	enabled := true
	on, window, threshold, maxInject = resolveMidRunAssessment(nil, nil, &enabled, &config.MidRunSelfAssessmentConfig{
		Window: 8, RepeatThreshold: 4, MaxInjections: 1,
	})
	require.True(t, on)
	require.Equal(t, 8, window)
	require.Equal(t, 4, threshold)
	require.Equal(t, 1, maxInject)

	// Invalid/below-floor global values fall back to defaults.
	on, window, threshold, maxInject = resolveMidRunAssessment(nil, nil, &enabled, &config.MidRunSelfAssessmentConfig{
		Window: 0, RepeatThreshold: 1, MaxInjections: 0,
	})
	require.True(t, on)
	require.Equal(t, midRunAssessmentWindowDefault, window)
	require.Equal(t, midRunAssessmentThresholdDefault, threshold)
	require.Equal(t, midRunAssessmentMaxInjectDefault, maxInject)
}

func TestResolveMidRunAssessment_SubAgentMerging(t *testing.T) {
	t.Parallel()

	globalEnabled := true
	globalCfg := &config.MidRunSelfAssessmentConfig{Window: 8, RepeatThreshold: 4, MaxInjections: 3}

	// Sub-agent overrides only the window; threshold + max_injections inherit
	// from the global tuning (field-by-field merge, not whole-struct replace).
	on, window, threshold, maxInject := resolveMidRunAssessment(
		nil, &config.MidRunSelfAssessmentConfig{Window: 2},
		&globalEnabled, globalCfg,
	)
	require.True(t, on, "enable inherited from global")
	require.Equal(t, 2, window, "window overridden by sub")
	require.Equal(t, 4, threshold, "threshold inherited from global")
	require.Equal(t, 3, maxInject, "max_injections inherited from global")

	// Sub-agent can disable while global is on.
	subOff := false
	on, _, _, _ = resolveMidRunAssessment(&subOff, nil, &globalEnabled, globalCfg)
	require.False(t, on, "sub override disables for the sub-agent")

	// Sub-agent can enable while global is unset/off.
	subOn := true
	on, _, _, _ = resolveMidRunAssessment(&subOn, nil, nil, nil)
	require.True(t, on, "sub override enables independently")
}
