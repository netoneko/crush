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
- `memory_scroll` — read a window of lines from a reference (`id`, `offset`, `limit ≤ 50`)
- `memory_grep` — search within a stored reference by regex (`id`, `pattern`, `context_lines`); prefer over repeated `memory_scroll` calls to keep responses small

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

## Benchmark Run — 2026-05-29 (report_11, qwen3-yolo, 02_git_clone, upstream crush)

**Task:** Run acceptance playbook `acceptance/02_git_clone.md` and write results to `tmp/acceptance/02_git_clone_report_11.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Upstream (vanilla) — no custom tooling patches
**Result:** PARTIAL / guided to completion — report written after user intervention at context limit

### Timing

| Metric | Value |
|--------|-------|
| Session start | 17:19 local |
| Session end | 18:02 local |
| Duration | ~43 min |
| Peak prompt | 134,982 tokens (past num_ctx=131,072 — ollama trimmed KV cache) |
| Context at report write | 130,317 tokens |
| Final turn | 134,982 tokens (20s — closing text only) |

### Per-turn prompt size (selected)

| Time | Prompt (tokens) | Duration | Notes |
|------|-----------------|----------|-------|
| 17:25 | 70,416 | 1m42s | |
| 17:29 | 75,809 | 49s | |
| 17:30 | 76,303 | 9s | cache remaining: 784 — very tight |
| 17:37 | 87,818 | 40s | cache remaining: 755 |
| 17:41 | 100,538 | 1m34s | |
| 17:46 | 120,822 | 1m42s | |
| 17:51 | 129,878 | 2m5s | **max_tokens** hit — model tried to write report, ran out of output budget |
| 17:59 | 130,317 | 2m39s | user prompted "update the report" — model wrote it successfully |
| 18:01 | 134,982 | 20s | past num_ctx limit; closing text turn; session done |

### Observations

- **qwen3 handled 134K+ tokens without parse errors.** Ran 4K past the configured num_ctx=131,072 limit — ollama trimmed the KV cache to fit rather than erroring. No malformed tool calls, no JSON truncation.
- **`max_tokens` on output, not context.** At 129,878 tokens, the model hit the output generation limit mid-report-write — the thinking trace + report content exceeded the per-response token cap. The session appeared stuck but the model was healthy. User nudge ("update the report now") restarted it cleanly.
- **KV cache evictions throughout.** `cache remaining` was frequently under 1,000 tokens from 17:30 onward, meaning older context was continuously being evicted. qwen3 stayed coherent despite heavy eviction pressure — a notable contrast to gemma4 which fails structurally at ~63K.
- **Playbook blockers (not model failures):**
  1. `akuma-playground` repo was private at test time — git clone failed with auth error. User made it public; subsequent clone succeeded.
  2. `apk add tcc` installs the compiler binary only — no `crt1.o`, `crti.o`, `stdio.h`. Fix: `apk add tcc musl-dev`.
  3. VM custom shell lacks `/dev/null`, `which`, `find -name`, `head` — broke several diagnostic commands the model tried.
  4. SSH drops (rc=255) after long apk commands — packages install correctly despite the disconnect.
- **User-guided victory.** Model correctly diagnosed all blockers and identified `musl-dev` as the fix (confirming user's hint). Final report is accurate and actionable.
- **No memory offloading triggered.** Vanilla upstream build; memory feature not enabled. Context grew linearly with no offloading.

### Improvements for next run

1. Enable memory with `memory_hard_limit_bytes: 2048` — the long diagnostic bash outputs (apk search, ls /usr/lib) would have been offloaded, saving significant context.
2. Add `musl-dev` to the playbook: `ssh("apk add tcc musl-dev")`.
3. Pre-make the repo public or use SSH git clone with key auth.
4. Consider adding busybox to the disk image for a proper shell environment (fixes `/dev/null`, `find`, `head`, etc.).
5. Set `num_ctx` higher (e.g. 163840) for qwen3-yolo on this hardware — it handled 134K cleanly, so the 131K limit is the binding constraint, not model quality.

---

## Benchmark Run — 2026-05-29 (report_12, qwen3-yolo, 02_git_clone, updated playbook)

**Task:** Run acceptance playbook `acceptance/02_git_clone.md` (updated from report_11 findings) and write results to `tmp/acceptance/02_git_clone_report_12.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Upstream (vanilla)
**Playbook:** Rewritten after report_11 — includes `UserKnownHostsFile=/dev/null`, ANSI stripping, `apk add tcc musl-dev`, and rc=255 guidance
**Result:** FAIL — report never written; session hit max_tokens repeatedly and terminated; context exhausted at 121K tokens

### Timing

| Metric | Value |
|--------|-------|
| Session start | ~18:26 local |
| Session still active at | 18:57 local |
| Peak prompt | ~157K tokens (past num_ctx=131,072 — KV cache trimmed) |
| Prompt after KV trim | 85,706 tokens (reset) |
| Prompt at last observation | 121,535 tokens |
| Sub-agent spawned at | ~93K tokens |
| Report written | **No** — model hit max_tokens before file write |

### What happened

1. **Shell archaeology loop.** Model ran into the mini-shell buffering issue early — commands like `git` and `apk` return output only to `[DEBUG] Using buffered path` consumers, not the SSH client. Model spent many turns trying to verify commands that appeared to produce no output, looping through increasingly creative diagnostic approaches.

2. **Sub-agent spawned at ~93K tokens.** Model launched a sub-agent tool call to debug the buffering issue (first time this has been observed in crush sessions). The sub-agent read `src/ssh/protocol.rs` and `src/shell/mod.rs`, correctly identified `check_streamable_command` and the hardcoded PATH approach, and reported back. This is the model independently discovering the same root cause that was previously documented in `docs/STABILITY_URGENT_ISSUES.md`.

3. **KV cache trimmed at 157K.** Session hit well past num_ctx=131,072 before crush/ollama trimmed the cache back to ~85K. Despite the trim, the model had already lost the thread — continued in archaeology mode.

4. **User nudge: "include all your debugging findings in the report as well."** Model responded "Now I have enough data to write a comprehensive report." then hit max_tokens mid-generation. Only tool call was `mkdir -p tmp/acceptance`. No report file was written.

5. **Second and third nudges both returned empty.** At 121K tokens, the prompt consumes nearly all of the 131K num_ctx window — no room for generation. Each subsequent request hit max_tokens immediately and returned an empty assistant message. Session terminated without recovery.

### Key new observations

- **Model independently discovered the buffered path bug — and the fix shipped.** The sub-agent read the akuma source, traced `handle_exec → execute_command_streaming_interactive → check_streamable_command`, and reported back. The user applied the fix (commit `dd0cad5`, 18:49 local) while the model was still churning. Correction to prior analysis: `check_streamable_command` already returns `StreamableCommand::External` for any resolvable binary — Fix 2 was already in place. The only bug was the stray `[DEBUG] Using buffered path\r\n` write in the buffered fallback branch of `handle_exec`. One line deleted; a static regression test (T9 in `src/ssh_tests.rs`) guards against re-introduction.

- **`busybox sh -c` workaround was NOT in the updated playbook.** Report_11 mentioned it, but the playbook rewrite only added rc=255 notes and musl-dev. The model had to rediscover the workaround on its own. This cost many turns and eventually caused the session to fail. Should be baked in as the default SSH helper wrapper.

- **Sub-agent tool call works but is expensive.** The agent spawned a sub-agent to debug the buffering, which correctly found the answer but cost additional context tokens for the agent tool call overhead and result injection. Total cost: several thousand tokens for information that could have been retrieved in one `view` call if the model knew where to look.

- **157K tokens, no parse error.** qwen3 continues to show robust structured output even past double the gemma4 failure point. Context robustness is not the issue — it's archaeology loops that waste the context budget.

- **Updated playbook did not prevent the loop.** The rc=255 guidance and ANSI stripping were present, but without `busybox sh -c` as the wrapper, commands still appear to silently fail (buffered output not returned to client). Model fell into the same diagnostic trap as report_11.

### Improvements for next run

1. **Add `busybox sh -c` as default SSH wrapper.** Change the playbook helper to wrap every command: `ssh(f"busybox sh -c '{cmd}'")`. This bypasses the streaming check and returns output correctly. Without this, all non-whitelisted binaries silently discard output to the SSH client.
2. ~~**Fix the akuma buffering bug.**~~ **FIXED** (`dd0cad5`) — deleted the `[DEBUG] Using buffered path` write from `handle_exec`; T9 regression test added. `check_streamable_command` already streamed all resolvable binaries; only the debug print was the bug.
3. **Enable memory offloading.** Long diagnostic bash outputs from the archaeology loop would have been offloaded, saving 30–40K tokens.
4. **Add playbook escape hatch.** If rc=255 and out is empty, the note should say "use `busybox sh -c '...'` to force streaming" explicitly — not just "rc=255 is normal."

---

## Benchmark Run — 2026-05-29 (report_13, qwen3-yolo, 02_git_clone, buffering fix applied)

**Task:** Run acceptance playbook `acceptance/02_git_clone.md` and write results to `tmp/acceptance/02_git_clone_report_13.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Upstream (vanilla)
**Akuma build:** Post-`dd0cad5` — `[DEBUG] Using buffered path` removed
**Result:** PARTIAL — report written (6.8KB, good quality); git clone blocked by akuma kernel bug

### Timing

| Metric | Value |
|--------|-------|
| Session start | ~19:09 local |
| Report written at | 19:28 local |
| Peak prompt at report | ~103,910 tokens |
| User nudge required | Yes — "just write the report already" |

### Step results

| Step | Result |
|------|--------|
| 1–4 Setup | ✅ PASS |
| 5 `apk add git` | ✅ PASS (git 2.52.0) |
| 6a `git --version` | ✅ PASS |
| 6b `git clone` | ❌ FAIL — `fatal: write error: No such file or directory` |
| 7 `apk add tcc musl-dev` | ✅ PASS (tcc 0.9.27, musl-dev installed) |
| 8 `tcc -o /tmp/hello` | ❌ FAIL — transitive (no `akuma-playground/hello.c`) |
| 9 `/tmp/hello` | ❌ FAIL — transitive |

### Root cause identified by model

Git clone fails because it needs `/tmp/` for internal pack temp files during the HTTPS fetch. **The akuma ext2 VFS does not forward `O_CREAT` write syscalls from child processes** — `/tmp/` appears in `ls /` and the shell's `mkdir` builtin can create entries in its directory table, but user-space processes calling `open("/tmp/...", O_CREAT)` get `ENOENT`. This is an akuma kernel bug, not a playbook issue.

Model tried 6 workarounds before giving up:
1. Plain `git clone <url>` (CWD `/`) → `No such file or directory`
2. `git clone <url> /tmp/akuma-playground` → same error
3. `mkdir -p /tmp/ap && git clone <url> /tmp/ap/akuma-playground` → mkdir returned "Not found"
4. `TMPDIR=/ git clone <url>` → mini-shell rejected `KEY=VALUE cmd` syntax
5. `export TMPDIR=/ && git clone <url>` → env not propagated to git child process
6. `cd / && git clone <url> akuma-playground` → same fatal write error

### Additional bugs identified

- **`export` doesn't propagate to child processes.** The mini-shell's `execve` wrapper does not forward the full environment to child processes. `TMPDIR=/` can be set but git never sees it.
- **No `VAR=val cmd` syntax.** Mini-shell treats `TMPDIR=/` as a command name (unknown command). Standard POSIX env-injection syntax not supported.

### Model quality

Report is 6.8KB, well-structured, accurate diagnosis, all workarounds documented. Model correctly distinguished between platform bugs (ext2 writes, env propagation) and transitive failures (compile, run). Context was at ~104K when the report was written — pushed to the wall but functional after user nudge.

### What changed vs report_12

- Buffering fix (`dd0cad5`) eliminated the archaeology loop — model got actual command output this time
- Model reached the real blocker (git clone / ext2 writes) instead of spinning on buffering
- Report written successfully vs complete session failure
- **New blocker exposed:** ext2 `O_CREAT` from child processes — this is the next thing to fix in akuma

### Improvements for next run

1. **Fix akuma ext2 `O_CREAT` for child processes** — primary blocker. Once fixed, git clone should work.
2. **Fix `execve` env propagation** — `export TMPDIR=` must reach child processes.
3. **Support `VAR=val cmd` syntax** in the mini-shell.
4. Add `.git` suffix to clone URL in playbook (confirmed by user; less likely to cause issues with some git servers).

---

## Benchmark Run — 2026-05-29 (report_14, qwen3-yolo, 01_verify_apk_bootstrap, memory + new tools)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_13.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory enabled, `file_write`/`file_edit`/`file_grep`/`memory_grep` added
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`
**Result:** PASS — report written (6.4KB, accurate, good quality)

### Timing

| Metric | Value |
|--------|-------|
| Session duration | ~7 min (20:02–20:09 local) |
| LLM requests | 30 |
| Starting prompt | **12,197 tokens** |
| Peak prompt | **22,425 tokens** |
| Total LLM time | ~192 s (3.2 min) |
| Avg response time | 6.4 s |
| Max response time | 32.0 s (cold-cache turn 2) |

### Tool call breakdown (27 total)

| Tool | Calls |
|------|-------|
| `bash` | 16 |
| `todos` | 5 |
| `grep` | 2 |
| `job_output` | 1 |
| `ls` | 1 |
| `view` | 1 |
| `write` | 1 |

`memory_scroll` / `memory_grep` / `memory_list`: **0** — memory refs were stored but not queried.  
`file_write` / `file_edit` / `file_grep`: **0** — new tools available but not invoked (model used `write` + `bash` instead).

### Memory references stored (5 total)

| ID | Tool | Bytes |
|----|------|-------|
| mem_1 | bash | 3,060 |
| mem_2 | bash | 3,090 |
| mem_3 | grep | 9,763 |
| mem_4 | bash | 2,729 |
| mem_5 | bash | 5,539 |

### Comparison: report_3 → report_4 → report_14

| Metric | Report_3 (qwen3, no mem) | Report_4 (qwen3, mem fix) | Report_14 (qwen3, mem + tools) |
|--------|--------------------------|---------------------------|--------------------------------|
| Result | PASS | PASS | PASS |
| Session duration | 18.2 min | 8.1 min | **~7 min** |
| LLM requests | 41 | 17 | 30 |
| Total tool calls | 92 | 46 | **27** |
| Avg response time | 18.8 s | 14.0 s | **6.4 s** |
| Max response time | 175.9 s | 48.8 s | **32.0 s** |
| Starting prompt | ~46K tokens | ~47K tokens | **12K tokens** |
| Peak prompt | ~71K tokens | ~62K tokens | **22K tokens** |
| Total LLM time | ~772 s | ~238 s | **192 s** |
| Memory refs stored | 0 | 0 | **5** |
| `view` calls | 35 | 15 | 1 |
| `bash` calls | 31 | 14 | 16 |
| `todos` calls | 18 | 1 | 5 |
| Parse errors | 0 | 0 | 0 |

### Observations

- **Starting prompt is 12K tokens, not 46K.** Earlier reports recorded ~46K starting prompt tokens, but verification against all available crush logs shows no session ever exceeding 30K `prompt_tokens` in the Ollama API response. The actual breakdown for the current session: system prompt 26,449 chars (~6,612 tokens) + tools JSON 20,682 chars (~5,170 tokens) + initial user message (~500 tokens) ≈ 12,282 tokens. The 46K values in earlier reports were likely from a different Ollama version that counted tool schemas differently, or from rotated log files that cannot be checked. **12K is the correct current baseline.** This makes all three runs (report_3, _4, _14) directly comparable at the same starting prompt size.

- **Memory was triggered (5 refs) but never queried.** The model received compact summaries for all 5 oversized results and continued without scrolling them back. For this playbook the summaries were sufficient — the model didn't need to re-read the full content. This is the ideal memory behavior: offload + forget rather than offload + scroll.

- **New tools (`file_write`, `file_edit`, `file_grep`) not invoked.** qwen3 used `write` (bash-style) and inline bash for all file operations. The new tools are present in the schema but the model had no reason to prefer them. Expected to see impact with gemma4, which hits the `"unexpected end of JSON input"` failure when writing large content via bash.

- **Task tracking diverged from actual completion.** The crush todos at session end show: tasks 1–3 completed, task 4 ("Start the VM and wait for SSH") `in_progress`, tasks 5–6 ("Run acceptance steps", "Write results report") `pending`. But the report file exists with full PASS content. The model completed all work but never called `todos` to close out tasks 4–6. Session ended with `finish_reason: tool_calls` — the model was still running when the session was closed. From the UI perspective the task appeared incomplete; the actual output was correct.

- **Title generation failed.** Small model `gemma4:yolo-4b` not found; title fell back to the large model (32s overhead). See issue #5 (model name validation).

- **No `reasoning` output (qwen3).** Like previous qwen3 runs, no `<think>` tags emitted. The `reasoning_effort: high` config has no effect for Ollama-served models.

### Improvements for next run

1. **Investigate starting prompt size.** 12K vs 46K is a large unexplained delta. Check whether the prior runs loaded a CLAUDE.md, a larger `read_files` set, or a different system prompt source. If the reduction is real and stable, it's the single biggest win in this series.
2. **Fix `gemma4:yolo-4b` model name** in crush.json → `gemma4-yolo-4b` (colon vs hyphen, issue #5).
3. **Verify `file_write` gets used by gemma4** — run `01_verify_apk_bootstrap` with gemma4 to confirm the new tool eliminates the JSON truncation failure.
4. **Todo tracking fix** — model should mark tasks done before writing the final report. Consider injecting a "mark all completed todos" step after the model produces a terminal write.

---

## Benchmark Run — 2026-05-29 (report_15, qwen3-yolo, 01_verify_apk_bootstrap, second clean run with memory + new tools)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_14.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory enabled, `file_write`/`file_edit`/`file_grep`/`memory_grep` added
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`
**Result:** PASS — `01_verify_apk_bootstrap_report_14.md` (3.5 KB, accurate, well-structured)

### Timing

| Metric | Value |
|--------|-------|
| Session duration | ~7 min (20:30–20:37 local) |
| LLM requests | 25 (incl. 2 setup) |
| Starting prompt | ~12K tokens |
| Peak prompt | ~14K tokens |
| Total LLM time | ~225 s (3.75 min) |
| Avg response time | 9.0 s |
| Max response time | 55.0 s (report write turn) |

### Tool call breakdown (23 total)

| Tool | Calls |
|------|-------|
| `bash` | 10 |
| `todos` | 7 |
| `memory_scroll` | 3 |
| `view` | 1 |
| `job_output` | 1 |
| `write` | 1 |

### Memory references stored (2 total)

| ID | Tool | Bytes |
|----|------|-------|
| mem_1 | bash | 3,060 |
| mem_2 | bash | 3,820 |

### Comparison: report_14 → report_15

| Metric | Report_14 (mem + tools) | Report_15 (second run) | Delta |
|--------|-------------------------|------------------------|-------|
| Session duration | ~7 min | ~7 min | 0 |
| LLM requests | 30 | 25 | −17% |
| Total tool calls | 27 | 23 | −15% |
| Avg response time | 6.4 s | 9.0 s | +41% |
| Max response time | 32.0 s | 55.0 s | +72% |
| Starting prompt | ~12K tokens | ~12K tokens | 0 |
| Peak prompt | ~22K tokens | ~14K tokens | **−36%** |
| Total LLM time | 192 s | 225 s | +17% |
| Memory refs stored | 5 | 2 | −60% |
| `memory_scroll` calls | 0 | **3** | first use |
| `bash` calls | 16 | 10 | −37% |
| `todos` calls | 5 | 7 | +40% |
| `view` calls | 1 | 1 | 0 |
| Parse errors | 0 | 0 | 0 |

### Observations

- **PASS with good quality report** (3.5 KB, well-structured, exact output matching, all steps documented). SSH known_hosts conflict handled correctly via `StrictHostKeyChecking=no`. All expected apk output matched exactly.

- **Peak prompt dropped to 14K tokens** — down from 22K in report_14, despite the same task. Fewer large bash outputs this run (only 2 mem refs vs 5), suggesting the VM state was warmer or SSH commands returned shorter output.

- **`memory_scroll` used for the first time (3 calls).** Unlike report_14 where memory was offloaded and forgotten, this session had the model read back stored content. Still well-controlled — 3 scroll calls vs 40 in report_9's Docker chaos run.

- **Max response time 55 s on the write turn.** The model generated a full markdown report in a single response. This is the `"unexpected end of JSON input"` risk window for gemma4 — a 55 s bash-embedded generation would truncate. On qwen3 it completed cleanly.

- **Fewer total tool calls (23 vs 27) with the same result.** The model was more direct; fewer diagnostic bash calls. Todo management increased slightly (7 vs 5).

- **Higher avg response time (9.0 s vs 6.4 s).** Variance in VM SSH response times and/or colder KV cache. Not a model regression.

### Improvements for next run

1. **Track `memory_scroll` count as a health metric.** 3 is fine; 40 (report_9) is a context leak. Consider logging the count at session end.
2. **Try `compact_tools: true`** — new config flag that reduces tool description tokens. Expected to save ~200–400 tokens from tool schema overhead per session. Measure impact on next run.

---

## Benchmark Run — 2026-05-29 (report_16, qwen3-yolo, 01_verify_apk_bootstrap, compact_tools enabled)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_15.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory + new tools + `compact_tools: true`
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`, `compact_tools: true`
**Result:** FAIL — model went into akuma source code archaeology; report never written

### Timing (two-part session, same session ID)

| Metric | Part 1 | Part 2 (after nudge) |
|--------|--------|----------------------|
| Start | 23:15:46 | 23:27:43 |
| End | 23:22 | 23:29:29 |
| LLM time | 4m45s | 6m13s |
| Turns | 23 | 8 |
| Starting prompt | **10,343 tokens** | 20,380 tokens (reloaded history) |
| Peak prompt | 16,490 tokens | 21,557 tokens |

### Tool call breakdown (part 1, 25 total)

| Tool | Calls |
|------|-------|
| `bash` | 14 |
| `view` | 3 |
| `todos` | 3 |
| `ls` | 2 |
| `job_kill` | 1 |
| `job_output` | 1 |
| `edit` | 1 |

### Memory references stored (4 total across both parts)

| Part | ID | Tool | Bytes |
|------|----|------|-------|
| 1 | mem_1 | view | 3,718 |
| 1 | mem_2 | view | 3,103 |
| 1 | mem_3 | bash | 3,060 |
| 2 | mem_5 | view | 3,181 |

### Compact tools: starting prompt reduction

The compact descriptions reduced the starting prompt from ~12,197 tokens (report_14) to **10,343 tokens** — a **−15% reduction** from tool schema alone. The savings are real and consistent.

### Failure mode: archaeology loop

Part 2's last 3 tool calls:
1. `view(src/ssh/keys.rs)` → 3.1KB stored as mem_5
2. `bash: grep -n "panic|PANIC" src/ssh/keys.rs` → no results
3. `bash: ls bootstrap/etc/` → listing

The model hit something during the acceptance run (likely an SSH error or unexpected output), switched into debugging mode, and started reading akuma kernel source instead of following the playbook. The final 720-token response was an explanation of findings, not a report write.

This is the same archaeology loop seen in report_12 (qwen3 on `02_git_clone`). The trigger is any unexpected tool result that the model can't map to playbook instructions — it then falls back to "investigate the system" as a goal rather than "document what I found and move on."

### Comparison: report_14 → report_15 → report_16

| Metric | Report_14 | Report_15 | Report_16 |
|--------|-----------|-----------|-----------|
| Result | PASS | PASS | **FAIL** |
| compact_tools | no | no | **yes** |
| Starting prompt | ~12,197 | ~12,197 | **10,343** |
| Peak prompt | ~22K | ~14K | 16,490 |
| Session duration | ~7 min | ~7 min | ~14 min (incl. nudge) |
| Parse errors | 0 | 0 | 0 |
| Archaeology | no | no | **yes** |

### Observations

- **−15% starting prompt from compact tools** — measured, real, consistent. No model behavior regression from shorter descriptions; qwen3 used all tools correctly.
- **Archaeology loop is the primary failure mode for qwen3** on this playbook, not context pressure or parse errors. The model is robust to token load; it's not robust to unexpected SSH behavior.
- **Nudge was not enough.** User prompted continuation after part 1; the model restarted but continued investigating rather than pivoting to the report.
- **Todos not used in this run.** The todos enforcement fix (reject >1 in_progress) could not be tested here.

### Improvements for next run

1. **Add playbook escape clause**: "If any SSH step fails or returns unexpected output, document the error verbatim and proceed to the next step. Do not investigate the akuma source code."
2. **Set a hard step budget**: "Complete all steps within N bash calls. If not finished by step N, write a partial report."
3. **Enable `enable_task_self_assessment`** in crush.json — would fire after a clean session end with incomplete todos, prompting the model to write the report.

---

## Benchmark Run — 2026-05-29 (report_17, qwen3-yolo, 01_verify_apk_bootstrap, compact_tools + taskmaster)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_16.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory + new tools + `compact_tools: true` + `enable_task_self_assessment: true`
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`, `compact_tools: true`
**Result:** PASS — `01_verify_apk_bootstrap_report_16.md` (4.2 KB, accurate, well-structured)

### Timing

| Metric | Value |
|--------|-------|
| Session start | 23:44:55 |
| Report written | 23:53:23 |
| Session duration | ~9 min |
| Starting prompt | **10,391 tokens** |
| Peak prompt | **20,753 tokens** |
| Turns | 27 (main) + 1 self-assessment |
| LLM time | ~7 min |

### Token progression

| Time | Input | Output | Notes |
|------|-------|--------|-------|
| 23:46:08 | 10,391 | 228 | First real turn |
| 23:47:08 | 12,705 | 442 | apk install |
| 23:49:11 | 15,546 | 234 | |
| 23:50:22 | 16,880 | 511 | busybox verification attempts |
| 23:51:36 | 18,195 | 542 | |
| 23:53:23 | 19,401 | **1,320** | Report write |
| 23:53:38 | **20,753** | **133** | Self-assessment (taskmaster) |

### Tool call breakdown (27 total)

| Tool | Calls |
|------|-------|
| `bash` | 16+ |
| `ls` | 2 |
| `view` | 1 |
| `write` | 1 |

Memory refs: 2 stored (bash 3.1KB at 23:47, bash 4.2KB at 23:48); neither scrolled back.

### Taskmaster confirmed firing

After the report write turn (1,320 output tokens), the next turn shows input jumping from 19,401 → 20,753 (+1,352 tokens) with only 133 output. The +1,352 matches the injected report content plus the self-assessment prompt. The 133-token response is the model confirming completion. No further turns — session ended cleanly.

### Key discovery: `busybox sh -c` fails, `busybox echo` works

The playbook step "verify busybox works" expects output `busybox OK`. The model tried `busybox sh -c 'echo busybox_OK'` → rc=255 (mini-shell doesn't forward `sh -c` syntax). After 3 failed attempts it discovered `busybox echo busybox_OK` → output `busybox_OK`. The mini-shell treats unknown commands as passthrough to binary with argv, so `busybox arg1 arg2` works while `sh -c '...'` does not.

The model correctly documented this in the report as a platform behavior note rather than a failure.

### Report used `write` tool, not bash

The model called `write(file_path=..., content=...)` directly — the structured file tool — rather than embedding content in a bash heredoc. No JSON truncation risk. This validates the `file_write`/`write` tooling approach.

### Comparison: report_14 → report_15 → report_16 (fail) → report_17

| Metric | R14 | R15 | R16 | R17 |
|--------|-----|-----|-----|-----|
| Result | PASS | PASS | FAIL | **PASS** |
| compact_tools | no | no | yes | yes |
| taskmaster | no | no | no | **yes** |
| Starting prompt | ~12,197 | ~12,197 | 10,343 | **10,391** |
| Peak prompt | ~22K | ~14K | 16,490 | **20,753** |
| Archaeology | no | no | **yes** | no |
| Self-assessment | no | no | no | **yes** |
| No-nudge completion | yes | yes | no | **yes** |

### Observations

- **No archaeology loop.** The model hit `busybox sh -c` failures (rc=255) and tried different invocations rather than reading akuma source. The difference from report_16 is unclear — may be run-to-run variance, or report_16 had a harder SSH failure that triggered source-reading heuristic.
- **Compact tools held at 10,391 tokens** for the third consecutive run. Reduction is stable.
- **Peak 20,753 is higher than report_15 (14K).** The model did more diagnostic work (5 attempts to get busybox verification right). Memory refs kept large outputs off the main context but total tool history still grew.
- **Taskmaster fired and produced a brief completion** (133 tokens). Effective — no wasted turns.
- **Small model name bug still present** — `gemma4:yolo-4b` not found, title fell back to large model.

### Playbook update needed

Replace the busybox verification step:
```
# Old (fails with rc=255 via sh -c):
busybox sh -c 'echo busybox OK'

# Working:
busybox echo busybox OK
```

---

## 10. Archaeology loop antipattern

**Problem:** When a tool returns unexpected output (SSH rc=255, empty output, unfamiliar error message), qwen3 and other large models switch from "follow the playbook" to "investigate the system." The model starts reading source files, grepping for relevant symbols, and tracing code paths. This consumes context budget, burns time, and never produces the requested output.

**Observed in:** report_12 (qwen3, `02_git_clone`, buffering bug), report_16 (qwen3, `01_verify_apk_bootstrap`, SSH/VM issue).

**Fix options:**

- **Playbook-level**: Add explicit instructions after each fallible step: "If this step fails, record the error and move on. Do not read akuma source files."
- **System prompt**: Add a global rule: "You are running acceptance tests, not debugging the system under test. If a command fails, document it and continue."
- **Step budget**: Inject a soft counter into the system prompt: "You have N tool calls remaining. Use them to complete the playbook, not to investigate failures."
- **Crush-level** (longer term): A max-steps-per-session config that returns a warning tool result when the budget is nearly exhausted, prompting the model to write a partial report.

---

---

## Benchmark Run — 2026-05-30 (report_18, qwen3-yolo, 01_verify_apk_bootstrap, compact_prompt enabled)

**Task:** Run acceptance playbook `acceptance/01_verify_apk_bootstrap.md` and write results to `tmp/acceptance/01_verify_apk_bootstrap_report_17.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory + new tools + `compact_tools: true` + `compact_prompt: true`
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`, `compact_tools: true`, `compact_prompt: true`
**Result:** PASS — all 6 steps green, all expected output lines matched exactly

### Timing

| Metric | Value |
|--------|-------|
| Session ID | `cbb2cc38` |
| Session start | 16:54:10 local |
| Turn 1 input | **10,371 tokens** |
| Observed peak (turn 15) | ~17,961 tokens |
| Growth rate | ~400–500 tokens/turn |

### Per-turn input token growth

| Turn | Input tokens | Notes |
|------|-------------|-------|
| 1 | 10,371 | First real turn |
| 2 | 11,629 | +1,258 — first tool results |
| 3–5 | 11,718–11,950 | Gradual growth |
| 6 | 12,232 | |
| 7 | 12,774 | |
| 8 | 13,068 | |
| 9 | 14,238 | +1,170 spike |
| 10 | 14,578 | |
| 11 | 15,513 | |
| 12 | 16,663 | +1,150 spike |
| 13 | 16,759 | |
| 14 | 17,856 | |
| 15 | 17,961 | |

### Result summary

| Expected output | Match? |
|-----------------|--------|
| `(1/2) Installing musl (1.2.5-r23)` | ✅ |
| `(2/2) Installing busybox (1.37.0-r30)` | ✅ |
| `Executing busybox-1.37.0-r30.post-install` | ✅ |
| `Executing busybox-1.37.0-r30.trigger` | ✅ |
| `OK: 1612 KiB in 2 packages` | ✅ |
| `busybox OK` | ✅ |

### Compact_prompt: unexpected measurement

Expected: `compact_prompt` reduces system prompt from ~7,506 tokens (original template) to ~3,602 tokens (compact template) — a ~3,904 token savings. Turn 1 should have been ~6,500 tokens.

**Observed:** Turn 1 = 10,371 tokens vs report_17's 10,391 (no compact_prompt) — only **20 tokens difference**, well within noise.

The compact template IS being sent to the model (confirmed from crush HTTP log body: prompt opens with `You are Crush, a powerful AI Assistant...` matching `coder_compact.md.tpl`). The Ollama `completion request` log at 16:53:36 shows `prompt=42,642 chars` for the first turn; at the measured 4.1 chars/token ratio for JSON-encoded chat completions this is ~10,400 tokens — consistent with the posthog figure but inconsistent with the expected 3,900-token savings.

**Likely causes (to investigate):**
1. The akuma `context_paths` injects large files (README, skill docs, etc.) into the `<memory>` block, which dominates the rendered system prompt regardless of template size. Template prose savings (~10K chars) may be overshadowed by injected content.
2. The chars/token ratio for the rendered prompt including injected content may differ from the 2.62 ratio measured on plain prose.
3. Compare rendered system prompt length in the HTTP body for a compact vs non-compact session to isolate what changed.

### Session health vs baseline (report_17)

| Metric | Report_17 (no compact_prompt) | Report_18 (compact_prompt) |
|--------|-------------------------------|----------------------------|
| Result | PASS | **PASS** |
| Turn 1 input tokens | 10,391 | 10,371 |
| Peak tokens seen | 20,753 | ~17,961 |
| Turns before context pressure | none seen | none seen |
| Parse errors | 0 | 0 |
| Archaeology loop | no | no |

**Key win:** session ran 15+ turns and peaked at ~18K tokens vs report_17's 20,753 peak — a **~2,800 token lower peak** despite similar starting point. The playbook completed cleanly without the diagnostic work that inflated report_17.

### Observations

- **No archaeology loop** — model executed the 6-step playbook directly without reading akuma source. SSH known_hosts warning observed (expected for fresh VM boots) but did not block execution.
- **Compact_prompt token savings not visible at turn 1** — template reduction is real but appears offset by injected context (context_paths content). Need to measure rendered system prompt character count in the HTTP request body to quantify actual savings.
- **Lower peak than report_17** — despite identical starting tokens, the session peaked lower. The compact template may be reducing verbosity of tool calls / model responses rather than raw system prompt size.
- **Session health is good for a 27K context window** — at ~400 tokens/turn growth, context pressure would not appear until turn ~40+.

### Next investigation

1. **Measure rendered system prompt size directly** — log `len(systemPrompt)` in `buildAgent` before and after `compact_prompt` to confirm actual rendered size reduction.
2. **Check what context_paths are active** in akuma's `crush.json` — README and skill content in `<memory>` may dominate the rendered prompt.
3. **Run with context_paths empty** to isolate template-only savings vs injected content.

---

## Context budget: README and system prompt analysis (2026-05-30)

### Token measurement methodology

Ollama's tokenizer reports ~2.62 chars/token for the mixed markdown/code/prose content of the
akuma context. All token estimates below use this ratio (measured from an actual `prompt_eval_count`
response: 8,871 tokens for 23,219 chars of system prompt + README + user message).

### README token impact

If `README.md` is added to `context_paths` it is injected into the system prompt on every session.

| Version | Chars | Tokens (Ollama ratio) |
|---------|-------|-----------------------|
| Original (pre-2026-05-30) | 12,831 | ~4,902 |
| Hand-edited (removed ASCII box, memory layout, prose) | 7,422 | ~2,836 |
| gemma4-yolo-4b auto-compressed (truncated at 2048 output tokens) | 4,906 | ~1,874 |
| **Savings: original → hand-edit** | −5,409 chars | **−2,067 tokens** |

The model's auto-compressed output was cut off mid-sentence at the 2,048 output token limit. The
reachable floor for a complete, agent-functional README is somewhere between the hand-edit (~2,836)
and the model output if it completed (~1,800 estimated). The hand-edited version is the current
baseline; further compression is possible but requires raising `num_predict` or chunking the
generation.

### Starting prompt with README added

| Configuration | Starting prompt tokens |
|---------------|------------------------|
| No README (current baseline, report_14–17) | ~12,200 |
| + Original README | ~17,100 |
| + Hand-edited README | ~15,000 |
| + compact_tools + hand-edited README | ~13,400 |

Adding the hand-edited README and `compact_tools` together keeps the starting prompt within ~1,200
tokens of the no-README baseline while giving the model full capability and platform context.

### Note: `read_files` table is not a context source

The `read_files` SQLite table (issue #2 in this doc) does **not** re-inject file content into
prompts. It is used only for the edit safety guard (must-read-before-edit) and LSP warmup on
session resume. The context bloat from `view` calls is purely from tool result history replayed
in the message thread, not from any separate injection mechanism. Issue #2 as originally described
is a non-issue; the memory offloading feature already addresses the actual source of bloat.

---

## Benchmark Run — 2026-05-30 (report_19/22, qwen3-yolo, 02_git_clone, full stack)

**Task:** Run acceptance playbook `acceptance/02_git_clone.md` and write results to `tmp/acceptance/02_git_clone_report_22.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Small model:** `qwen3:4b` (summarize_prompt; `enable_thinking: false` added this session)
**Crush build:** Patched — memory + compact_tools + compact_prompt + summarize_prompt
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`, `compact_tools: true`, `compact_prompt: true`, `summarize_prompt: true`
**Result:** PARTIAL PASS — steps 5–7 pass, steps 8–9 fail (playbook bugs, not model bugs)

### Session token progression

| Turn (time) | Input tokens | Output tokens | Notes |
|-------------|-------------|---------------|-------|
| 18:13:43 | 12,198 | — | Turn 1 (with full system prompt, pre-compress) |
| 18:14:22 | 12,302 | — | |
| 18:14:38 | 12,469 | 133 | |
| 18:15:39 | 12,976 | 201 | |
| 18:17:00 | 13,332 | 732 | |
| 18:17:18 | 14,071 | 81 | |
| 18:19:34 | 14,310 | 67 | User cancelled agentic_fetch hallucination |
| 18:21:56 | 10,665 | 1,161 | "continue" resumes; new turn with compressed prompt |
| 18:30:25 | 20,740 | 1,133 | |
| 18:33:01 | 24,511 | 204 | |
| 18:34:06 | **24,913** | **1,061** | Final turn — partial summary written as chat text |

**Peak: 24,913 tokens. Duration: ~21 min. No parse errors.**

### Step results

| Step | Result | Notes |
|------|--------|-------|
| 5 Install git | ✅ PASS | `OK: 30.8 MiB in 20 packages` (rc=255 expected) |
| 6 git clone | ✅ PASS | Cloned successfully; `hello.c` confirmed in working tree |
| 7 Install tcc + musl-dev | ✅ PASS | `OK: 30.8 MiB in 20 packages` (rc=255 expected) |
| 8 Compile hello.c | ❌ FAIL | `tcc: error: file 'libtcc1.a' not found` |
| 9 Run binary | ❌ FAIL | `Unknown command: /tmp/hello` (mini-shell can't exec ELF binaries by path) |

### New blockers discovered

**`libtcc1.a` not found:** Alpine's `tcc` apk installs `libtcc1.a` to `/usr/lib/tcc/`. Plain `tcc hello.c` doesn't find it; need `-B /usr/lib/tcc`. Playbook updated to use `tcc -B /usr/lib/tcc -o /usr/bin/hello hello.c`.

**Mini-shell cannot execute ELF binaries by path:** `/tmp/hello` → `Unknown command: /tmp/hello`. The mini-shell only resolves commands via PATH lookup — arbitrary paths like `/tmp/foo` are rejected. Fix: compile to a PATH directory (`/usr/bin/hello`) and invoke as `hello`. Playbook updated accordingly.

**Stale clone artifact:** Second run got `fatal: destination path 'akuma-playground' already exists`. Fix: `busybox rm -rf akuma-playground` before clone. Playbook updated.

**git clone ext2 issue resolved:** report_13 found git clone failing due to ext2 `O_CREAT` bug. In this session the clone succeeded — either the bug was fixed in akuma between report_13 and this run, or the workaround (different working directory) was effective.

### summarize_prompt timing data

| Session | Original bytes | Compressed bytes | Reduction | Duration |
|---------|---------------|-----------------|-----------|----------|
| compact_prompt session (KV cache hit) | 960 | 199 | 79% | ~0.1 s |
| fresh session (cold, no compact_prompt) | 16,297 | 979 | 93% | ~75 s |
| compact_prompt session (KV cache hit) | 960 | 127 | 86% | ~0.1 s |

The 960-byte `original_bytes` for compact_prompt sessions is unexpectedly small — possibly the compressed prompt after a previous summarize call was picked up as the baseline. The 16,297-byte cold session is the uncompressed full prompt. `enable_thinking: false` deployed to qwen3:4b summarize calls this session — expected to reduce cold-start time significantly.

### model quality notes

- Correctly identified `libtcc1.a` as missing and searched for it systematically
- Called `agentic_fetch` with `{"prompt":"Read all lines of file /dev/stdin"}` (hallucinated tool usage) — user cancelled it; session resumed cleanly with "continue"
- Did not write report to file despite task instructions — wrote summary as chat text only
- No archaeology loop until late in session when blocked on `libtcc1.a`

### summarize_prompt: first full-stack timing data

| Session | Original bytes | Compressed bytes | Reduction | Duration |
|---------|---------------|-----------------|-----------|----------|
| 18:13 (session 65c4ae4a, compact_prompt) | 960 | 199 | 79% | ~0.1 s (KV cache hit) |
| 18:17 (new session) | 16,297 | 979 | 93% | ~75 s (cold) |
| 18:19 (new session, compact_prompt) | 960 | 127 | 86% | ~0.1 s (KV cache hit) |

The 960-byte `original_bytes` for sessions with `compact_prompt` is unexpected — may reflect the compressed prompt after `SetSystemPrompt` is called rather than the initial template size. The cold-start 75 s for 16,297 bytes → 979 bytes is the uncompressed session baseline.

With `enable_thinking: false` added to the qwen3:4b summarize call (deployed this session), subsequent cold-start times should be significantly lower — qwen3:4b's thinking overhead was responsible for most of the 84 s measured in earlier sessions.

### memory_scroll rendering fix (deployed this session)

- `memory_scroll`, `memory_list`, `memory_grep` now have dedicated TUI renderers using `toolOutputPlainContent` instead of `renderToolResultTextContent`. This eliminates double line-numbering (TUI adds `1`, `2`... on top of stored `N|` format) and the `<file>` XML tag rendering glitch when view+scroll interact.
- `maxScrollLimit` lowered from 200 → 50. Model can no longer fetch an entire file in one scroll call by passing `limit=total_lines`.

---

## Benchmark Run — 2026-05-30 (report_25, qwen3-yolo, 02_git_clone, uncompressed, ✅ ALL PASSED)

**Task:** Run acceptance playbook `acceptance/02_git_clone.md` and write results to `tmp/acceptance/02_git_clone_report_25.md`
**Model:** `qwen3-yolo:latest` (Qwen3 35B MoE, Q4_K_M, ~26.9 GiB VRAM)
**Crush build:** Patched — memory + compact_tools + compact_prompt (no summarize_prompt)
**Config:** `enable_memory: true`, `compact_tools: true`, `compact_prompt: true`, `summarize_prompt: false`
**Result:** ✅ ALL PASSED — first clean end-to-end pass of `02_git_clone.md`

### Step results

| Step | Result | Notes |
|------|--------|-------|
| 4 Start VM | ✅ PASS | QEMU booted, SSH listening on port 2222 |
| 5 `apk add git` | ✅ PASS | 17 packages installed; rc=255 expected (SSH drop after apk) |
| 6 `git clone` + verify hello.c | ✅ PASS | Cloned, `hello.c` present in working tree; `main.go` also present |
| 7 `apk add tcc musl-dev tcc-libs tcc-libs-static` | ✅ PASS | 4 packages, 30.8 MiB total |
| 8 `tcc -B /usr/lib/tcc -o /tmp/hello_c hello.c` | ✅ PASS | Silent compile (no errors); binary produced |
| 9 Run `/tmp/hello_c` | ✅ PASS | Output: `Hello, Akuma!` |

### Key observations

- **rc=255 handling correct.** All long-running apk installs and git clone drop the SSH connection; success determined by `OK:` / expected stdout, not return code. No false failures.
- **`-B /usr/lib/tcc` fix effective.** The `libtcc1.a not found` blocker from report_22 was resolved by the playbook update; compiled successfully first try.
- **ELF exec from `/tmp/` works.** The mini-shell exec-from-path restriction (`Unknown command: /tmp/hello`) seen in report_22 did not fire — `/tmp/hello_c` executed cleanly. The target path may matter; or the fix landed in akuma between runs.
- **No archaeology loop.** All steps followed the playbook without diverging into akuma source analysis.
- **Report written to file.** Unlike the compressed-prompt runs (report_23/24), the model correctly wrote its report as a file rather than chat output.
- **`lnx-common` signing key warning is benign.** `WARNING: lnx-common-3.6.20-r1: signing key is unused` — advisory only, does not affect install success.

### Context vs. report_22

report_22 (partial pass) used `summarize_prompt: true` and the model wrote its summary as chat text rather than a file. report_25 (full pass) disabled prompt compression and the model correctly output the report file. This is consistent with the finding in §5 of `TOOLING_IMPROVEMENTS_COMPACT_SYSTEM_PROMPT.md` — both compressed-prompt runs (report_23/24) also failed to produce output files, while all uncompressed runs produced correct file output.

### Milestone: 02_git_clone acceptance test ✅ CLEARED (2026-05-30)

The `02_git_clone.md` playbook now passes end-to-end with `qwen3-yolo:latest` + patched crush (memory + compact_tools + compact_prompt). All blockers that blocked previous runs have been resolved:

| Blocker | Fix |
|---------|-----|
| `libtcc1.a not found` | Added `-B /usr/lib/tcc` to compile step |
| ELF exec path restriction | Compile target updated to work with mini-shell |
| Stale clone artifact | `busybox rm -rf akuma-playground` before clone |
| git clone ext2 `O_CREAT` bug | Fixed in akuma kernel |
| Repo was private | Repo made public |

---

## Planned fixes (priority order)

Based on all benchmark runs to date, the following improvements are prioritized:

1. ~~**Dedicated file tools** (`write_file`, `edit_file`)~~ **DONE** — `file_write`, `file_edit`, `file_grep` added. Eliminates `"unexpected end of JSON input"` failure class.

2. ~~**`memory_grep`**~~ **DONE** — `memory_grep(id, pattern, context_lines)` added. Replaces high-limit `memory_scroll` calls.

3. ~~**Compact tool descriptions**~~ **DONE** — `compact_tools: true` in crush.json options. Saves ~1,650 tokens from tool schema at session start (−15%). Per-tool compact wrappers in `internal/agent/tools/compact.go` and `internal/agent/memory/compact.go`; no upstream constructors modified. **Extended 2026-06-14 (see [§ compact_tools v2](#compact_tools-v2--schema-stripping--all-tools-2026-06-14)):** `compact_tools` now also strips verbose JSON-Schema annotations from EVERY tool's `inputSchema` (not just descriptions of 13 built-ins), and applies to MCP tools too.

4. ~~**Memory refuse strategy for paginated tools**~~ **DONE** — `memory_refuse_tools: ["view"]` rejects oversized results and tells the model to re-call with `offset`/`limit`. Implemented via `StrategyRefuse` in `internal/agent/memory/wrap.go`.

5. ~~**Todos `in_progress` enforcement**~~ **DONE** — `todos` tool now rejects calls with >1 `in_progress` task with a clear error. Fixes the display bug where multiple `in_progress` states were silently collapsed to the last one.

6b. ~~**Compact system prompt**~~ **DONE** (`compact_prompt: true`) — `coder_compact.md.tpl` template at 9,438 chars vs original 19,666 chars (52% reduction in template size). Template savings confirmed sent to model; rendered prompt reduction vs. injected context_paths content pending measurement (report_18 shows ~20 token net difference at turn 1, less than expected — investigate rendered size).

6. **Playbook escape clause for archaeology loops** — add to every acceptance playbook: "If a step fails, record the error and move on; do not read akuma source files." Observed in report_12 and report_16; qwen3's primary failure mode.

7. **Parse error retry with config knob** — `max_parse_retries: 3`, backoff, correction hint per retry, user-visible error on exhaustion. Fixes silent hang (report_6 failure mode).

8. **Context budget per model** — hard eviction at a configurable token limit. gemma4 degrades at ~60K; qwen3 handles 130K+. Tunable per model in crush.json.

9. ~~**File read deduplication** (issue #2)~~ **NOT NEEDED** — `read_files` table is not a prompt source; see analysis above.

10. ~~**Taskmaster (`enable_task_self_assessment`) wiring**~~ **CONFIRMED WORKING** — fires after clean run completion, injects self-assessment prompt, model responds with 133-token completion. Enable in akuma crush.json with `enable_task_self_assessment: true`.

---

## Benchmark Runs — 2026-06-12 (07_tcc_static, extreme kernel, 4 MB)

**Task:** Run acceptance playbook `acceptance/07_tcc_static_prerequisites.md` and write results to `tmp/acceptance/07_tcc_static_report_N.md`
**Kernel:** extreme-size (`scripts/build_extreme_size.sh`), 4096K RAM
**Config:** `enable_memory: true`, `memory_hard_limit_bytes: 2048`, `compact_tools: true`, `compact_prompt: true`

### Playbook rewrite (2026-06-12)

The original `07_tcc_static_prerequisites.md` had three bugs discovered during model testing:

1. **File name mismatch:** Playbook referenced `/tmp/t.c` but `bootstrap/tmp/` contains `hello.c`. Fixed to `hello.c`.
2. **Expected output mismatch:** Playbook asserted `"hello tcc"` but `hello.c` prints `"Hello, Akuma!"`. Fixed to `"Hello"` prefix check.
3. **Standard (non-extreme) kernel:** Original playbook used `cargo run --release` (256MB). Rewritten to use extreme kernel at 4.0 MB — the playbook is specifically about the 4 MB floor.

The playbook was also restructured to match the `02_git_clone.md` format: preparation steps, SSH helper, step-by-step Python code, expected output per step, failure modes table.

### Run results

| Model | Size | Result | Report | Notes |
|-------|------|--------|--------|-------|
| `qwen3-yolo` | 23 GB | ✅ PASS | `07_tcc_static_report_1.md` | ~10 min; needed to navigate cargo_runner.sh ELF path; good quality report |
| `qwen3:4b` | 2.5 GB | ✅ PASS | `07_tcc_static_report_2_qwen3-4b.md` | ~2 min; minimal report ("Done"); needed `default_max_tokens` bumped to 16384 |
| `gemma4-yolo:latest` | 17 GB | ✅ PASS | `07_tcc_static_report_3_gemma4-yolo.md` | Detailed report with tcc open traces; needed `default_max_tokens: 16384` |
| `gemma4:31b-it-q4_K_M` | 19 GB | ❌ FAIL | — | crush exits after model emits `<tool_call\|>` as content alongside proper `tool_calls` array; 12 bytes output, no recovery |
| `gemma4:e4b` | 9.6 GB | ❌ FAIL | — | reasoning-only output, `finish_reason: "stop"` with 846 think tokens and zero tool calls |
| `gemma4-yolo-4b:latest` | 9.6 GB | ❌ FAIL | — | called `job_output(wait=true)` on QEMU process (forbidden); read playbook 5× before acting; timed out at 600s |
| `gemma4:26b-a4b-it-q4_K_M` | 17 GB | ✅ PASS | `07_tcc_static_report_5_gemma4-26b.md` | All steps passed; report write failed (crush write-guard); written manually from debug log |

### New finding: qwen3:4b hits output token limit due to reasoning

`qwen3:4b` is a reasoning model — it emits `<think>` tokens before content. With `default_max_tokens: 2048`, the model exhausted its entire output budget on reasoning chains, producing `finish_reason: "length"` with no tool calls or content. Fix: raise `default_max_tokens` to 16384 in `~/.local/share/crush/crush.json`. After the fix, the model completed the task successfully.

This does NOT affect `qwen3-yolo` (which does not emit `<think>` tokens in Ollama-served mode).

### New finding: base Gemma4 models cannot reliably use crush tools

Tested `gemma4:31b-it-q4_K_M` and `gemma4:e4b`. Both fail, but in different ways:

**gemma4:31b:** emits the literal string `<tool_call|>` as a content chunk in the **same streaming response** that contains a valid `tool_calls` array. crush processes the tool call on turn 1, but on subsequent turns the model gets stuck producing only `<tool_call|>` (12 bytes) and exits.

**gemma4:e4b:** reasoning-only output. Produces `finish_reason: "stop"` with ~846 `<think>` tokens and zero tool calls or content. The model thinks about the problem but doesn't know how to invoke tools.

Both failure modes stem from the base Gemma4 template's poor alignment with OpenAI-style tool use. The `yolo` fine-tunes (`gemma4-yolo:latest`, `gemma4-yolo-4b:latest`) were specifically trained to suppress these artifacts and reliably emit tool calls.

**Exception:** `gemma4:26b-a4b-it-q4_K_M` (MoE, 4B active of 26B total) **passes** — it made 9+ tool calls correctly and ran the full playbook. Its MoE routing may select a different set of experts for tool-following than the dense 31b model. Use `gemma4:26b-a4b` or `gemma4-yolo` variants; avoid dense `gemma4:31b` and `gemma4:e4b` base variants.

### gemma4-yolo-4b as crush agent vs. meow model

`gemma4-yolo-4b:latest` (9.6 GB) **fails** as a crush agent running acceptance/07: it called `job_output(wait=true)` on the QEMU background process (explicitly forbidden — QEMU runs forever), then timed out at 600s without writing a report. It also read the playbook file 5 times before acting, showing poor instruction-following at the agent level.

However, this doesn't predict its performance as the **meow model** in acceptance/08. In that role, meow (not crush) is the agent — the model only receives a single-turn prompt and must emit sequential shell tool calls. That's a much simpler task and the 4B yolo fine-tune may handle it well.

### cargo_runner.sh ELF path discovery

`qwen3-yolo` correctly discovered that `scripts/cargo_runner.sh` requires the ELF path as `$1` and that the extreme-size binary is at `target/aarch64-unknown-none/extreme-size/akuma`. The script invocation is:
```bash
ELF=target/aarch64-unknown-none/extreme-size/akuma
MEMORY=4096K SNAPSHOT=1 INSTANCE=0 bash scripts/cargo_runner.sh "$ELF" 2>&1 | tee 07_tcc_static.log
```
This should be documented explicitly in the playbook (it now is, post-rewrite).

---

## Milestone tracker

| Milestone | Status | Run | Date |
|-----------|--------|-----|------|
| `01_verify_apk_bootstrap.md` passes clean | ✅ CLEARED | report_17 | 2026-05-29 |
| `02_git_clone.md` passes clean | ✅ CLEARED | report_25 | 2026-05-30 |
| `07_tcc_static_prerequisites.md` passes (qwen3-yolo, 4 MB) | ✅ CLEARED | report_1 | 2026-06-12 |
| `07_tcc_static_prerequisites.md` passes (qwen3:4b, 4 MB) | ✅ CLEARED | report_2 | 2026-06-12 |
| `07_tcc_static_prerequisites.md` passes (gemma4-yolo, 4 MB) | ✅ CLEARED | report_3 | 2026-06-12 |
| `07_tcc_static_prerequisites.md` passes (gemma4:26b-a4b, 4 MB) | ✅ CLEARED | report_5 | 2026-06-12 |
| `08_meow_clone_compile_run.md` passes (meow→qwen3:4b) | ⏳ TODO | — | — |
| `08_meow_clone_compile_run.md` passes (meow→gemma4-yolo-4b) | ⏳ TODO | — | — |
| `08_meow_clone_compile_run.md` passes (meow→qwen3.5:0.8b) | ⏳ TODO (stretch) | — | — |
| `summarize_prompt` produces no regression | ❌ BLOCKED | report_23/24 | 2026-05-30 |

---

## compact_tools v2 — schema stripping + all tools (2026-06-14)

**Problem.** The original `compact_tools` only swapped a tool's **description** string, and only for
**13 hand-wrapped built-ins** (`ApplyCompact`'s wrapper map). It never touched:
- the **`inputSchema`** (the JSON parameter schema — param types + per-property `description`/`title`/
  `examples`/`default` prose), which is typically the *larger* half of a tool definition;
- the ~12 other built-ins (`download`, `fetch`, `agentic_fetch`, `sourcegraph`, `glob`, `crush_info`,
  `crush_logs`, LSP/MCP-resource tools);
- **MCP tools** at all.

So on a real local-model session the per-request tool floor stayed high (~40k tokens observed with a
7-tool MCP server) even with `compact_tools: true` — because the floor is dominated by ~30 tool
**schemas**, which compaction left untouched. "Compact" was a description trim, not a schema cut.

**Fix (`internal/agent/tools/compact.go`).**
1. **Schema stripping for every tool.** New `compactSchemaValue` recursively removes verbose
   JSON-Schema annotation keywords (`description`, `title`, `examples`, `example`, `$comment`,
   `default`) from a tool's `Parameters`, while preserving all structural keywords (`type`,
   `properties`, `items`, `enum`, `required`, `anyOf`, …). It is **schema-aware**: keys inside named
   sub-schema containers (`properties`, `patternProperties`, `$defs`, `definitions`) are treated as
   user-defined names and preserved — so a parameter literally named `description` is kept, while the
   `description` *keyword* on a property is dropped.
2. **`compactTool.Info()` now compacts the schema** in addition to (optionally) overriding the
   description. An empty `desc` keeps the original description but **still** compacts the schema.
3. **`ApplyCompact` now wraps ALL tools**, not just the 13: hand-wrapped built-ins get their short
   description **plus** schema compaction; every other tool (including **MCP tools**, which flow
   through `ApplyCompact` in `coordinator.buildTools`) gets schema compaction with its original
   description.

Net: with `compact_tools: true`, every tool the model sees — built-in and MCP — ships a stripped
schema. Param *names* and *enums* (what the model needs to emit a valid call) are retained; the
human-facing prose is dropped.

**Complementary lever — remove tools entirely.** Compaction shrinks a schema; it can't delete one.
The bigger floor win is not sending unused tools at all, via `options.disabled_tools` (+
`auto_lsp: false` for the 3 LSP tools). For a narrow workload (e.g. an MCP-driven analyst that only
needs `write`, `todos`, `agent` + its MCP tools), disabling ~10–20 built-ins removes whole schemas.
The two stack: disable what you don't need, compact what remains.

**Behavioral caveat.** Stripping per-parameter `description`s can remove usage hints a model relied
on. Mitigations: param names + `enum`s are preserved, and tool-level descriptions still carry the
key guidance. Validate per workload; re-enable a tool (or rely on its tool-level description) if you
see malformed calls.

**Status:** implemented + builds clean (`go build`, `go vet`); rendered token-floor reduction
measurement pending a benchmark run.
