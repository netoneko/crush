# Tool & Run Optimizations for Local Models

This is a configuration reference for the optimizations this fork adds on top of
upstream Crush to make small / local models (gemma, qwen3, glm, etc.) behave
better under tight context windows and weaker instruction-following.

Everything here is opt-in or has a safe default; all of it lives under the
`options` block in `crush.json`. For the field-by-field schema see
[`schema.json`](../schema.json); for the benchmark history that motivated each
knob see [`TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md`](./TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md).

---

## Quick reference

| Option | Type | Default | Purpose |
|--------|------|---------|---------|
| `enable_task_self_assessment` | bool | `false` | After a run that ends with open todos, re-prompt the model to finish/close them. Applies to the top-level coder and (unless overridden) the spawned Task sub-agent. |
| `task_self_assessment` | object | — | Tuning for the reminders above (loop count + completion target). |
| `subagent_enable_task_self_assessment` | bool | — | Override `enable_task_self_assessment` for the spawned Task sub-agent only. Unset = inherit the global value. |
| `subagent_task_self_assessment` | object | — | Override `task_self_assessment` tuning for the spawned Task sub-agent only. Unset = inherit the global tuning. |
| `enable_midrun_self_assessment` | bool | `false` | *During* a run, detect a tool-call spiral (same tool repeated) and inject a one-shot nudge with tool-usage stats asking the model to scope tighter or abandon. Applies to the coder and the Task sub-agent. |
| `midrun_self_assessment` | object | — | Tuning for the nudge above (`window`, `repeat_threshold`, `max_injections`). |
| `subagent_enable_midrun_self_assessment` | bool | — | Override `enable_midrun_self_assessment` for the spawned Task sub-agent only. Unset = inherit the global value. |
| `subagent_midrun_self_assessment` | object | — | Override `midrun_self_assessment` for the Task sub-agent only. **Merges field-by-field** over the global tuning — set just the knobs you want to change. |
| `enable_context_budget` | bool | `false` | *During* a run, watch how full the model's context window is and inject a one-shot nudge to wrap up (soft) or stop and write the deliverable now (hard) as usage crosses thresholds. Inert when the context window is unknown. Applies to the coder and the Task sub-agent. |
| `context_budget` | object | — | Tuning for the context-budget nudge (`warn_percent`, `hard_percent` — fractions of the context window). |
| `subagent_enable_context_budget` | bool | — | Override `enable_context_budget` for the Task sub-agent only. Unset = inherit the global value. |
| `subagent_context_budget` | object | — | Override `context_budget` for the Task sub-agent only. **Merges field-by-field** over the global tuning. |
| `enable_time_budget` | bool | `false` | *During* a run, watch wall-clock elapsed against a budget and inject a one-shot nudge to pace yourself (soft) or stop and write the deliverable now (hard). Requires `time_budget.budget_minutes`. Applies to the coder and the Task sub-agent. |
| `time_budget` | object | — | Tuning for the time-budget nudge (`budget_minutes` — required; `warn_percent` — fraction of the budget). |
| `subagent_enable_time_budget` | bool | — | Override `enable_time_budget` for the Task sub-agent only. Unset = inherit the global value. |
| `subagent_time_budget` | object | — | Override `time_budget` for the Task sub-agent only. **Merges field-by-field** over the global tuning (e.g. a tighter budget for sub-agent investigations). |
| `enable_memory` | bool | `true` | Spill oversized tool results into a queryable memory store instead of the prompt. |
| `memory_hard_limit_bytes` | int | `8192` | Byte threshold above which a tool result is stored in memory. |
| `memory_overspill` | float | `0.20` | Slack fraction kept inline before spilling (e.g. `0.20` → up to `hard_limit*1.2`). |
| `memory_preview_lines` | int | `10` | Lines shown in the inline reference summary. |
| `memory_refuse_tools` | []string | — | Tools that reject oversized output (model must re-call narrower) instead of storing it. |
| `compact_tools` | bool | `false` | Use short tool descriptions to save prompt tokens. |
| `compact_prompt` | bool | `false` | Use a shorter system prompt (rules preserved, examples stripped). |
| `summarize_prompt` | bool | `false` | Async: compress the system prompt with the small model at session start. |
| `prompt_paths` | []string | — | Files concatenated (in order) to **fully override** the coder system prompt. Rendered as a Go template; suppresses auto-discovered context files. Overrides `compact_prompt`. |
| `subagent_prompt_paths` | []string | — | Same as `prompt_paths` but for the spawned Task **sub-agent**. When unset the sub-agent inherits `prompt_paths`; set this only to diverge from the coder. |
| `subagent_roles` | map[string][]string | — | Named, specialized sub-agent prompts the coder can pick at spawn time. Each role maps to prompt file(s) (same semantics as `prompt_paths`). When set, the coder gains the `list_roles` tool and may pass a `role` argument to the `agent` tool to spawn that role. No role → the normal `subagent_prompt_paths`→`prompt_paths`→built-in resolution. |
| `stream_subagent_output` | bool | `false` | In non-interactive mode (`crush run`), stream the live output of spawned sub-agents to stdout, not just the top-level agent's. |
| `disabled_tools` | []string | — | Built-in tools to disable and hide from **every** agent (coder *and* sub-agent). A disabled tool can never be re-granted by `task_tools`. |
| `task_tools` | []string | — | Built-in tools the spawned Task sub-agent may call, plus the special `mcp` token for all MCP tools. Unset = read-only default (`glob`/`grep`/`ls`/`sourcegraph`/`view`, no MCP). Intersected with the enabled set, so `disabled_tools` still wins. |

A representative local-model config:

```jsonc
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "compact_prompt": true,
    "compact_tools": true,
    "enable_memory": true,
    "enable_task_self_assessment": true,
    "task_self_assessment": {
      "max_reminders": 5,
      "target_completion": 1.0
    }
  }
}
```

---

## Measured impact (and caveats)

What the benchmark journal
([`TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md`](./TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md)),
the compression analysis
([`TOOLING_IMPROVEMENTS_COMPACT_SYSTEM_PROMPT.md`](./TOOLING_IMPROVEMENTS_COMPACT_SYSTEM_PROMPT.md)),
and the live field validation (the cost-anomaly-agent runbook) actually
measured. Read this as "real but uneven" — several headline numbers shrink once
measurement artifacts and run-to-run variance are separated out, and two options
either didn't show up or backfired. **Every claim here is paired with its
caveat on purpose.**

### Headline trajectory

Same task (`01_verify_apk_bootstrap`, qwen3-yolo) across configs:

| Metric | no memory | memory fix | memory + tools |
|---|---|---|---|
| Session duration | 18.2 min | 8.1 min | ~7 min |
| Total tool calls | 92 | 46 | **27** |
| Avg response time | 18.8 s | 14.0 s | **6.4 s** |
| Max response time | 175.9 s | 48.8 s | **32 s** |
| Total LLM time | ~772 s | ~238 s | **192 s** |
| Peak prompt | ~71K tok | ~62K tok | **22K tok** |

> **Caveats on the table.** (1) The first big jump (col 1→2) happened *with memory
> inactive in both runs* — it was the model being more direct that session, i.e.
> run-to-run variance, not the optimization. (2) An earlier ~46K→12K *starting*-prompt
> "drop" was a **tokenizer measurement artifact** (an older Ollama counted tool
> schemas differently); the real baseline is ~12K and all runs are comparable at
> that size. The durable, attributable wins are the tool-call / latency reductions
> and the low peak context — real, but smaller than the raw 3–4× the table implies.

### Per-optimization

| Optimization | Measured effect | Caveat |
|---|---|---|
| **Memory offload** | Reached **85,391 tokens — ~22K past the baseline parse-failure point** — with no malformed output. The primary correctness win. | `memory_scroll` re-inflates context (40 calls in one run); the wrap is **line-based**, so single-line JSON (e.g. a 73 KB `query_costs` row, `Lines: 1`) trims nothing — in production one call jumped the prompt ~60K→135K tokens in one step. Many sub-threshold results also sum into a spike. ~930-token system-prompt overhead. |
| **`compact_tools`** | Starting prompt **12,197 → 10,343 tokens (−15%)**, stable across 3 runs, no behavior regression. The cleanest repeatable win. | Honored by the patched/dev build only — **not** stock brew v0.71.0, so the lower floor isn't portable. |
| **`compact_prompt`** | Expected ~3,900-token saving. | **Observed ~20 tokens at turn 1** — template savings swamped by injected `context_paths` content. Peak was ~2,800 tokens lower, but only indirectly. Effectively inconclusive as measured. |
| **`summarize_prompt`** | 83–96% **byte** reduction of the static prompt (16,297 B → ~2,652 B at 84%). | **Actively risky over-applied:** at 84–96% the 4B model dropped the live `<memory>`/`<env>`/`<skills>` blocks and sessions wrote *no file at all*. A small model can't tell live runtime data from boilerplate. Safe form = compress only static rules, append live blocks unmodified. |
| **Task self-assessment + seeded todos** | Cut filesystem search to **≤1/leg vs 20+** in the baseline; reliable completion. | Structure *without depth* invited **premature closure** — a todos-only run closed an anomaly early and missed the real peak; needed a mandatory drill-down subtask to fix. |
| **Mid-run self-assessment (spiral breaker)** | A leg came in at **38 msgs / 20 tool calls vs a 61-msg baseline**, still correct. | Early false positive: `todos` updates tripped the nudge at `repeat_threshold:4` — now excluded from the spiral count (doc-default `5` also wouldn't trip). The nudge is **persisted** to history (earlier docs wrongly called it transient). |

### Field validation (cost-anomaly agent)

End-to-end task with ground truth, most recent evidence:

- **Capability jump:** from *"loops forever, fills the context window, never writes
  a report"* → *"produces ground-truth-accurate single-anomaly reports reliably"* —
  estimated **~60–70% of the way to a usable agent**.
- **Leaner/faster:** the patched build did **24 tool calls / 14.9 min** for a better
  report vs **38 calls / 29.3 min** for stock — but the comparison is **confounded**
  (the patched build also inherited experimental global config), so it isn't a clean
  binary-only delta.

> **Standing ceilings the field doc names.** (1) **Cumulative** context overflow is
> the new limit — a thorough run peaked **153,667 tokens over the 131,072 `num_ctx`**;
> the per-payload caps don't stop history growth. (2) `crush.json`'s
> `context_window` is **ignored** when Ollama's model cap is lower (262k configured,
> 131k effective). (3) Per-model `default_max_tokens` wasn't applied on the run path
> (effective ~2048 cap truncated reports). (4) Small models (qwen3:4b) **collapse
> agentic tasks into Q&A** — workers "answer the path" instead of calling the tool —
> so the agent effectively *requires* the large model. (5) A full multi-anomaly
> end-to-end synthesis has **never completed cleanly**.

The throughline: memory (parse-error elimination) and `compact_tools` (−15%, leaner)
are the clean wins; the self-assessment / spiral features show concrete tool-call
and message reductions; `compact_prompt`/`summarize_prompt` are marginal or risky as
measured. The biggest *capability* change isn't one knob — it's the combination
moving a run from "never finishes" to "finishes correctly and reliably."

---

## Task self-assessment (autonomous todo tracking)

**Problem.** Small models routinely declare victory with todos still open: they
write a partial deliverable, stop calling tools, and end the turn. Nothing pulls
them back to the unfinished work. (See journal §7 and report_10 — gemma4 wrote a
JSON blob when markdown was asked for and considered the task done.)

**What it does.** When `enable_task_self_assessment` is on, after a *successful*
run Crush inspects the session's todo list. If any todo is not `completed`, it
injects a follow-up user turn — a reminder — asking the model to close out the
work, then runs the agent again. With the default settings this fires once
(the historical behavior). With `task_self_assessment` tuning it **loops** until
the work is done or a cap is hit.

### The reminder prompt

Each reminder lists the **full** todo list (so the model can submit a correct
full replacement via the `todos` tool without clobbering completed entries) and
then calls out the still-open tasks **by name** in their own section:

```
Current task list:
- [completed] Wire up the parser
- [pending] Add the CLI flag
- [in_progress] Write the docs

Still unfinished:
- Add the CLI flag
- Write the docs

Assess each unfinished task: if the work was already completed this session,
mark it completed. If work is genuinely unfinished, complete it now. Use the
todos tool to submit the updated full list.
```

### Configuration

```jsonc
"options": {
  "enable_task_self_assessment": true,   // master on/off switch
  "task_self_assessment": {
    "max_reminders": 5,        // hard cap on reminders per run (loop guard)
    "target_completion": 1.0   // stop once this fraction of todos is completed
  }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `max_reminders` | `1` | Maximum follow-up reminders per run. Raise it to keep re-prompting until tasks close. Values `< 1` fall back to `1`. This is the loop guard — it bounds the worst case even if the model never closes its tasks. |
| `target_completion` | `1.0` | Reminders stop once this fraction (`0 < x ≤ 1`) of todos are `completed`. `1.0` means every task must close; `0.5` stops at half. Out-of-range values fall back to `1.0`. |

Omitting the `task_self_assessment` object keeps the original one-shot behavior
(`max_reminders: 1`, `target_completion: 1.0`).

### Loop semantics

On each iteration the loop re-fetches the session's todos (so it sees progress
the model made in the previous reminder) and stops as soon as **any** of these
is true:

1. completed fraction ≥ `target_completion`,
2. no incomplete todos remain,
3. `max_reminders` reached,
4. the session has no todos at all, or
5. the agent run returns an error.

Because the cap is checked first, `max_reminders` is always an upper bound on
extra turns — there is no runaway. The reminders reuse the same model settings
(max tokens, temperature, provider options) as the originating run.

### Sub-agent self-assessment

Self-assessment also runs for the spawned **Task sub-agent** (the `agent` tool
the coder delegates to), not just the top-level run. After a sub-agent's run
finishes, Crush inspects *that sub-session's* todos and loops the same way,
before propagating the sub-agent's cost back to the parent.

By default the sub-agent **inherits the global setting** — no extra config is
needed. Set `enable_task_self_assessment: true` and both the coder and the
sub-agent get reminders. The `subagent_*` keys exist only to configure the
sub-agent *differently* from the coder:

```jsonc
"options": {
  "enable_task_self_assessment": true,        // coder: on
  "task_self_assessment": { "max_reminders": 5 },

  "subagent_enable_task_self_assessment": false  // sub-agent: off (override)
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `subagent_enable_task_self_assessment` | inherit | `true`/`false` overrides the on/off switch for the sub-agent only. Unset = use `enable_task_self_assessment`. |
| `subagent_task_self_assessment` | inherit | Overrides the `{ max_reminders, target_completion }` tuning for the sub-agent only. Unset = use the global `task_self_assessment`. |

Resolution order for the sub-agent is **`subagent_*` override → global value →
built-in default**. The override is a per-agent value carried on the Task
agent's config; the coder agent never carries one, so it always resolves
directly against the global options.

### Cost note

Each reminder is a full model turn. On hosted/metered models this multiplies
cost; keep `max_reminders` low (or leave the feature off) there. It is most
useful for local models where turns are effectively free and the failure mode
(abandoned todos) is common.

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.EnableTaskSelfAssessment`,
  `Options.TaskSelfAssessment`, the per-sub-agent
  `Options.SubagentEnableTaskSelfAssessment` /
  `Options.SubagentTaskSelfAssessment` overrides, and the
  `TaskSelfAssessmentConfig` type. The per-agent override fields
  (`Agent.EnableTaskSelfAssessment` / `Agent.TaskSelfAssessment`) are populated
  for the Task agent in `SetupAgents`. Note `Config.Agents` is `json:"-"` and
  rebuilt by `SetupAgents` on every load — user config reaches the agents only
  through `Options`, which is why the sub-agent knobs live there.
- `internal/agent/coordinator.go` — `runTaskSelfAssessment` (the loop),
  `taskSelfAssessmentEnabled` / `taskAssessmentConfig` (per-agent resolution
  with global fallback), `resolveTaskAssessmentSettings` (defaults), and
  `buildTaskAssessmentPrompt` (the reminder text). The loop is invoked from
  `coordinator.Run` (top-level, coder config) and from `runSubAgent`
  (sub-agent, Task config) after a successful run.
- `internal/session/session.go` — `CompletedFraction` / `HasIncompleteTodos`.
- Tests: `internal/agent/coordinator_test.go` (resolution + sub-agent loop),
  `internal/config/agent_id_test.go` (SetupAgents wiring),
  `internal/session/session_test.go`.

---

## Mid-run self-assessment (tool-call spiral breaker)

**Problem.** Task self-assessment only fires *after* a run ends, so it can't
rescue a run that is spiraling in the middle — e.g. a model that keeps running
the same `grep`/`view` with slightly different arguments, getting nowhere, and
never stopping on its own. The built-in loop detector (`loop_detection.go`)
catches only *exact* repeats (same tool, same input, same output, >5 times in
10 steps) and its only move is to **abort** the run.

**What it does.** With `enable_midrun_self_assessment: true`, the run loop
watches tool usage over a sliding window of recent steps. When any single tool
is called at least `repeat_threshold` times within the window, it appends a
**one-shot** nudge to the conversation as a real (persisted) user message on the
next step — then lets the run continue. It is *not* a transient/vanishing
message (see "Persisted into history" below for why). The message carries the
run's tool-usage stats and asks the model to scope tighter or give up:

```text
Self-check: you appear to be repeating tool calls without making progress.
Recent tool usage: grep ×6, view ×2 (called grep 6 times).

Stop and reassess before the next call:
- Restate what you are trying to find and what you have learned so far.
- If the search is too broad or mis-scoped, narrow it: tighter query, specific paths/globs, or a different tool.
- If you have already tried several times without success, abandon this approach — proceed with what you know, or report that you could not find it rather than searching again the same way.
Do not repeat the same call with the same arguments.
```

```jsonc
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "enable_midrun_self_assessment": true,
    "midrun_self_assessment": {
      "window": 7,           // most recent *steps* inspected (default 7)
      "repeat_threshold": 5, // trip once one tool hits this count in the window (default 5)
      "max_injections": 5    // cap nudges per run (default 5)
    },
    // Optional: tune the sub-agent independently. Merges field-by-field over
    // the global tuning, so this only changes the window for the sub-agent.
    "subagent_midrun_self_assessment": { "window": 8 }
  }
}
```

> `window` is a count of **steps**, not minutes. It must be ≥ `repeat_threshold`
> or the nudge can never trip (you can't call a tool more times than there are
> steps in the window).

Notes:

- **Inject and continue**, not abort — it's a nudge, not a kill switch. The
  hard loop detector still backstops true infinite loops.
- **`todos` is never counted.** The task-tracking tool is legitimately called
  many times to mark work done, so it is excluded from the spiral count (see
  `spiralExcludedTools`) and can't trip the nudge.
- **Persisted into history, by design.** The nudge is appended as a real user
  message, *not* injected transiently. The agentic loop resends the whole
  conversation each step, and the prompt cache only pays off when each step
  shares a stable prefix with the last — so history must grow append-only. A
  transient, vanishing message (especially a system message, which Anthropic
  hoists into the cached system block) would rewrite the cached prefix and
  thrash it. The minor cost is that the nudge shows up in the transcript. See
  `sessionAgent.injectMidRunNudge` for the full rationale.
- **Bounded.** After each injection a full `window` of steps must pass before it
  can trip again, and `max_injections` caps the total per run.
- **Applies to both** the top-level coder and the spawned Task sub-agent — they
  share the run loop, so a spiraling sub-agent gets nudged too. The sub-agent
  can be tuned independently via the `subagent_*` keys, which **merge
  field-by-field** over the global values (set one knob, inherit the rest).

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.EnableMidRunSelfAssessment`,
  `Options.MidRunSelfAssessment`, the `subagent_*` overrides
  (`Options.SubagentEnableMidRunSelfAssessment` /
  `Options.SubagentMidRunSelfAssessment`), and the `MidRunSelfAssessmentConfig`
  type.
- `internal/agent/spiral_assessment.go` — `summarizeToolUsage` (windowed
  per-tool counts), `shouldAssessSpiral` (trip check), `buildSpiralAssessmentPrompt`
  (the nudge text), and `resolveMidRunAssessment` (defaults + field-by-field
  merge of sub-agent over global). `buildAgent` (coordinator.go) passes the
  sub-agent overrides only when `isSubAgent`.
- `internal/agent/agent.go` — detection in the `OnStepFinish` hook (accumulates
  steps, queues a nudge); injection in `PrepareStep` via
  `sessionAgent.injectMidRunNudge`, which persists the nudge as an appended user
  message (append-only → cache-safe; see its doc comment) and tracks the
  injection count and per-window cooldown.
- Tests: `internal/agent/spiral_assessment_test.go` — includes
  `TestInjectMidRunNudge_AppendsToHistoryCacheSafe` (asserts the prefix is
  untouched and history grows append-only) and `..._SubAgentMerging`.

---

## Budget nudges (context window + wall-clock)

**Problem.** Mid-run self-assessment catches a model *looping* on the same tool,
but a run can fail two other ways while making genuinely-different calls each
step: it can **fill the context window** (the prompt grows past `num_ctx`,
oldest messages get truncated, and the run produces nothing) or simply **run too
long**. The repeat detector never sees either coming — the calls aren't
identical. Observed live: a sub-agent issued slightly-varied `query_costs` calls
while its prompt grew to 187K tokens past a 131K `num_ctx`, overflowing before
it ever wrote findings.

**What they do.** Two independent nudges, each keyed on resource pressure rather
than repetition, each firing a softer **warn** at a threshold and a harder
**stop-and-write** at the ceiling. Like the spiral nudge they *inject and
continue* (they are not kill switches) and the injected message is persisted
append-only into history (cache-safe — see the mid-run section for why). Both
apply to the top-level coder and the spawned Task sub-agent, and both have
`subagent_*` overrides that **merge field-by-field** over the global tuning.

### Context budget (`enable_context_budget` / `context_budget`)

Watches `prompt + completion` tokens for the latest step against the model's
context window. When usage crosses `warn_percent` it tells the model to start
consolidating into its deliverable; past `hard_percent` it tells it to stop
investigating and write the deliverable now. Each threshold fires **once per
run**. **Inert when the model's context window is unknown** (`0`) — it can't
compute a percentage, so nothing fires (this is the common case for some
local/custom model entries; set the model's `context_window` to enable it).

```jsonc
"options": {
  "enable_context_budget": true,
  "context_budget": {
    "warn_percent": 0.70,   // soft "start wrapping up" nudge (default 0.70)
    "hard_percent": 0.85    // hard "stop and write now" nudge (default 0.85)
  }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `warn_percent` | `0.70` | Fraction (0–1] of the context window at which the soft nudge fires. Out-of-range → default. |
| `hard_percent` | `0.85` | Fraction (0–1] of the context window at which the hard nudge fires. Out-of-range → default. |

### Time budget (`enable_time_budget` / `time_budget`)

Watches wall-clock elapsed for the run against `budget_minutes`. At
`warn_percent` of the budget it asks the model to pace itself; once elapsed
reaches the full budget it forces a wrap-up. Each threshold fires **once per
run**. The budget is **per run** — per user turn for the coder, per spawn for a
sub-agent — so a `subagent_time_budget` is the natural place to bound individual
investigations. There is **no default `budget_minutes`**: the nudge stays inert
until you set it above `0`, even with `enable_time_budget: true`.

```jsonc
"options": {
  "enable_time_budget": true,
  "time_budget": {
    "budget_minutes": 10,   // wall-clock budget for one run (required; no default)
    "warn_percent": 0.75    // soft "pace yourself" nudge (default 0.75)
  },
  // e.g. give each spawned investigation a tighter ceiling than the coder:
  "subagent_time_budget": { "budget_minutes": 5 }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `budget_minutes` | — | Wall-clock budget for a single run, in minutes. `≤ 0` (or unset) → the nudge never fires. |
| `warn_percent` | `0.75` | Fraction (0–1] of the budget at which the soft nudge fires. The hard wrap-up nudge always fires at 100% of the budget. Out-of-range → default. |

### Notes

- **One nudge per step.** The context, time, and spiral nudges share a single
  injection slot, so at most one is injected on any step; if the slot is taken,
  a tripped budget threshold simply injects on the next step (nothing is lost —
  the fired-once flag is set only on injection). Budget nudges do **not** consume
  the spiral nudge's per-run injection cap or cooldown.
- **Pairs with the spiral nudge.** Enable all three to cover the three failure
  modes at once: *don't loop* (spiral), *don't overflow* (context), *don't run
  long* (time) — and in every case leave a written deliverable.

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.EnableContextBudget` /
  `Options.ContextBudget` / `Options.EnableTimeBudget` / `Options.TimeBudget`,
  the `subagent_*` overrides, and the `ContextBudgetConfig` / `TimeBudgetConfig`
  types.
- `internal/agent/budget_assessment.go` — `resolveContextBudget` /
  `resolveTimeBudget` (defaults + field-by-field sub-agent merge) and
  `buildContextBudgetPrompt` / `buildTimeBudgetPrompt` (the nudge text).
- `internal/agent/agent.go` — detection in the `OnStepFinish` hook (after the
  session usage is updated, so the latest token count is available); injection
  shares the spiral nudge's `pendingAssessment` slot and `injectMidRunNudge`.
  `buildAgent` (coordinator.go) resolves the settings and passes the sub-agent
  overrides only when `isSubAgent`.
- Tests: `internal/agent/budget_assessment_test.go`.

---

## Sub-agent tool access (`task_tools`)

**Problem.** The spawned Task sub-agent (the `agent` tool the coder delegates to)
should not automatically get the coder's full tool belt. By default it is
**read-only** — it can look around (`glob`/`grep`/`ls`/`sourcegraph`/`view`) but
cannot run shell commands, edit, or write. To let a sub-agent do real work (e.g.
run `bash`) you have to grant the tools explicitly.

**What it does.** `task_tools` is the allow-list of built-in tools the sub-agent
may call, plus the special token `mcp` to grant *all* MCP tools. When it is
unset, the sub-agent falls back to the read-only set. When it is set, it
**replaces** that default entirely — so listing `["bash"]` gives the sub-agent
*only* bash (not bash plus the read-only tools); you must list every tool you
want.

### How the intersection works

A tool reaches the sub-agent only if it survives **two filters**, applied in this
order:

1. **`disabled_tools` (exclude, global).** First the full built-in set is
   filtered down to the *enabled* set: `enabled = allTools − disabled_tools`.
   This applies to every agent, coder and sub-agent alike. A disabled tool is
   gone for good.
2. **`task_tools` (include, sub-agent only).** The sub-agent's tools are then the
   intersection of the enabled set with `task_tools`:
   `subagent = enabled ∩ task_tools`. If `task_tools` is unset, the sub-agent
   instead gets `enabled ∩ {glob, grep, ls, sourcegraph, view}` (the read-only
   default).

Because step 2 intersects against the *already-filtered* enabled set,
**`disabled_tools` always wins** — naming a tool in `task_tools` cannot bring
back something `disabled_tools` removed. Conversely, naming a tool the coder
never had does nothing: the intersection just drops it.

MCP access is separate from the built-in list: the sub-agent gets MCP tools only
if `task_tools` contains the literal `"mcp"` token. Without it the sub-agent has
**no** MCP access regardless of which built-in tools are listed (this preserves
the historical default).

```text
allTools ── minus disabled_tools ──▶ enabled set ──┐
                                                   ├─ ∩ task_tools (or read-only default) ─▶ sub-agent built-in tools
                                                   │
"mcp" in task_tools? ── yes ─▶ all MCP tools       │
                       no  ─▶ no MCP tools          (independent of the above)
```

### Examples

```jsonc
// Default (task_tools unset): sub-agent is read-only, no MCP.
"options": {}
// sub-agent → glob, grep, ls, sourcegraph, view

// Grant bash plus the usual editing belt, plus all MCP tools.
"options": {
  "task_tools": ["bash", "view", "ls", "glob", "grep", "edit", "write", "sourcegraph", "mcp"]
}
// sub-agent → bash, view, ls, glob, grep, edit, write, sourcegraph + all MCP

// disabled_tools wins over task_tools.
"options": {
  "disabled_tools": ["bash"],
  "task_tools": ["bash", "view"]
}
// sub-agent → view   (bash was removed in step 1 and can't come back)
```

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.TaskTools` (the field),
  `resolveAllowedTools` (step 1, `allTools − disabled_tools`),
  `resolveReadOnlyTools` (the read-only default set), `resolveTaskTools` (step 2,
  the intersection / default fallback), and `resolveTaskMCP` (the `mcp` token →
  all-or-nothing MCP). All are wired together in `SetupAgents`, which sets the
  Task agent's `AllowedTools` / `AllowedMCP`.
- `internal/proto/tools.go` — `BashToolName = "bash"` and the other built-in tool
  name constants used in `task_tools`.
- Tests: `internal/config/agent_id_test.go` (SetupAgents wiring).

---

## Memory (oversized tool-result spillover)

**Problem.** Large tool outputs (file reads, greps, command output) bloat the
prompt and get re-injected every turn, crowding out the real conversation and
pushing weak models past their parse-reliable context size.

**What it does.** When `enable_memory` is on (default), any tool result larger
than `memory_hard_limit_bytes` (plus the `memory_overspill` slack) is stored in
an in-process memory store. The model sees a short inline summary
(`memory_preview_lines` lines) plus a reference id, and can pull the full
content back on demand via `memory_scroll` / `memory_list` / `memory_grep`.

`memory_refuse_tools` flips the strategy for specific tools: instead of storing
the oversized result, the model gets an error telling it to re-call with
narrower parameters (e.g. `view` with `offset`/`limit`). Use this for tools
where a narrower call is cheap and a stored blob would just be re-scrolled.

```jsonc
"options": {
  "enable_memory": true,
  "memory_hard_limit_bytes": 8192,
  "memory_overspill": 0.20,
  "memory_preview_lines": 10,
  "memory_refuse_tools": ["view"]
}
```

Implementation: `internal/agent/memory/`.

---

## Prompt / tool-description compression

These three reduce the static token cost of every turn — most valuable for
models with context windows under ~64K.

- **`compact_tools`** — swaps verbose tool descriptions for terse ones
  (`memory_scroll`, `memory_list`, `memory_grep`, `file_write`, `file_edit`,
  `file_grep`). Pure prompt-size win, no behavior change.
- **`compact_prompt`** — uses a shorter coder system prompt that keeps all
  behavioral rules and tool names but drops examples and duplicated prose.
  Saves ~4,500–5,000 tokens. See
  [`TOOLING_IMPROVEMENTS_COMPACT_SYSTEM_PROMPT.md`](./TOOLING_IMPROVEMENTS_COMPACT_SYSTEM_PROMPT.md).
- **`summarize_prompt`** — async bootstrap that uses the *small* model to
  compress the system prompt at session start. The first turn uses the base
  prompt; later turns use the summarized version. Requires a configured small
  model.

`compact_prompt` and `summarize_prompt` stack: compact is the static baseline,
summarize compresses further at runtime.

---

## Overriding the system prompt (`prompt_paths`)

**Problem.** `compact_prompt` and `summarize_prompt` only *shrink* the built-in
coder prompt — they can't replace its content. When you run crush as an
**execution engine** (a fixed, non-interactive `crush run` pipeline) you often
want a fully custom, deterministic system prompt instead of the stock coder
persona, without forking and rebuilding to edit `coder.md.tpl`.

**What it does.** `prompt_paths` is a list of files that are read, concatenated
in order (separated by a blank line), and used as the coder system prompt **in
place of** the built-in template. It applies to the top-level **coder** agent and
is **inherited by the spawned Task sub-agent** unless the sub-agent has its own
`subagent_prompt_paths` (see below) — so a single `prompt_paths` makes an
execution-engine prompt apply end-to-end.

```jsonc
"options": {
  "prompt_paths": [
    "prompts/base.md",
    "prompts/house-rules.md",
    "prompts/output-contract.md"
  ],
  // Optional: give the sub-agent a DIFFERENT prompt. Omit to have it inherit
  // prompt_paths above.
  "subagent_prompt_paths": ["prompts/subagent.md"]
}
```

### Behavior

- **Concatenation order is the file order.** Files join with a single blank line
  between them. The result is the entire prompt.
- **Rendered as a Go `text/template`.** The concatenated content goes through the
  same template engine as the built-in prompt, so it can interpolate the same
  data — `{{.WorkingDir}}`, `{{.Platform}}`, `{{.Date}}`, `{{.GitStatus}}`,
  `{{.Model}}` / `{{.Provider}}`, `{{.AvailSkillXML}}`, and `{{range .ContextFiles}}`.
  Plain prose with no `{{…}}` directives passes through unchanged, so a raw
  Markdown file just works.
- **Auto-discovered context files are suppressed.** While the override is active,
  the `context_paths` files (`CLAUDE.md`, `CRUSH.md`, `AGENTS.md`,
  `.cursorrules`, …) are **not** appended — the injected files are the whole
  prompt. If you still want project context, reference it explicitly in one of
  your prompt files (e.g. include a `{{range .ContextFiles}}…{{end}}` block, or
  just paste the content). This keeps an execution-engine prompt fully
  self-contained and reproducible.
- **Paths resolve like context paths.** Relative paths join against the working
  directory; `~` and `$VARS` are expanded.
- **Missing files fail loudly.** A path that can't be read is a hard error at
  startup rather than a silent fallback to the built-in prompt — a typo in an
  engine config should stop the run, not quietly change the model's behavior.

### Precedence

`prompt_paths` (when non-empty) wins over `compact_prompt` — the compact built-in
variant is irrelevant once you supply your own prompt. `summarize_prompt` still
works *on top of* an override: the small model compresses whatever the resolved
base prompt is, including a custom one.

### Sub-agent prompt (`subagent_prompt_paths`)

The spawned Task sub-agent (the `agent` tool the coder delegates to) has its own
prompt, built from `task.md.tpl`, and its own override key:
`subagent_prompt_paths`. It has identical semantics to `prompt_paths` (concatenate
in order, render as a template, suppress context files, hard error on a missing
file) but applies only to the sub-agent.

The sub-agent's prompt resolves in this order:

1. **`subagent_prompt_paths`** — if set, the sub-agent uses these files.
2. **`prompt_paths`** — otherwise the sub-agent **inherits the coder's override**,
   so one `prompt_paths` makes an execution-engine prompt apply end-to-end (and
   the sub-agent gets the same context-file suppression as the coder, not the
   stock task persona).
3. **built-in `task.md.tpl`** — if neither is set.

So: set only `prompt_paths` → both coder and sub-agent use it; set only
`subagent_prompt_paths` → coder is stock, sub-agent is custom; set both → each
gets its own. Inheritance is one-way (coder → sub-agent): a sub-agent-only
override never changes the coder's prompt. The keys follow the existing flat
`subagent_*` convention (e.g. `subagent_enable_task_self_assessment`), not a
nested block.

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.PromptPaths` and
  `Options.SubagentPromptPaths` (the fields + schema).
- `internal/agent/prompt/prompt.go` — `ConcatPromptFiles` (read + concatenate,
  hard error on missing files) and `WithoutContextFiles` / the `skipContextFiles`
  flag honored in `promptData` (the context-file suppression).
- `internal/agent/prompts.go` — `promptFromFiles` (builds a prompt from the files
  with `WithoutContextFiles` applied; shared by both agents, `name` is
  `"coder"`/`"task"`) and `resolveSubagentPromptPaths` (the sub-agent resolution
  order: `subagent_prompt_paths` → `prompt_paths` → nil/built-in).
- `internal/agent/coordinator.go` — coder selection: `prompt_paths` →
  `compact_prompt` → full default.
- `internal/agent/agent_tool.go` — sub-agent selection via
  `resolveSubagentPromptPaths`, falling back to the built-in task prompt.
- Tests: `internal/agent/prompt/prompt_override_test.go` (the shared mechanism),
  `internal/config/prompt_paths_test.go` (config parsing), and
  `internal/agent/subagent_prompt_test.go` (sub-agent resolution order +
  one-way inheritance).

---

## Sub-agent roles (`subagent_roles` + `list_roles`)

`subagent_prompt_paths` gives the sub-agent **one** fixed prompt. `subagent_roles`
goes further: it defines **several** named, specialized sub-agent prompts and lets
the model choose which one to spawn per task.

```jsonc
{
  "options": {
    "subagent_roles": {
      "reviewer": ["prompts/roles/reviewer.md"],
      "explorer": ["prompts/roles/explorer.md"],
      "debugger": ["prompts/base.md", "prompts/roles/debugger.md"]
    }
  }
}
```

Each value has the **same semantics as `prompt_paths`** (files concatenated in
order, rendered as a Go template, context files suppressed, hard error on a
missing file). Multiple files per role are allowed.

**How the model uses it.** A role can reach the `agent` tool three ways:

1. The model calls **`list_roles`** to see the catalog, then passes the chosen name
   as the `agent` tool's `role` argument.
2. The system/role prompt or the user tells it to use a specific role; it just
   passes that `role`.
3. It passes no `role` and gets the default sub-agent.

**`list_roles`** is a read-only tool that returns the catalog as
`name: summary` lines (the summary is the first non-blank line of the role's first
file). It is granted **only to the coder, and only when `subagent_roles` is
non-empty** — so it never bloats the tool schema when the feature is unused, and it
never nests into sub-agents.

**Resolution per spawn:**

- `role` set and known → that role's prompt files.
- `role` empty → the normal default (`subagent_prompt_paths` → `prompt_paths` →
  built-in `task.md.tpl`).
- `role` set but unknown → the call returns an error listing the valid role names,
  so a wrong guess self-corrects without a separate `list_roles` round-trip.

**Concurrency.** Each role is a distinct `SessionAgent` engine, pre-built at tool
construction (the `agent` tool runs spawns in parallel, so a shared engine whose
prompt is swapped per call would race). Spawns are otherwise fully isolated — each
gets its own child session keyed by tool-call ID — so running several roles (or
several spawns of one role) concurrently does not cross-contaminate prompts,
context, or transcripts. Roles share only the parent session's cost accumulator,
whose update is serialized by a mutex (`coordinator.costMu`).

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.SubagentRoles` (field + schema), the
  `ListRolesToolName` constant, and the `SetupAgents` logic that grants
  `list_roles` to the coder only when roles are configured.
- `internal/agent/prompts.go` — `subagentRoles`, `sortedRoleNames`, and
  `buildSubAgentFor` (the shared prompt-build + `buildAgent` factory used by the
  default sub-agent and every role).
- `internal/agent/agent_tool.go` — the `role` param on `AgentParams`, the per-role
  pre-built engine map, and role selection at call time.
- `internal/agent/roles_tool.go` — the `list_roles` tool and `roleSummary`.
- Tests: `internal/agent/subagent_roles_test.go`.

---

## Streaming sub-agent output (`crush run`)

When the coder delegates work via the `agent` tool, that sub-agent runs in its
own *child session*. In the interactive TUI the child session is rendered on its
own, but in non-interactive mode (`crush run`) the stdout streamer only follows
the top-level session — so a sub-agent's reasoning and tool calls are invisible
and only its final result surfaces, folded into the parent's next message.

Set `stream_subagent_output: true` to also stream sub-agent assistant text to
stdout as it is produced:

```jsonc
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "stream_subagent_output": true
  }
}
```

Notes:

- **Default is off** — behaviour is unchanged unless you opt in.
- **Non-interactive only.** It has no effect in the interactive TUI, which
  already renders child sessions.
- **Works in both run paths** — the local in-process path
  (`App.RunNonInteractive`) and the client/server path (`crush run` against a
  running server). In the client/server path the top-level turn's own text is
  still reconciled from the authoritative `RunComplete` event (so a queued turn
  can't corrupt stdout); sub-agent sessions have no such correlator and stream
  live.
- **Completion is unaffected.** Only the message-rendering filter is widened.
  The run still exits solely on the *top-level* turn's completion — a
  sub-agent's own completion never terminates `crush run`.
- **Labeled output.** Because everything is merged onto one stdout stream, each
  switch between writers is annotated with a header on its own line —
  `[subagent <id>]` for a spawned sub-agent (where `<id>` is its tool-call id,
  which is stable per sub-agent and distinguishes parallel sub-agents) and
  `[main]` when output returns to the top-level agent. The top-level agent's
  *first* output is left unprefixed, so a run with no sub-agents is
  byte-identical to the unannotated stream. Example:

  ```text
  Looking into the failing tests now.
  [subagent toolu_01H…]
  Found a nil deref in parser.go:42 …
  [main]
  Fixed it — the parser now guards the empty case.
  ```

  In the common case the parent is blocked on the tool call while one sub-agent
  runs, so blocks don't overlap; with multiple *parallel* sub-agents their
  chunks can still interleave, but each chunk is attributed by the preceding
  header.

Detection uses the agent-tool session-ID format (`messageID$$toolCallID`)
produced by `session.CreateAgentToolSessionID` (see
`session.IsAgentToolSessionID`); the labeling lives in
`format.StreamPrefixer`.

---

## Relaxed tool parameters (fewer required fields)

Weak models frequently stall or mis-call a tool when a *required* parameter
carries no useful signal for the task — the canonical case is the `bash` tool's
`description` field. Small models would omit it (schema-invalid call → retry) or
burn tokens inventing a label before every command. Required fields should be
the ones the tool genuinely cannot run without.

The tool schemas are generated by reflection over the param structs in
`internal/agent/tools` (via `charm.land/fantasy`'s `schema.Generate`): a field is
emitted as **`required`** unless its JSON tag contains **`omitempty`**. So
relaxing a parameter is a one-tag change.

- **`bash.description` is optional.** `BashParams.Description`
  (`internal/agent/tools/bash.go`) carries `json:"description,omitempty"`, so the
  only required field for `bash` is `command`. An omitted description is handled
  everywhere it is used (the run metadata and the background-job label just get
  an empty string). This applies to the compact bash tool too — `CompactBashTool`
  only swaps the description *text*, it reuses the same `BashParams` schema.

If you add or relax a built-in tool parameter, keep `omitempty` in sync with
what the handler actually needs: validate the genuinely-required fields in the
handler (e.g. `bash` rejects an empty `command`) and mark everything else
`omitempty` so a weak model is never forced to supply filler.

---

## Built-in tool-call protections

These are always on (no config) and exist to stop weak models from wedging
themselves. Documented here for completeness; details and the benchmark
evidence are in the journal.

- **Result truncation → memory** — see Memory above; prevents context blow-up
  from a single huge result.
- **Ripgrep / path validation** — grep and friends reject stale or broken paths
  rather than silently returning nothing (journal §3).
- **Root-level glob protection** — a bare glob at the repo root is guarded so a
  model can't accidentally enumerate a 1000+ file tree (journal §4).
- **Large-write guarding** — oversized `write` payloads are a known failure
  vector for small models (journal §6).

---

## Related

- [`hooks/README.md`](./hooks/README.md) — user-defined shell hooks
  (`PreToolUse`), for deterministic control over tool calls (block, rewrite,
  inject context, auto-approve).
- [`TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md`](./TOOLING_IMPROVEMENTS_FOR_LOCAL_MODELS.md)
  — the benchmark journal these options came out of.
