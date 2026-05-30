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
