# System Prompt Compression Flow

This document details the architectural flows for system prompt compression in Crush. It compares the original asynchronous flow (which contained a race condition and sub-agent overhead) with the new coordinated, synchronous flow.

---

## 1. Overview

System prompt compression optimizes token usage by utilizing a smaller, cost-effective LLM to summarize and compress the extensive system prompt template before running the primary large model.

* **Original System Prompt:** Highly detailed, containing full instructions, examples, and tool specifications.
* **Compressed System Prompt:** A high-density version preserving only strict constraints, tool names, and structural blocks (`{{...}}`), omitting verbose prose.

---

## 2. The Regular (Original) Flow

In the original implementation, prompt compression was treated as a detached, "fire-and-forget" background goroutine. 

### Key Characteristics
1. **Race Condition:** The `summarizeSystemPrompt` call ran in a separate goroutine (`go c.summarizeSystemPrompt(...)`) outside of the startup synchronizer (`readyWg`). As a result, the primary agent initialized and responded to the user's first prompt *before* the compression finished. The first turn went out with the expensive, uncompressed system prompt.
2. **Sub-agent Overhead:** Sub-agents running the small `task.md.tpl` template (~977 bytes) triggered prompt summarization anyway, incurring unnecessary latency and small-model API costs with zero token-saving benefits.

### Diagram: Original Flow (Race Condition & Sub-agent Waste)

```
[Main Conversation Start]
          │
          ▼
   [Coordinator]
    Builds Agent
          │
          ├──(readyWg.Go)──► Build System Prompt
          │                        │
          │                        ▼
          │                  SetSystemPrompt(UNCOMPRESSED)
          │                        │
          │         ┌──────────────┴──────────────┐
          │         ▼                             ▼
          │  go summarizePrompt()          readyWg.Wait() (Finishes immediately)
          │         │                             │
          │         │ (Async Small Model call)     ▼
          │         │                      [First User Message]
          │         │                             │
          │         │                             ▼
          │         │                       [Large Model Turn 1]
          │         │                   Sends expensive UNCOMPRESSED prompt
          │         ▼                             │
          │  SetSystemPrompt(COMPRESSED)          │
          │         │                             ▼
          │         └────────────────────► [Large Model Turn 2+]
          │                             Sends cheap COMPRESSED prompt
          │
          ▼
  (Spawns Sub-agent)
          │
          ▼
   [Sub-agent]
    Builds Agent
          │
          └──(readyWg.Go)──► Build System Prompt (~977 bytes)
                                   │
                                   ▼
                             go summarizePrompt() ◄── [REDUNDANT OVERHEAD]
                             (Incurs API call cost for tiny prompt)
```

---

## 3. The New Flow with Compression

The new flow addresses these limitations by integrating prompt compression directly into the agent builder's startup task group (`readyWg`), blocking execution until compression finishes, and completely bypassing it for sub-agents.

### Key Characteristics
1. **Synchronous Blocking:** The `summarizeSystemPrompt` method is called synchronously within the `readyWg` group. The coordinator blocks on `readyWg.Wait()` before allowing the first message turn to begin, guaranteeing that the first message utilizes the compressed prompt.
2. **Compressing Notification:** To keep the user interface responsive during this startup step, the coordinator publishes a `TypeSystemPromptCompressing` notification to indicate that compression has started.
3. **Sub-agent Bypass:** Compression logic explicitly checks `!isSubAgent`. Sub-agents immediately skip compression and proceed with their lightweight tasks.

### Diagram: New Coordinated Flow

```
[Main Conversation Start]
          │
          ▼
   [Coordinator]
    Builds Agent
          │
          ├──(isSubAgent == true?) ──► YES ──► Skip Compression
          │                                          │
          └──(isSubAgent == false) ──► NO ───────────┤
                                                     ▼
                                              [readyWg.Go]
                                                     │
                                                     ▼
                                             Build System Prompt
                                                     │
                                                     ▼
                                        Publish Event:
                                  "system_prompt_compressing"
                                                     │
                                                     ▼
                                        [summarizeSystemPrompt]
                                        (Sync Small Model Call)
                                                     │
                                                     ▼
                                         SetSystemPrompt(COMPRESSED)
                                                     │
                                                     ▼
                                               readyWg.Wait()
                                         (Blocks first user send)
                                                     │
                                                     ▼
                                            [First User Message]
                                                     │
                                                     ▼
                                            [Large Model Turn 1+]
                                         Sends cheap COMPRESSED prompt
```

---

## 4. Flow Comparison Summary

| Metric / Behavior | Original Flow | New Flow |
| :--- | :--- | :--- |
| **First Message Prompt Size** | Expensive / Uncompressed (Race Condition) | High Density / Compressed (Synchronous) |
| **Sub-agent Execution** | Triggers redundant small-model compression | Skips compression (Immediate execution) |
| **UI State Visibility** | Only emitted `TypeSystemPromptCompressed` | Emits both `Compressing` and `Compressed` events |
| **First-Turn Token Cost** | High | Low |
| **Startup Behavior** | Unpredictable / Concurrent | Deterministic / Synchronous |

---

## 5. Empirical Findings (akuma acceptance-test runs, 2026-05-30)

These findings come from `akuma/.crush/logs/crush.log`, which recorded several consecutive acceptance-test sessions against the `02_git_clone.md` playbook using different prompt configurations.

### 5.1 Compression quality observed in the wild

The small model (`qwen3:4b`) was used to compress the main agent's system prompt (~16,300 bytes). Results varied significantly across runs:

| Run | Original (bytes) | Compressed (bytes) | Reduction | Notes |
| :--- | ---: | ---: | ---: | :--- |
| A | 16,297 | 2,652 | 84% | Structured output, all sections preserved in condensed form |
| B | 16,297 | 2,734 | 83% | Similar to A |
| C | 16,297 | 2,514 | 84% | Similar to A |
| D | 16,297 | 1,222 | 92% | Sections present but stripped further |
| E | 16,297 | 979  | 93% | Minimal section headers, sparse content |
| F | 16,352 | 500  | 96% | **Catastrophic**: single paragraph, all structural context gone |
| — | 960   | 127–652 | 32–86% | Sub-agent task.md — compressed despite being tiny (pre-fix) |

Additionally, 3 runs returned **empty responses** from the small model (silently fell back to the uncompressed prompt), and 1 run failed with `model 'qwen3:4b' not found`.

### 5.2 What the 84% version preserved

The 2,652-byte output (run A) retained structure:

- All 15 numbered `<critical_rules>` — condensed prose, but all present
- `<communication_style>`, `<workflow>`, `<decision_making>`, `<editing_files>`, `<error_handling>`, `<task_completion>` sections — abbreviated but structurally intact
- `<env>` block (working directory, git branch, date) — present in condensed form
- `<available_skills>` with skill names and locations — present
- `<skills_usage>` activation instructions — condensed but correct

What it **silently dropped**:

- The full `<memory>` block containing the embedded `CLAUDE.md` and `GEMINI.md` file contents. These were reduced to a one-line note: *"Update memory files for build/test/lint commands, code style, project patterns."* — stripping all actual project-specific instructions.

### 5.3 What the 96% version (500 bytes) lost vs the 84% version

The 500-byte output was a **single dense paragraph** with no structural context:

> *You are Crush (CLI). Read context (exact format). Autonomous: search, act; try alternatives until blocked. Test after changes. Output <4 lines. Exact matches. Never commit without user instruction. Follow memory instructions. No code comments (user asks). Security first. No URL guessing. Never push remote. Never revert. Use 'edit' or 'multiedit' (not apply_patch). If skill description matches, view SKILL.md first. Limit file reads. Tools: edit, multiedit, write. Code refs: file_path:line_number.*

Compared to the 84% version, the 96% version additionally lost:

| Lost element | Impact |
| :--- | :--- |
| `<env>` block (working dir, git branch, date) | Model does not know its own working directory or git context |
| `<available_skills>` + `<skills_usage>` | Model cannot discover or load SKILL.md files |
| All labeled `<section>` structure | Rules collapse into ambiguous prose; ordering and precedence are implicit |
| Rule details and nuance | e.g., `never call job_output with wait:true on QEMU` → collapsed to "try alternatives until blocked" |

### 5.4 The root cause of quality loss: static vs. dynamic sections

The compress meta-prompt instructs the small model to:

> *Rewrite the system prompt below into the most concise version possible that preserves all behavioral rules, tool names, constraints, and dynamic template blocks (`{{...}}`). Remove all examples, redundant prose, and repeated explanations.*

The problem is that `<memory>`, `<env>`, and `<available_skills>` are **runtime-injected context**, not "examples or redundant prose". They contain:
- The actual project `CLAUDE.md` with build commands and hard constraints (e.g., *"Never glob or list the repo root — it has 1000+ files"*)
- The working directory and current git branch
- The resolved skill file locations

These blocks do not contain `{{...}}` template variables because they are pre-rendered before the prompt reaches the compressor. A 4B-parameter model given the instruction "remove redundant prose" will strip project documentation that looks like boilerplate, because it cannot distinguish live runtime data from static examples.

### 5.5 Behavioral impact on acceptance test runs

Three acceptance-test sessions ran the `02_git_clone.md` playbook:

| Session | System prompt | Report produced | Outcome |
| :--- | :--- | :--- | :--- |
| report_23 (84% compressed, 2,652 B) | Compressed — `<memory>` dropped | **No file written** (57 messages, session abandoned) | Likely globbed the repo root; lost track of SSH polling pattern |
| report_24 (96% compressed, 500 B) | Compressed — env + memory + skills dropped | **No file written** (34 messages, session cut short) | VM was already running from a previous session; model confused about state |
| report_25 (uncompressed, 16,296 B) | Full prompt | `02_git_clone_report_25.md` ✅ ALL PASSED | Clean execution, correct SSH wait loop, proper VM management |

The uncompressed run succeeded on the first attempt. Both compressed runs produced no output.

### 5.6 Recommendation: preserve dynamic sections verbatim

The compress meta-prompt needs to explicitly protect runtime-injected sections from compression. One approach:

```
You are a prompt compressor. Rewrite the following system prompt to be maximally concise
while preserving all behavioral rules, tool names, and constraints.

IMPORTANT: The following XML sections must be reproduced VERBATIM — do NOT summarize,
shorten, or remove them: <env>, <memory>, <available_skills>, <skills_usage>.

Remove examples, redundant prose, and repeated explanations from all other sections.
Output only the rewritten prompt.
```

A more robust long-term approach is to split compression at the prompt-builder level:
1. Compress only the **static behavioral rules** (all `<critical_rules>` + section prose) — this part never changes and can be pre-cached.
2. Append `<env>`, `<memory>`, `<available_skills>`, and `<skills_usage>` **after** the compressed rules, unmodified.

This eliminates the risk of a small model dropping live runtime context while still achieving the token savings on the large static portion of the prompt.
