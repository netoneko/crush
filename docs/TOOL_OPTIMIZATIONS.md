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
| `enable_task_self_assessment` | bool | `false` | After a run that ends with open todos, re-prompt the model to finish/close them. |
| `task_self_assessment` | object | — | Tuning for the reminders above (loop count + completion target). |
| `enable_memory` | bool | `true` | Spill oversized tool results into a queryable memory store instead of the prompt. |
| `memory_hard_limit_bytes` | int | `8192` | Byte threshold above which a tool result is stored in memory. |
| `memory_overspill` | float | `0.20` | Slack fraction kept inline before spilling (e.g. `0.20` → up to `hard_limit*1.2`). |
| `memory_preview_lines` | int | `10` | Lines shown in the inline reference summary. |
| `memory_refuse_tools` | []string | — | Tools that reject oversized output (model must re-call narrower) instead of storing it. |
| `compact_tools` | bool | `false` | Use short tool descriptions to save prompt tokens. |
| `compact_prompt` | bool | `false` | Use a shorter system prompt (rules preserved, examples stripped). |
| `summarize_prompt` | bool | `false` | Async: compress the system prompt with the small model at session start. |

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

### Cost note

Each reminder is a full model turn. On hosted/metered models this multiplies
cost; keep `max_reminders` low (or leave the feature off) there. It is most
useful for local models where turns are effectively free and the failure mode
(abandoned todos) is common.

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.EnableTaskSelfAssessment`,
  `Options.TaskSelfAssessment`, and the `TaskSelfAssessmentConfig` type.
- `internal/agent/coordinator.go` — `runTaskSelfAssessment` (the loop),
  `resolveTaskAssessmentSettings` (defaults), and `buildTaskAssessmentPrompt`
  (the reminder text). The loop is invoked from `coordinator.Run` after a
  successful run.
- `internal/session/session.go` — `CompletedFraction` / `HasIncompleteTodos`.
- Tests: `internal/agent/coordinator_test.go`,
  `internal/session/session_test.go`.

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
