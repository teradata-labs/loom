# Loom Context & Skill Management — Test Design

**Type:** test design — the spec a test lead reads to write the suite for the
mutation design (`loom-segmented-memory-redesign.md`). It says *what to assert
and why*, not how to code it. No Go, no framework names.
**Grounding:** the mutation design's §4 invariants and §5 acceptance. All
vocabulary is main's — ROM / Kernel / L1 / L2 / Swap, compaction, compressor,
large-result threshold. Nothing here is a prior design's shape.
**Two arms, one instrument:** a *structural* arm (assert the invariants) and a
*consumer* arm (an LLM judges the context from the reader's seat) both read
the **same per-turn capture** — the M9 context-dump. Neither reconstructs
context; both judge exactly what the model was handed.

---

## 1. Purpose & the seat you test from

The object under test is **the compiled context** — the exact `(messages,
tools)` handed to the provider each turn — not the `SegmentedMemory` struct.
A segment layout is only ever right or wrong *as the model reads it*, and the
model reads the compiled context. Every assertion below inspects that, never
the internal fields.

This is why there is a consumer arm at all: some failures (a stale skill
claim, an approval that lost its referent, a tool result that reads as Go-map
soup) are not structural violations — the bytes are well-formed — but are
wrong *to the reader*. Structural tests catch shape; the consumer eval catches
sense.

---

## 2. The instrument — the M9 context-dump

Both arms read the M9 debug context-dump (mutation M9): a switch at
`chatWithRetry` serializes the exact `(messages, tools)` before every provider
call, one record per turn, tagged with session id and turn number. Paired with
it, debug logs at each mutation point (compaction, skill load, large-result
storage, per-session tool assembly) explain *how* a turn's context came to be.

The test lead drives a session with a **scripted LLM** (deterministic tool
calls, no network), enables the dump, and reads the resulting per-turn records.
The structural arm asserts on the records; the consumer arm feeds them to a
judge model. A failing invariant is traced to its cause through the debug log
of the same turn.

```
   scripted LLM turn script
            │
            ▼
     ┌─────────────┐   per-turn (messages, tools)   ┌────────────────────┐
     │ Agent.Chat  │ ─────────  M9 dump  ─────────▶  │  dump records[]    │
     │  (real loop)│                                 └─────────┬──────────┘
     └─────────────┘                                           │
            │ debug logs (compaction / load / store / tools)   │
            ▼                                    ┌─────────────┴─────────────┐
     trace a failure                             ▼                           ▼
     to its turn                        structural arm              consumer arm
                                    assert the 15 invariants     judge each turn from
                                    on each record               the reader's seat
```

---

## 3. Assertion catalog — the 15 invariants

Each invariant is stated as *observe in the dump → pass condition*. Numbers
match §4 of the mutation design.

### Head stability
1. **Tools block stability.** *Observe* the `tools` array across consecutive
   turns. *Pass:* byte-identical between two turns unless **this session's
   own** skill load (or its own first-need tool disclosure) happened between
   them. No other session's activity changes it.
2. **ROM stability.** *Observe* the system slot's ROM segment across the whole
   session. *Pass:* byte-identical every turn, including the static skill list.
3. **One system injection.** *Observe* what occupies the system slot each turn.
   *Pass:* ROM only (plus the cumulative L2 summary once compaction has run).
   No per-turn skill, pattern, or findings block ever appears.

### Append-only conversation
4. **Prefix extension.** *Observe* the `messages` list on turn N vs. N−1
   **between two compactions**. *Pass:* turn N is a strict prefix-extension —
   every prior message byte-identical, only the new turn appended.
5. **Interior is immutable except by compaction.** *Observe* any change to an
   already-compiled message. *Pass:* the only operation that rewrites the
   interior is a compaction (oldest batch → L2). Nothing else edits it.

### Compaction
6. **Budget-driven trigger.** *Observe* the turn compaction first fires,
   against the debug log's budget usage. *Pass:* it fires only when total
   budget crosses the warning zone of the real window — never at a fixed L1
   token cap. Before that, L1 grows freely.
7. **Oldest batch, pairs intact, floor held.** *Observe* what compaction
   moved. *Pass:* the **oldest** batch was compressed into a **cumulative** L2
   summary; at least `minL1Messages` recent turns remain verbatim; no
   `tool_use`/`tool_result` pair was split across the cut.
8. **Approvals survive with scope.** *Observe* an approval given before a
   compaction (e.g. "approved: SELECT on Passenger_Data") after that
   compaction. *Pass:* the L2 summary still carries the decision **and the
   exact thing it approved**; the referent is not lost. The full
   pre-compaction history is recoverable from Swap.

### Skills
9. **Skill enters only as a load message.** *Observe* how a skill body appears.
   *Pass:* only as a `manage_skills(load)` tool-result message appended to L1 —
   plain-text body with a one-line load confirmation. No per-turn skill surface
   anywhere.
10. **Single source of "loaded".** *Observe* what `manage_skills(list)` reports
    and which tools are advertised, vs. the conversation. *Pass:* both derive
    from the orchestrator active set maintained by load events; nothing
    re-derives "loaded" from the conversation per turn, so nothing diverges.
11. **Load registers tools; no unload, no cap.** *Observe* the tools block
    right after a load. *Pass:* the skill's required tools are advertised from
    that turn on; they are never de-registered by an unload; there is no
    skill-count cap that blocks a load or evicts a skill.
12. **High-risk load gates.** *Observe* the result of loading a high-risk
    skill without approval. *Pass:* an explicit approval-required result, never
    a silent skip and never a silent activation.

### Isolation
13. **Clean rendering.** *Observe* a tool result whose data is a composite
    (e.g. `manage_skills(list)`) and one whose data is a string (a skill body).
    *Pass:* the composite renders as JSON; the string renders verbatim; neither
    renders as `fmt %v` of a Go map.
14. **Cross-session isolation.** *Observe* session B's dump while session A
    loads a skill and stores a large result. *Pass:* A's load never changes B's
    advertised tools; B can never address A's stored errors / results / blobs.

### Large results
15. **Bounded offload with an exempt set.** *Observe* tool results by size.
    *Pass:* a result at/above the threshold is stored by reference (preview +
    handle inline); the default threshold is positive (64 KiB), never −1;
    results below stay inline and readable; the exempt set (skill body,
    `query_tool_result`/`get_tool_result`) always enters whole.

---

## 4. Structural scenarios

Three scenarios drive the real `Agent.Chat` loop with a scripted LLM. Each
lists its turn script, what to observe in the per-turn dumps, and which
invariants it settles.

### S1 — Grant workflow, ~20 turns (the primary trace)

Mirrors §3 of the mutation design: a DBA (Doug) grants data-scientist access
through a loaded skill, across load → work → compaction → restart → second
grant. The loaded skill is **high-risk** (it grants database access), so this
scenario also exercises the load gate.

- **T1.** User asks for the grant. Script: model reads ROM's static skill list,
  calls `manage_skills(load, <grant-skill>)`. First load returns the gate
  (inv 12); after approval, the body enters L1, the orchestrator records it,
  its tools register for this session.
- **T2–T6.** Profiling reads; some results ≥ 64 KiB (stored by reference);
  sensitivity + readiness report; Gate-1 approval as a verbatim user message.
  Total context stays under warning — no compaction.
- **T7–T10.** SQL generation, gates 2–3, execution. L1 grows freely (inv 6).
- **T11.** Total crosses the warning zone → **compaction** fires. Oldest batch
  → cumulative L2; Gate-1's approval-with-scope preserved; pairs intact; floor
  held.
- **T12–T16.** More compaction as pressure recurs; L2 evicts oldest to Swap at
  its cap.
- **Restart between T17 and T18.** New process replays the durable
  conversation, then re-fires each load event — re-activate + re-register
  tools. T18's dump must match a live session: tools present, `list` truthful.
- **T18–T20.** A second skill loads; two bodies coexist as messages; compaction
  absorbs the older under pressure. Runs to completion.

*Observe:* system slot = ROM only every turn (inv 3, 2); tools block stable
except at this session's own loads (inv 1); `messages` append-only between
compactions (inv 4, 5); at T11 the interior rewrites only by compaction and the
approval survives with scope (inv 6, 7, 8); skill body present as an L1 load
message (inv 9); `list`/tools agree with the active set (inv 10); tools stay
advertised across compaction and restart, no cap blocks the second load
(inv 11); restart yields a live-identical session (inv 11).

*Settles:* 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12.

### S2 — Data-heavy

Drive turns whose tools return results straddling the threshold, plus a
composite-data tool and a skill body.

- Results just below 64 KiB → stay inline, directly readable.
- Results at/above 64 KiB → stored by reference (preview + handle), retrievable
  via `query_tool_result`; the recall tool's own result is never re-offloaded.
- A `manage_skills(list)` result (composite) and a `manage_skills(load)` body
  (string) in the same run.

*Observe:* inline vs. referenced strictly by size; exempt set always whole;
composite renders JSON, body renders verbatim; no findings block appears
despite many tool results.

*Settles:* 13, 15 (and reconfirms 3).

### S3 — Multi-session isolation

Two sessions, A and B, on one `Agent`.

- A calls `manage_skills(load)` (registering tools for A) and stores a large
  result (a handle in A's partition).
- Capture B's dump across the same wall-clock turns.

*Observe:* B's tools block is unchanged by A's load; B cannot resolve A's
result handle.

*Settles:* 1 (per-session tools), 14.

---

## 5. LLM-consumer eval

After the structural arm is green, feed **each dumped turn** to a judge model
positioned as the consumer: *"This is the context you were handed for this
turn — is it the one you'd want to reason on?"*

**Clean verdict (required):**
- No contradiction between what the transcript says happened and what is
  resident (e.g. a skill the transcript loaded but whose instructions are
  gone with no reload pointer).
- No stale skill claims — nothing asserts a skill is active that the active set
  does not hold.
- No lost approval scope — every approval still names what it approved.
- Tool results are readable — data, not Go-map dumps or dangling handles.

The eval judges sense, not shape; a run can be structurally green and still
fail here (an approval technically preserved but summarized into ambiguity).
Both must pass.

---

## 6. Test-sample pattern

The canonical end-to-end test, for the test lead to replicate per scenario:

> **enable the dump → drive all turns with the scripted LLM → assert the
> invariants on each dumped turn → run the consumer eval over the dumps.**

Build the eval runner fresh: read the dump records, call the judge model,
report concerns. One scenario = one such test. Keep names grounded in this
design's vocabulary.

---

## 7. Scenario × invariant matrix

Every invariant is settled by at least one scenario — no silent gaps.

| Inv | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15 |
|-----|---|---|---|---|---|---|---|---|---|----|----|----|----|----|----|
| S1  | ● | ● | ● | ● | ● | ● | ● | ● | ● | ●  | ●  | ●  |    |    |    |
| S2  |   |   | ● |   |   |   |   |   |   |    |    |    | ●  |    | ●  |
| S3  | ● |   |   |   |   |   |   |   |   |    |    |    |    | ●  |    |

Invariants 13 and 15 are exercised only by S2, and 14 only by S3 — so those
scenarios are not optional. The consumer eval (§5) runs over all three
scenarios' dumps.
