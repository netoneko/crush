# Tooling Improvements for Local Models

Observations from running crush with local ollama models (qwen3 35B MoE, gemma4 4B/26B) on agentic
acceptance test playbooks. Local models have smaller effective context windows and slower inference
than cloud APIs, so context bloat and tool output size matter more.

All results greater than X rows/tokens/size go into memory to create a reference and the LLM gets a snippet and a sample and can query the memory later by scrolling them via dedicated tools

## Fix

Crush stores large tool results (logs, grep hits, command output) in a memory
store so the model receives a compact reference inline and can page through the
full content on demand via `memory_list` / `memory_scroll`. This caps the byte
cost of any single tool result and stops stale output from bloating subsequent
turns.

Phase 1 (in-memory map) is implemented. Phase 2 (SQLite, cross-session) is
planned.

## Backends

### SQLite ( MemoryDB ) — second phase (shoud work across sessions)

  An in-memory SQLite database with typed tables ( logs ,  spans ,  code ,
  generic ,  results ). Tool results are parsed into structured rows on
  ingest, enabling SQL queries with  WHERE ,  GROUP BY ,  JOIN ,  REGEXP ,
  etc.

  Exposed tools:

*  query_memory  — run a  SELECT  query against stored data
*  list_tables  — show table schemas and row counts

  When to use: best for large models (Claude, GPT-4) that can write SQL
  reliably.

  Enabled when:  EnableMemory=true  and  EnableMemorySQL=true  (both default
  to  true ).

### InMemory — **implemented** (`internal/agent/memory/`)

A `sync.RWMutex`-protected map. Contents are lost when the process exits.

Every tool's response is intercepted by a wrapper (`WrapWithMemory`). If the
response text exceeds the configured threshold the full content is stored and
the model receives a compact summary instead:

```
[Large result stored as memory reference mem_42]
Source: bash | Kind: generic | Bytes: 12483 | Lines: 847

Preview (first 10 lines):
---
...
---

Use memory_scroll(id="mem_42", offset=0, limit=50) to read more.
```

Exposed tools:

- `memory_list` — list all stored references (ID, source, kind, line count, first-line preview)
- `memory_scroll` — read a window of lines from a reference (`id`, `offset`, `limit ≤ 200`)

Configuration (`options` block in `crush.json`):

| Key | Default | Description |
|-----|---------|-------------|
| `enable_memory` | `true` | Toggle the feature on/off |
| `memory_hard_limit_bytes` | `8192` | Byte length above which results are stored |
| `memory_overspill` | `0.20` | Fraction of the hard limit tolerated inline (20 % → effective limit 9830 B) |
| `memory_preview_lines` | `10` | Lines shown in the inline reference summary |

The store is created once per coordinator and persists across model switches
within a session. Reference IDs (`mem_N`) remain valid for the lifetime of the
process.

## 1. Tool result truncation (scrolling window)

**Problem:** Tool outputs are stored in full and replayed into every subsequent turn. A single
`job_output` or `bash` result returning hundreds of log lines gets re-injected for the rest of the
session. By turn 5–10 of an agentic session, the majority of the context is stale tool history.

**Fix:** Cap tool result size before storing, or truncate older results when building the prompt.
A simple approach: keep the first N + last N tokens of each tool result (preserving context header
and recent output). A more sophisticated approach slides a window over the full tool history,
dropping results from turns older than K.

Prior art: an investigative agent implementation with a dedicated memory layer (explicit
store/recall tool) alongside scrolling truncation for ephemeral results improved stability
noticeably — the model stopped attending to stale intermediate state.

**Suggested threshold:** ~2K tokens per tool result for log-style output; file reads can be larger
since they're referenced repeatedly.

---

## 2. read_files deduplication / staleness

**Problem:** crush re-injects the full content of every file in the `read_files` table on every
turn, regardless of whether the file is still relevant. A file read in turn 1 for context-gathering
stays in the prompt through turn 20. This is a second axis of bloat independent of tool results.

**Fix:** Evict `read_files` entries after N turns since last access, or allow the model to
explicitly mark a file as "no longer needed." Alternatively, summarize file contents into a
one-paragraph digest after the first use.

---

## 3. Ripgrep invoked with stale/broken paths

**Problem:** The glob tool builds its ripgrep invocation from directory listings that may include
symlinks to deleted paths. When symlinks are broken, ripgrep exits with status 2 and the tool falls
back to doublestar glob — but the error message (listing all broken paths) is itself large and gets
stored as a tool result.

**Fix:** Pre-filter symlinks before passing paths to ripgrep, or suppress the broken-symlink errors
(`--no-messages` flag) so fallback is silent.

---

## 4. Root-level glob protection

**Problem:** Models occasionally list or glob the repo root directory, which can return 1000+ files.
crush surfaces this as a truncated listing, but even the truncated version is large and the model
sometimes retries with a broader glob trying to find a file it could have located with a targeted
search.

**Fix:** Warn or soft-block glob/ls calls at the repo root when the result would exceed a threshold
(e.g. 200 entries). Suggest a more specific path in the error response. This is also addressable
via CLAUDE.md instructions but a guardrail in the tool itself would be more reliable.

---

## Benchmark Run — 2026-05-29 (report_3)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_3.md`  
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, fully in VRAM ~29 GB)  
**Inference speed:** ~20 tok/s  
**Result:** PASS

### Timing

| Metric | Value |
|--------|-------|
| Session duration | 18.2 min |
| LLM requests | 41 |
| Avg response time | 18.8 s |
| Min / Max response | 0.0 s / 175.9 s |
| Total LLM time | ~772 s (~12.9 min) |

The max 175 s response was the initial context load (full playbook + system prompt on first turn).

### Tool call breakdown (92 total)

| Tool | Calls |
|------|-------|
| `view` | 35 |
| `bash` | 31 |
| `todos` | 18 |
| `grep` | 6 |
| `write` | 2 |

`memory_list` / `memory_scroll`: **0** — memory was not used this run (config bug: `enable_memory` was at the wrong nesting level in `crush.json`; fixed after the run).

### Observations

- **Todos worked well.** Qwen autonomously created and ticked off a todo list without being asked, keeping itself on track through the 6-step playbook.
- **SSH known_hosts friction.** Each run generates a new VM host key. The model handled it by running `sed -i '' '83d' ~/.ssh/known_hosts`, but had to detect the failure first (RC=255). This cost at least 2 extra bash roundtrips per run.
- **Heavy use of `view`.** 35 `view` calls vs 31 `bash` — the model read files it had already seen (playbook, disk scripts, bootstrap layout). File read deduplication (#2 above) would cut context significantly.
- **Memory not triggered.** With the default 8 KB threshold and bash outputs being short (apk stdout, ls listings), nothing crossed the threshold even if the config had been correct. A 2 KB threshold would have caught several outputs.
- **No thinking.** `qwen3-yolo` does not emit `<think>` tags; the `think` config flag has no effect for Ollama-served models (only wired for specific cloud providers).

### Improvements for next run

1. Fix SSH known_hosts: use `-o UserKnownHostsFile=/dev/null` in all acceptance test SSH calls to eliminate the RC=255 noise.
2. Verify memory is active: check for `[Large result stored as memory reference]` lines in the model's received tool results.
3. Lower `memory_hard_limit_bytes` to 512–1024 to trigger offloading on typical bash/ls output.
4. Add a `memory_overspill: 0` option so nothing is tolerated inline above the threshold.

---

## Benchmark Run — 2026-05-29 (report_4, first run with config fix)

**Task:** Same playbook, new session after `enable_memory` config was moved into `options` block.  
**Result:** PASS

### Comparison: report_3 vs report_4

| Metric | Report 3 (baseline) | Report 4 | Delta |
|--------|---------------------|----------|-------|
| Session duration | 18.2 min | 8.1 min | **−55%** |
| LLM requests | 41 | 17 | −59% |
| Total tool calls | 92 | 46 | −50% |
| Avg response time | 18.8 s | 14.0 s | −26% |
| Max response time | 175.9 s | 48.8 s | −72% |
| Total LLM time | ~772 s | ~238 s | −69% |
| `view` calls | 35 | 15 | −57% |
| `bash` calls | 31 | 14 | −55% |
| `todos` calls | 18 | 1 | −94% |
| `ls` calls | 0 | 14 | +14 |
| `grep` calls | 6 | 6 | 0 |
| Memory tool calls | 0 | 0 | — |

### Observations

- **Massive speed improvement** despite memory not being active in either run. The model was more direct in report 4, skipping a lot of redundant file reads and todo management.
- **Todos dropped from 18 → 1.** The model barely used the todo list this time. Either context from the prior run informed a more confident plan, or it was a different random seed. Worth watching across more runs.
- **`ls` appeared (14 calls) where report 3 used none.** The model switched strategy for directory exploration — possibly more efficient for short listings than `view`.
- **Max response time dropped from 175 s → 48 s** — the cold-start context penalty was much smaller, suggesting the system prompt / message history was leaner at session start.
- **Memory still not triggered.** Config fix was applied but crush was already running; the new config takes effect on the next process start. Need to verify in a fresh session.

---

## 5. Small model name validation at startup

**Problem:** crush.json `models.small` can reference a model that doesn't exist in ollama. The
failure is silent — title generation falls back to the large model without surfacing an error to the
user. This wastes a large-model inference call on a trivial task every session.

**Observed:** `gemma4:yolo-4b` was configured but the model was created as `gemma4-yolo-4b`
(colon vs hyphen). Not caught until the log showed `model 'gemma4:yolo-4b' not found`.

**Fix:** Validate configured model names against the provider at startup and warn if any are
missing.
