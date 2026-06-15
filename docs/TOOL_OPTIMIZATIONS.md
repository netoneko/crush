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
| `enable_memory` | bool | `true` | Spill oversized tool results into a queryable memory store instead of the prompt. |
| `memory_hard_limit_bytes` | int | `8192` | Byte threshold above which a tool result is stored in memory. |
| `memory_overspill` | float | `0.20` | Slack fraction kept inline before spilling (e.g. `0.20` → up to `hard_limit*1.2`). |
| `memory_preview_lines` | int | `10` | Lines shown in the inline reference summary. |
| `memory_refuse_tools` | []string | — | Tools that reject oversized output (model must re-call narrower) instead of storing it. |
| `compact_tools` | bool | `false` | Use short tool descriptions to save prompt tokens. |
| `compact_prompt` | bool | `false` | Use a shorter system prompt (rules preserved, examples stripped). |
| `summarize_prompt` | bool | `false` | Async: compress the system prompt with the small model at session start. |
| `prompt_paths` | []string | — | Files concatenated (in order) to **fully override** the coder system prompt. Rendered as a Go template; suppresses auto-discovered context files. Overrides `compact_prompt`. |
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
place of** the built-in template. It applies to the top-level **coder** agent
(the spawned Task sub-agent keeps its own `task.md.tpl`).

```jsonc
"options": {
  "prompt_paths": [
    "prompts/base.md",
    "prompts/house-rules.md",
    "prompts/output-contract.md"
  ]
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

### Where it lives (for maintainers)

- `internal/config/config.go` — `Options.PromptPaths` (the field + schema).
- `internal/agent/prompt/prompt.go` — `ConcatPromptFiles` (read + concatenate,
  hard error on missing files) and `WithoutContextFiles` / the `skipContextFiles`
  flag honored in `promptData` (the context-file suppression).
- `internal/agent/prompts.go` — `coderPromptFromFiles` (builds the coder prompt
  from the files with `WithoutContextFiles` applied).
- `internal/agent/coordinator.go` — selection logic: `prompt_paths` →
  `compact_prompt` → full default.
- Tests: `internal/agent/prompt/prompt_override_test.go`.

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
