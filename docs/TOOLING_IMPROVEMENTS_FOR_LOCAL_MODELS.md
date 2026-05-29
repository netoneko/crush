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

---

## Benchmark Run — 2026-05-29 (gemma4-yolo baseline, upstream crush, no memory)

**Task:** Acceptance playbook `acceptance/01_verify_apk_bootstrap.md`  
**Model:** `gemma4-yolo:latest` (Gemma 4 27B Q4, ~21.9 GiB VRAM)  
**Crush build:** Unpatched upstream — no tool result truncation, no memory offloading  
**Result:** STUCK / FAIL (tool call parse error, session did not complete)

### Timing

| Metric | Value |
|--------|-------|
| Session duration | ~6 min (04:57–05:03 local) |
| LLM requests | 14 (incl. 2 setup: title + initial prompt) |
| Prompt at session start | 46,128 tokens |
| Prompt at failure | 63,535 tokens |
| Context growth | +17,407 tokens (+37.7%) across 12 turns |

### Per-turn prompt size and response time

| Turn | Prompt (tokens) | Response time | Notes |
|------|-----------------|---------------|-------|
| 1 | 827 | 1.4 s | Title generation (small model) |
| 2 | 46,128 | 1.4 s | Initial system prompt load |
| 3 | 46,334 | 2.3 s | |
| 4 | 48,764 | 20.2 s | First slow turn |
| 5 | 49,754 | 5.9 s | |
| 6 | 49,997 | 3.0 s | |
| 7 | 50,818 | 6.0 s | |
| 8 | **55,527** | **34.0 s** | +4,709 spike — large bash/file result injected |
| 9 | 55,861 | 5.8 s | |
| 10 | 56,125 | 13.2 s | |
| 11 | **60,561** | **28.1 s** | +4,436 spike — another large result injected |
| 12 | 60,988 | 15.6 s | |
| 13 | 62,034 | 32.1 s | |
| 14 | **63,535** | **61.0 s** | **Parse failure — session stuck** |

### Root cause: gemma4 tool call parse failure under context pressure

At turn 14 (63,535 tokens), `gemma4-yolo` generated malformed output that mixed:

1. Reasoning/thought content (`</div></i></code></thought>`)
2. Two concatenated tool calls (`call:bash{...}`) separated by HTML channel markers (`<channel|><|tool_call>`)
3. A large multi-paragraph string argument inside the outer tool call JSON, causing the JSON decoder to hit `"invalid character 'm' after object key:value pair"` (`gemma4.go:293`)

Crush logged a `WARN` but has no retry or recovery path for parse errors — the session appeared frozen to the user.

### Context bloat mechanics (no memory = stale reinjection)

Without memory offloading, every turn replays the full accumulated tool result history. The two +4K spikes at turns 8 and 11 correspond to large bash outputs (SSH interaction transcripts, disk script reads) being injected fresh into every subsequent prompt. By turn 14, the model was carrying ~17K tokens of stale intermediate context on top of a ~46K base, pushing it past the point of reliable structured output.

### Earlier gemma4:e4b runs (00:56–01:00 session)

An earlier session used `gemma4:e4b` (4B variant, `num_ctx=32768`). Prompt grew from 46,066 to 55,205 in ~6 requests, hitting the context window limit faster. The smaller context meant even less headroom for tool result accumulation.

### Observations

- **Context pressure → parse failure.** gemma4 loses structured tool-call discipline under high context load. This is the direct cause of the stuck session.
- **Response time is a leading indicator.** The correlation between prompt size and response time is strong: turns 8 (34 s), 11 (28 s), 13 (32 s), 14 (61 s). Monitoring response time can signal impending failure.
- **Two large context spikes, not gradual growth.** The bloat comes from a small number of oversized tool results replayed across all subsequent turns — exactly the pattern the memory offloading feature was designed to prevent.
- **No recovery on parse error.** Crush silently drops the bad response and leaves the session hung. A retry with a simplified/truncated prompt, or a user-facing error, would be better.

### Improvements for next run

1. **Apply memory patch** — even the in-memory phase with `memory_hard_limit_bytes: 2048` would have capped the two large spikes and likely prevented the parse failure.
2. **Add parse-error recovery** — on `gemma4 tool call parsing failed`, retry once with the last assistant turn stripped or replaced with a brief error message, rather than silently hanging.
3. **Monitor response time as a health signal** — if response time exceeds 3× the session average, log a warning.
4. **gemma4 appears more brittle than qwen3 at high context.** qwen3-yolo ran the same playbook to completion at 71,856 tokens with no parse errors. gemma4 failed at 63,535. Consider a tighter context budget for gemma4 (e.g. force a hard cap or eviction at 50K tokens).

---

## Benchmark Run — 2026-05-29 (report_7, cold-cache baseline, no memory)

**Task:** Same playbook  
**Model:** `gemma4-yolo:latest`  
**Crush build:** Patched build, but `enable_memory: false` in config (memory disabled by accident)  
**KV cache:** Cold (different prompt structure busted the cache from report_6)  
**Result:** PASS

### Timing

| Metric | Value |
|--------|-------|
| Session duration | ~4 min 40 s (05:08–05:12) |
| LLM requests | 15 (incl. 2 setup) |
| Prompt at start | 46,128 tokens |
| Prompt at completion | 62,935 tokens |
| Context growth | +16,807 tokens (+36.4%) |

### Per-turn prompt size and response time

| Turn | Prompt (tokens) | Response time | Notes |
|------|-----------------|---------------|-------|
| 1 | 827 | 1.3 s | Title generation |
| 2 | 46,128 | **86 s** | **Cold KV cache** — 11,165 tokens processed from scratch |
| 3 | 46,334 | 2.3 s | |
| 4 | 48,764 | 21 s | |
| 5 | 49,783 | 6 s | |
| 6 | 50,049 | 3 s | |
| 7 | 50,870 | 6 s | |
| 8 | **55,574** | **33 s** | +4,704 spike |
| 9 | 55,904 | 6 s | |
| 10 | 56,178 | 17 s | |
| 11 | 58,738 | 14 s | +2,560 |
| 12 | 58,999 | 4 s | |
| 13 | 59,181 | 22 s | |
| 14 | 61,016 | 30 s | +1,835 |
| 15 | 62,935 | 11 s | Session complete — no parse failure |

### Observations

- **Passed despite same token range as report_6 failure.** Report_6 (hot cache) failed at 63,535; report_7 (cold cache) completed at 62,935. Likely random — gemma4's structured output reliability in the 60–64K range is nondeterministic. Not safe to treat cold start as a reliability improvement.
- **Cold cache cost: 86 seconds on turn 2.** The first large prompt (46,128 tokens) took 86 s vs ~1.4 s hot. One-time penalty per fresh process; subsequent turns hit the cache normally. Plan for it in session duration estimates.
- **Context growth pattern identical to report_6.** Same two large spikes (+4,704 at turn 8, matching +4,709 in report_6). Memory offloading remains necessary to change this curve.
- **Memory system prompt overhead.** Report_8 (memory enabled) starts at 47,058 tokens vs 46,128 — 930-token overhead from memory tool descriptions added to the system prompt.

---

## Benchmark Run — 2026-05-29 (report_8, memory enabled, aborted)

**Task:** Same playbook  
**Model:** `gemma4-yolo:latest`  
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`  
**Result:** ABORTED — host disk filled up mid-run (Docker holding deleted file handles; freed on Docker restart). No usable data.

---

## Benchmark Run — 2026-05-29 (report_9, memory enabled, Docker failure mid-session)

**Task:** Same playbook  
**Model:** `gemma4-yolo:latest`  
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`  
**Result:** SUBPAR — report written but task quality poor; session derailed by Docker being inoperable mid-run

### Timing

| Metric | Value |
|--------|-------|
| Session duration | ~18 min (05:13–05:31) |
| Peak prompt | 85,391 tokens |
| Context growth | ~38,000 tokens from base (Docker debugging overhead ~20K) |
| Memory refs stored | 4 (mem_1: 3.1KB view, mem_2: 7.8KB bash, mem_3: 4.5KB bash, mem_4: 2.7KB bash) |
| `memory_scroll` calls | 40 |
| Parse error | **None** |

### Comparison with baselines

| Metric | Report_6 (no memory, hot) | Report_7 (no memory, cold) | Report_9 (memory, Docker chaos) |
|--------|--------------------------|---------------------------|----------------------------------|
| Stopped/failed at | 63,535 tokens | 62,935 tokens (pass) | 85,391 tokens (confused) |
| Session duration | 6 min | 4 min 40 s | ~18 min |
| Parse error | Yes | No | **No** |
| Task completed | No | Yes | Partial (poor report) |

### Observations

- **Memory eliminated the parse error.** The model reached 85,391 tokens — 22K past the baseline failure point — without producing malformed output. This is the primary win.
- **Docker detour ate ~20K tokens.** The model spent ~10 turns debugging an inoperable Docker socket (VM couldn't start), storing two large bash outputs as mem_2 and mem_3. By the time Docker was restarted and the session continued, the model had lost track of the playbook and produced a low-quality report.
- **`memory_scroll` is a context leak.** 40 scroll calls in one session means the model was re-injecting stored content back into the prompt in chunks. This partially defeats memory offloading — the saved bytes come back piecemeal via scroll responses. Replacing scrolling with `memory_grep` (search within a ref by pattern) would keep responses small and avoid re-inflation.
- **No offloading after 05:27.** The last stored ref (mem_4) was at 05:27:43. From 80K→85K the context grew from small inline results that each passed under the 2,048B threshold. Sum of many small results can match one large spike in cost.
- **gemma4 still significantly behind qwen3** on task quality at elevated context. The Docker chaos is a confound, but even without it the model needs cleaner context to produce reliable output.

### Next steps

1. Run a clean session (no infrastructure failures) to get an uncontaminated memory-enabled baseline.
2. Implement `memory_grep` to replace high-limit `memory_scroll` calls.
3. Consider lowering `memory_hard_limit_bytes` further (e.g. 1024) to catch more of the small-result accumulation.

---

## Benchmark Run — 2026-05-29 (report_10, memory enabled, clean infrastructure)

**Task:** Same playbook  
**Model:** `gemma4-yolo:latest`  
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`  
**Result:** STUCK — parse failure at 58,529 tokens, crush retried but session did not complete

### Timing

| Metric | Value |
|--------|-------|
| Session start | 05:32 |
| Cold cache penalty (turn 2) | 93 s |
| Parse failure at | 58,529 tokens |
| Peak prompt seen | 62,616 tokens (still climbing at cutoff) |
| Memory refs stored | 2 (mem_1: 4.5KB bash, mem_2: 3.7KB bash) |

### Parse failure: output truncation, not context confusion

At 05:36:54 (`prompt=58,529`), gemma4 emitted `"unexpected end of JSON input"` — different from report_6's `"invalid character 'm'"`. The model was generating a full correct PASS report as the argument to a `write` bash call. The content was accurate (correct steps, correct output, correct conclusion) but the JSON string was cut off mid-generation, likely hitting an output token limit.

This is a **generation length failure**, not a context pressure failure. The fix is to have the model write the report to a file in chunks or use the `write` tool directly rather than embedding a multi-paragraph string inside a bash command argument.

Crush did retry after this failure (next request at 58,573), but the session did not recover to completion.

### Full session timeline

| Turn | Prompt | Duration | Notes |
|------|--------|----------|-------|
| 1 | 828 | 1.3 s | Title |
| 2 | 47,059 | **93 s** | Cold cache |
| 3 | 47,265 | 2.9 s | |
| 4 | 49,695 | 27 s | |
| 5–13 | 50,907–56,323 | 4–14 s | Gradual growth |
| 14 | **58,529** | **38 s** | **Parse failure: "unexpected end of JSON input"** |
| — | 58,573 | 13 s | Crush retry |
| 15–17 | 59,926–62,897 | 9–20 s | Session continues |
| 18–19 | 63,100–63,390 | 3–72 s | 72 s gap before 63,390 request |
| 20 | 65,361 | 28 s | |
| 21 | 67,364 | 27 s | |
| 22 | **68,967** | 16 s | Same prompt retried at 05:41:43 |
| 23 | 68,967 | 18 s | Retry — session ends |

### What the model actually wrote

**report_10.md** (445 B, written after parse-failure retry):
```json
{ "status": "completed", "playbook": "...", "result": "passed",
  "feedback": "...", "challenges": "..." }
```
JSON blob instead of markdown — wrong format, structurally correct content.

**report_10_v2.md** (written at 68K+ tokens):
Proper markdown structure, correct PASS verdict, but contains `"port 4cap4444"` — a hallucination/corruption artifact in the port number (should be 4444). The corruption is a sign of context pressure affecting generation quality even when the parse succeeds.

### Observations

- **Cleaner context growth than report_9.** Without the Docker detour, context grew from 47K to 68K — far better than report_9's 85K.
- **Two new failure modes identified:**
  1. `"unexpected end of JSON input"` — output truncation when embedding large strings in bash args. Model had the right answer; tooling failed to deliver it.
  2. **Wrong output format on retry** — after the truncation, the model switched to JSON instead of markdown, apparently forgetting the format requirement. No mechanism to correct it.
- **Hallucination under context pressure.** `"4cap4444"` in the v2 report is a token-level corruption artifact, not a logic error. Appears as context exceeds ~65K tokens.
- **Crush retry works for truncation but not malformed-structure errors.** Report_6's `"invalid character 'm'"` hung; this `"unexpected end of JSON input"` retried successfully. The difference is likely that truncation produces a structurally parseable partial result whereas the mixed-content failure in report_6 produced something unparseable at the start.

---

## 6. Large write commands as a failure vector

**Problem:** gemma4 (and possibly other models) embed multi-paragraph report content as a single string argument inside a `bash` tool call (e.g. `bash{command: "write -f report.md \"...\""}`). Long string arguments cause the JSON to be truncated at the output token limit before the closing `}` is reached, producing `"unexpected end of JSON input"`.

**Fix:** Either (a) give the model a dedicated `write_file(path, content)` tool that accepts content as a structured field rather than a shell-escaped string, or (b) instruct the model to write reports incrementally (e.g. `echo "line" >> file`) rather than as a single large write. Option (a) is more reliable since it doesn't depend on prompt instructions holding under context pressure.

---

## 7. No task completion self-assessment

**Problem:** After writing a report (or any final deliverable), the model does not verify that the output matches the specification. In report_10, gemma4 wrote a JSON blob when the playbook asked for markdown, then considered the task done. No mechanism caught the format mismatch.

**Fix:** After the model produces what looks like a terminal action (file write, report creation), inject a self-assessment prompt: "Review the output you just produced against the task requirements. Does it fully satisfy the spec? If not, fix it." This should be a configurable step (`enable_task_verification: true`) so it can be disabled for models that handle it poorly or for non-interactive runs where latency matters.

---

## 8. Parse error retry is silent and unconfigurable

**Problem:** When `gemma4.go` logs a `parsing failed` WARN, crush either hangs silently (malformed mixed-content errors, report_6) or retries once without telling the user (truncation errors, report_10). There is no retry count, no backoff, no user-visible indication that something went wrong, and no way to tune the behavior per-model.

**Observed outcomes by error type:**

| Error | Example | Crush behavior |
|-------|---------|----------------|
| `"invalid character 'm'..."` | report_6 | Hangs — no retry |
| `"unexpected end of JSON input"` | report_10 | Retries once silently |

**Fix:**
- Add `max_parse_retries` config knob (default: 3) with exponential backoff.
- On each retry, inject a brief correction hint into the next turn: "Your previous response could not be parsed as a valid tool call. Please try again with a simpler, shorter tool call."
- After `max_parse_retries` exhausted, surface a user-visible error rather than hanging.
- Log retry attempts at WARN level so they appear in the crush log without requiring debug output.

---

## 9. `memory_scroll` re-inflates context

**Problem:** `memory_scroll` with a high `limit` re-injects stored content back into the prompt in chunks, partially defeating memory offloading. In report_9, 40 scroll calls were observed in a single session. The model uses scroll as a substitute for reading the original content, but each scroll response adds tokens to the running context.

**Fix:** Implement `memory_grep(id, pattern)` — search within a stored reference by regex and return only matching lines with context. This lets the model find specific information without paging through the full content. Scroll remains useful for structured sequential reads (log files, diffs) but grep should be the default recommendation in the memory reference summary shown to the model.

---

## Planned fixes (priority order)

Based on all benchmark runs to date, the following improvements are prioritized for gemma4 usability:

1. **Dedicated file tools** (`write_file`, `edit_file`) — structured `path`/`content` fields, no shell escaping. Eliminates the `"unexpected end of JSON input"` failure class. gemma4 reaches for `bash` because the tool description is more prominent; file tools should be listed first in the system prompt for local models.

2. **Parse error retry with config knob** — `max_parse_retries: 3`, backoff, correction hint injected per retry, user-visible error on exhaustion. Fixes the silent hang (report_6 failure mode).

3. **Task completion self-assessment** — post-write verification step, configurable via `enable_task_verification`. Catches format mismatches (JSON vs markdown) and hallucinations (corrupted values like `4cap4444`) before the session closes.

4. **`memory_grep`** — replace high-limit `memory_scroll` with pattern search. Keeps scroll results small and inline, stops the 40-call re-inflation observed in report_9.

5. **Context budget per model** — hard eviction at a configurable token limit (e.g. `context_budget: 55000` for gemma4). Drop oldest tool results when approaching the limit. qwen3 handles 70K+ cleanly; gemma4 degrades at ~60K. The budget should be tunable per model in crush.json.

6. **File read deduplication** (existing issue #2) — evict `read_files` entries after N turns since last access. gemma4 re-reads aggressively, burning context on files it already has.
