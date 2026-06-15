package agent

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
)

const (
	midRunAssessmentWindowDefault    = 7
	midRunAssessmentThresholdDefault = 5
	midRunAssessmentMaxInjectDefault = 2
)

// toolUsageStats summarizes how often each tool was called across a window of
// steps. It backs the mid-run self-assessment nudge: detection trips on the
// most-repeated tool and the full breakdown is reported back to the model.
type toolUsageStats struct {
	counts map[string]int
	total  int
}

// summarizeToolUsage counts tool calls by name across the last window steps.
// Steps with no tool calls (plain text/reasoning) are ignored. A window <= 0 or
// larger than the slice inspects everything available.
func summarizeToolUsage(steps []fantasy.StepResult, window int) toolUsageStats {
	stats := toolUsageStats{counts: make(map[string]int)}
	if len(steps) == 0 {
		return stats
	}
	if window > 0 && window < len(steps) {
		steps = steps[len(steps)-window:]
	}
	for _, step := range steps {
		for _, tc := range step.Content.ToolCalls() {
			stats.counts[tc.ToolName]++
			stats.total++
		}
	}
	return stats
}

// maxRepeat returns the tool called most often in the window and its count.
func (s toolUsageStats) maxRepeat() (name string, count int) {
	for n, c := range s.counts {
		if c > count {
			name, count = n, c
		}
	}
	return name, count
}

// breakdown renders the per-tool counts as a stable, descending, comma-joined
// list (e.g. "grep ×6, view ×3"), so the injected message is deterministic.
func (s toolUsageStats) breakdown() string {
	type kv struct {
		name  string
		count int
	}
	pairs := make([]kv, 0, len(s.counts))
	for n, c := range s.counts {
		pairs = append(pairs, kv{n, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = fmt.Sprintf("%s ×%d", p.name, p.count)
	}
	return strings.Join(parts, ", ")
}

// shouldAssessSpiral reports whether the tool usage looks like a spiral: a
// single tool repeated at least threshold times within the window.
func shouldAssessSpiral(stats toolUsageStats, threshold int) bool {
	_, count := stats.maxRepeat()
	return count >= threshold
}

// buildSpiralAssessmentPrompt composes the one-shot nudge injected mid-run when
// a spiral is detected. It reports what the model has been doing (tool-usage
// stats) and asks it to either scope tighter or abandon the approach.
func buildSpiralAssessmentPrompt(stats toolUsageStats) string {
	name, count := stats.maxRepeat()
	var sb strings.Builder
	sb.WriteString("Self-check: you appear to be repeating tool calls without making progress. ")
	fmt.Fprintf(&sb, "Recent tool usage: %s (called %s %d times).\n\n", stats.breakdown(), name, count)
	sb.WriteString("Stop and reassess before the next call:\n")
	sb.WriteString("- Restate what you are trying to find and what you have learned so far.\n")
	sb.WriteString("- If the search is too broad or mis-scoped, narrow it: tighter query, specific paths/globs, or a different tool.\n")
	sb.WriteString("- If you have already tried several times without success, abandon this approach — proceed with what you know, or report that you could not find it rather than searching again the same way.\n")
	sb.WriteString("Do not repeat the same call with the same arguments.")
	return sb.String()
}

// resolveMidRunAssessment returns the effective mid-run self-assessment
// settings, merging the sub-agent override over the global value over the
// built-in default — independently per field, so a sub-agent can override just
// one knob (e.g. a tighter window) while inheriting the rest. Precedence per
// field is: valid sub value → valid global value → default. For the top-level
// agent, pass nil sub args.
//
// Validity floors: window >= 1, threshold >= 2, maxInjections >= 1; values
// below the floor are treated as unset and fall through to the next source.
func resolveMidRunAssessment(
	subEnabled *bool, subCfg *config.MidRunSelfAssessmentConfig,
	globalEnabled *bool, globalCfg *config.MidRunSelfAssessmentConfig,
) (on bool, window, threshold, maxInject int) {
	// enable: sub override wins, else global, else off.
	switch {
	case subEnabled != nil:
		on = *subEnabled
	case globalEnabled != nil:
		on = *globalEnabled
	}

	window, threshold, maxInject = midRunAssessmentWindowDefault, midRunAssessmentThresholdDefault, midRunAssessmentMaxInjectDefault
	// Apply global then sub so the sub-agent's valid fields take precedence
	// while unset ones fall back to whatever global resolved to.
	for _, cfg := range []*config.MidRunSelfAssessmentConfig{globalCfg, subCfg} {
		if cfg == nil {
			continue
		}
		if cfg.Window >= 1 {
			window = cfg.Window
		}
		if cfg.RepeatThreshold >= 2 {
			threshold = cfg.RepeatThreshold
		}
		if cfg.MaxInjections >= 1 {
			maxInject = cfg.MaxInjections
		}
	}
	return on, window, threshold, maxInject
}
