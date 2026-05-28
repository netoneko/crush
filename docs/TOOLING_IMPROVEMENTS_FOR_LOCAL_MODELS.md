# Tooling Improvements for Local Models

Observations from running crush with local ollama models (qwen3 35B MoE, gemma4 4B/26B) on agentic
acceptance test playbooks. Local models have smaller effective context windows and slower inference
than cloud APIs, so context bloat and tool output size matter more.

All results greater than X rows/tokens/size go into memory to create a reference and the LLM gets a snippet and a sample and can query the memory later by scrolling them via dedicated tools

## Fix

Crush should store tool results (logs, spans, code search hits) in an in-memory
store so the LLM can query them later without re-calling rate-limited APIs.

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

### InMemory

Just a map, contents lost across sessions. First phase.

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

## 5. Small model name validation at startup

**Problem:** crush.json `models.small` can reference a model that doesn't exist in ollama. The
failure is silent — title generation falls back to the large model without surfacing an error to the
user. This wastes a large-model inference call on a trivial task every session.

**Observed:** `gemma4:yolo-4b` was configured but the model was created as `gemma4-yolo-4b`
(colon vs hyphen). Not caught until the log showed `model 'gemma4:yolo-4b' not found`.

**Fix:** Validate configured model names against the provider at startup and warn if any are
missing.
