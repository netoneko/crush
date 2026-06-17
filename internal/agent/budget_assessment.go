package agent

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

// Budget-nudge defaults. These mirror the mid-run spiral nudge in spirit
// (detect a failure mode during a run, inject a one-shot reflective message,
// keep going) but key off resource pressure instead of repetition: the
// context-budget nudge watches how full the model's context window is, and the
// time-budget nudge watches wall-clock elapsed against a configured budget.
//
// Both fire a softer "warn" once their warn threshold is crossed and a harder
// "stop and write your deliverable now" once the hard ceiling is crossed —
// converting "ran out of room / ran long and produced nothing" into "wrapped
// up with a partial-but-honest deliverable".
const (
	contextBudgetWarnDefault = 0.70 // fraction of the context window
	contextBudgetHardDefault = 0.85

	timeBudgetWarnDefault = 0.75 // fraction of the time budget; hard is always 1.0
)

// resolveContextBudget returns the effective context-budget settings, merging
// the sub-agent override over the global value over the built-in default,
// independently per field (so a sub-agent can override just one knob). For the
// top-level agent, pass nil sub args.
//
// Validity floors: warn/hard must be in (0, 1]; out-of-range values are treated
// as unset and fall through to the next source.
func resolveContextBudget(
	subEnabled *bool, subCfg *config.ContextBudgetConfig,
	globalEnabled *bool, globalCfg *config.ContextBudgetConfig,
) (on bool, warn, hard float64) {
	switch {
	case subEnabled != nil:
		on = *subEnabled
	case globalEnabled != nil:
		on = *globalEnabled
	}

	warn, hard = contextBudgetWarnDefault, contextBudgetHardDefault
	for _, cfg := range []*config.ContextBudgetConfig{globalCfg, subCfg} {
		if cfg == nil {
			continue
		}
		if validFraction(cfg.WarnPercent) {
			warn = cfg.WarnPercent
		}
		if validFraction(cfg.HardPercent) {
			hard = cfg.HardPercent
		}
	}
	return on, warn, hard
}

// resolveTimeBudget returns the effective time-budget settings, merging the
// sub-agent override over the global value over the built-in default per field.
// budgetMinutes has no default: a value <= 0 means the budget is unset and the
// nudge stays inert even when enabled. For the top-level agent, pass nil sub
// args.
func resolveTimeBudget(
	subEnabled *bool, subCfg *config.TimeBudgetConfig,
	globalEnabled *bool, globalCfg *config.TimeBudgetConfig,
) (on bool, budget time.Duration, warn float64) {
	switch {
	case subEnabled != nil:
		on = *subEnabled
	case globalEnabled != nil:
		on = *globalEnabled
	}

	warn = timeBudgetWarnDefault
	var minutes float64
	for _, cfg := range []*config.TimeBudgetConfig{globalCfg, subCfg} {
		if cfg == nil {
			continue
		}
		if cfg.BudgetMinutes > 0 {
			minutes = cfg.BudgetMinutes
		}
		if validFraction(cfg.WarnPercent) {
			warn = cfg.WarnPercent
		}
	}
	return on, time.Duration(minutes * float64(time.Minute)), warn
}

// validFraction reports whether f is a usable threshold fraction, i.e. in (0, 1].
func validFraction(f float64) bool {
	return f > 0 && f <= 1
}

// buildContextBudgetPrompt composes the nudge injected when context usage
// crosses a threshold. Past the hard ceiling it tells the model to stop and
// write its deliverable now; at the softer warn level it tells it to start
// consolidating.
func buildContextBudgetPrompt(usedFraction float64, hard bool) string {
	pct := int(math.Round(usedFraction * 100))
	var sb strings.Builder
	fmt.Fprintf(&sb, "Context budget: you have used ~%d%% of your context window", pct)
	if hard {
		sb.WriteString(" and are about to run out of room. Stop investigating now.\n")
		sb.WriteString("- Write your findings / deliverable to its file with what you already have; mark anything still unknown honestly rather than digging further.\n")
		sb.WriteString("- Do not make more exploratory tool calls — once the window overflows, earlier context is dropped and the run can produce nothing.\n")
		sb.WriteString("Wrap up and return.")
	} else {
		sb.WriteString(". Start wrapping up before you run out of room.\n")
		sb.WriteString("- Prioritize recording what you have already learned into your deliverable file.\n")
		sb.WriteString("- Avoid opening new lines of investigation; make any remaining tool calls count and prefer narrow, targeted ones.")
	}
	return sb.String()
}

// buildTimeBudgetPrompt composes the nudge injected when wall-clock elapsed
// crosses a threshold relative to the configured budget. Past the budget it
// forces a wrap-up; before it, it asks the model to pace itself.
func buildTimeBudgetPrompt(elapsed, budget time.Duration, hard bool) string {
	em := int(math.Round(elapsed.Minutes()))
	bm := int(math.Round(budget.Minutes()))
	var sb strings.Builder
	fmt.Fprintf(&sb, "Time budget: you are ~%d min into your ~%d min budget", em, bm)
	if hard {
		sb.WriteString(" — time is up. Stop investigating now.\n")
		sb.WriteString("- Write your findings / deliverable to its file with what you already have; mark anything still unknown honestly.\n")
		sb.WriteString("- Do not start new lines of investigation.\n")
		sb.WriteString("Wrap up and return.")
	} else {
		sb.WriteString(". Pace yourself so you finish in time.\n")
		sb.WriteString("- Focus on the single highest-value remaining step.\n")
		sb.WriteString("- Start consolidating toward your deliverable rather than broadening the investigation.")
	}
	return sb.String()
}
