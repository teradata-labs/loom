# Loom Segmented Memory — Target Design (HLD)

**Scope:** How context is broken into segments, compiled per iteration, and relieved under pressure — for one conversation. Sub-agent contexts are a separate surface (see unknowns in `loom-context-management-deficiency.md`).
**Companions:** `loom-context-management-deficiency.md` (the diagnosis this design answers), `loom_context_arch_v4.md` (skill load-as-event; this design supplies its missing retention layer).
**Validation:** the mechanisms here were derived from first principles, then independently confirmed against Claude Code v2.1.199's actual implementation extracted from the binary (§8).

---

## 0. Problem Statement

Loom organizes context into five memory segments (ROM / Kernel / L1 / L2 / Swap), and that structure is sound. The problem is what happens to the conversation *inside* them: **L1 — the live conversation — is capped at a few thousand tokens, so on any real workload it is continuously shredded, oldest-first, into a lossy summary.** Evicting by age throws away exactly what the agent needs (the loaded procedure, what the user just approved) and keeps what it doesn't (raw data already digested into a conclusion). The agent loses track of where it is and what was decided, and the results become unreliable. *(Full diagnosis: `loom-context-management-deficiency.md`.)*

This document proposes the fix. In one sentence — each term is defined as you go:

> Context is a compiled view over segments; items are managed by **role, not age**; pressure is relieved by **discarding recoverable ballast**; summarization happens at **one rare, guarded event**; repeated summarization is an **alarm, not a mechanism**.

**How to read this doc.** §1 is the **segments** — the structure Loom already has — shown as *today vs proposed*, so you can see exactly what changes. §2 introduces the one genuinely new idea, **roles**, and maps them onto those segments. §3–§5 are the mechanics: the **context events** that change what's in context, *what the model receives* each iteration (compilation), and *how pressure is relieved* (the pipeline). §7 is a full worked conversation you can trace step by step. If you read only two sections, read §1 and §2.

---

## 1. Loom Segmented Memory — what they hold today, and what changes

Loom already organizes context into five segments, and **the segments are good — they are kept, unchanged in name and role.** The redesign changes only *what goes into each one and when items move between them*. We start here because this is the part that already exists and that everyone already understands.

### 1a. What the segments hold **today**

| Segment | Contents today | Behavior today |
|---|---|---|
| **ROM** | Base prompt + docs (identity, tool guidance). | Static for the session. |
| **KERNEL** | Tool definitions, schema cache, recent tool results, findings. | Near-constant. |
| **L1** | A small cache of the most recent messages, capped at ~**4,000–9,600 tokens**. | When it exceeds the cap, the **oldest** messages are removed and summarized into L2. |
| **L2** | A running summary of older conversation. | **Grows continuously** as L1 drips into it. |
| **SWAP** | DB-backed cold storage. | Holds L2 overflow. |

The problem this shape creates, in one line: L1 is a *tiny* window, so on any real workload the conversation is **continuously shredded** into L2 — oldest-first, by age — losing what was gathered and what was approved, while L2 fills with lossy stock phrases ("Tool result received"). *(Full diagnosis: `loom-context-management-deficiency.md`.)*

### 1b. What the segments hold **after the redesign**

```
┌─ ROM ───────────── identity + environment. Session-constant. Never touched.   (unchanged)
├─ KERNEL ────────── tool definitions, structural facts. Near-constant.         (unchanged)
├─ L1 (CONVERSATION) THE timeline: the whole conversation, APPEND-ONLY, VERBATIM.
│                    No small cap — bounded only by the model's budget.
│                    → stops being a cache; becomes the conversation itself.
├─ L2 (RESIDUE) ──── EMPTY until a rare "fold" event; then one summary + the
│                    carried-forward important items. Written once, never dripped.
└─ SWAP ──────────── recoverable store. Bulk data evicted here leaves a one-line
                     stub in L1; the model recalls it on demand (a "page-fault handler").
```

The whole change in one line: **L1 stops being a cache and becomes the conversation; L2 stops being a continuous drain and becomes the residue of a rare event; Swap stops being a graveyard and becomes a recovery path.** In the common case — a session that never hits the budget — **L2 stays empty** and Swap holds only oversized data by reference. Nothing is summarized at all.

---

## 2. Message Roles

Today, when L1 overflows, Loom evicts the **oldest** messages. Age is the wrong thing to evict on: in a real workflow the oldest item is often the *most* important (the loaded procedure, the original request) and the newest is often the *most* disposable (a raw data dump already digested into a conclusion). Age-based eviction throws away exactly what the agent needs and keeps what it doesn't. **The fix is to classify each item by its *role* and let role — not age — decide what survives.**

Four roles, assigned **once** when an item enters, purely by **structure** — who produced it and through which tool — never by reading the text (semantic detection would be unreliable):

| Role | What it is | Structural rule (how it's detected) |
|---|---|---|
| **CHARTER** | instructions currently in force | result of a *loader* tool (`manage_skills` / `manage_patterns(load)`) |
| **LEDGER** | decisions, approvals, commitments | **every user message** + results of *state-changing* tools + `contact_human` results |
| **BALLAST** | bulk, reproducible data | results of *read-only data* tools (profiling, reads, query output; whitelisted) |
| **NARRATIVE** | the working reasoning | everything else (assistant text, tool calls) — the default |

**How roles map to the segments.** Roles are not a new place — they are labels on items *inside L1 (the conversation)*, and they decide how each item is treated when pressure hits and where it goes on a fold:

| Role | Lives in | Under memory pressure (the "valve") | On a "fold" (rare) |
|---|---|---|---|
| **CHARTER** | L1 | never evicted | carried **verbatim** into L2 |
| **LEDGER** | L1 | never evicted | carried **verbatim** into L2 (with its adjacency pair — the question that its "approved" answered) |
| **NARRATIVE** | L1 | not touched | **summarized** into L2 |
| **BALLAST** | L1 → SWAP | **first to be evicted** (recoverably, leaving a stub) | already a stub; stub stays |

Two rules make classification safe and cheap:
- **Safety asymmetry.** The only harmful mistake is calling something *ballast* that isn't (ballast can be evicted/summarized). So ballast is an explicit **whitelist**, and anything unrecognized defaults to a *retained* role. A misclassification costs tokens, never correctness.
- **Ledger superset rule.** "Is this user message an approval?" can't be reliably detected — so we don't try. **Every** user message is ledger. User text is a few percent of any session, so keeping all of it is cheap, and it turns approval-preservation into a structural guarantee instead of a detection problem.

---

## 3. Context events — everything that changes what's in context

The context is not static. A small, fixed set of operations — **context events** — are the only things that ever change what's in the segments. This section says what each one *is*; §5 covers *when* the pressure-driven ones fire. (The loop tick itself — one **iteration** = compile → call the LLM → run its tools → admit the results — is the unit you already know; context events are what happens *to* the context.)

| Context event | What it is |
|---|---|
| **ADMIT** | A new message enters the conversation — a user message, the assistant's reply, or a tool result. |
| **DIVERT** | An oversized tool result is kept out of the conversation and stored aside, leaving a short reference ("stub") in its place. |
| **VALVE** | Bulk data already in the conversation is moved out to free space, leaving a stub behind. Reversible. |
| **RECALL** | Data that was set aside (by DIVERT or VALVE) is brought back into the conversation on demand. |
| **FOLD** | The conversation is compacted — disposable parts summarized, important parts kept verbatim. The one lossy event. |
| **NOTE** | A temporary, single-iteration message (a skill menu, a harness reminder) is attached, then dropped. |

Of the six, only **FOLD** loses anything — and only as a last resort (§5). The other five are lossless or reversible, which is what lets the model run near its budget for a long time without degrading.

---

## 4. Compilation — what the LLM actually receives each iteration

"Compilation" is the step that assembles the segments into the concrete payload sent to the model at the start of each iteration. The whole payload is just three parts, in this order:

```
system   = [ROM] [KERNEL]                          ← byte-stable across iterations → cached
messages = [L2 residue, if any]                    ← changes only at a fold
           [L1, verbatim, append-only]             ← grows monotonically → cached prefix
           [turn-scoped notes]                     ← THIS iteration only, then gone
```

Two rules make this correct:
- **Everything dynamic is a message, never system.** Skill menus, harness notices, reminders — appended as turn-scoped notes at the tail. Mutating the system blob per turn (today's design) destroys the cache and the event/state distinction at once.
- **The prefix is append-only between folds.** Nothing already compiled is rewritten. One rule buys both the caching invariant and "position is carried by order."

---

## 5. Pressure pipeline — how the budget is kept, in four escalating stages

This is the heart of the design: what happens as the conversation grows toward the model's limit. Three of the six context events (§3) are driven by budget pressure — **DIVERT, VALVE, FOLD** — plus a safety stop, the **BREAKER**. They form four stages in order of escalation. **Most iterations hit only the first (do nothing).** Each later stage engages only when the previous one can't keep the context under budget — so summarization (the lossy step) is the *last* resort, not the routine one.

Budget = window − output reserve. Zones: GREEN < ~70% · YELLOW ~70–85% · RED > ~85%.

```
ADMISSION ──► (GREEN: do nothing) ──► VALVE (yellow) ──► FOLD (red) ──► BREAKER
 the door        most sessions          discard,           summarize      alarm
                 live & die here        recoverable        once
```

**Stage 0 — ADMISSION / the DIVERT event (the bouncer).** Per-tool inline size cap. An oversized result never enters L1 whole: L1 gets a preview + handle ("4,812 rows stored as S9, first 5 shown — query via `query_tool_result`"), full data goes straight to Swap. Without this, a single 25k-token `SELECT *` exceeds the whole budget and no downstream stage can save it. *Loom already has this machinery (`handleLargeResult`, `SQLResultStore`, `SharedMemoryStore`, `query_tool_result`) — today it ships disabled (threshold −1); turning it on is a config change.*

**Stage 1 — VALVE (yellow).** Evict BALLAST items — oldest first, **whole**, each persisted to Swap and replaced in-place by a one-line stub (`what it was + how to recall it`). Touches only ballast; charter, ledger, narrative are immune regardless of age. **Min-payoff rule:** the valve fires only when it reclaims a large batch at once (Claude's constant: ≥20,000 tokens) — because a stub-in-place edit invalidates the prompt cache from that position forward, and the reclaim must dwarf the one-time re-read. Rare bulk cleanups; never nickel-and-dime. This stage is what lets a session sit near the ceiling for many iterations without losing anything that matters.

**Stage 2 — FOLD (red, valve exhausted).** One event:
- **Carried verbatim** (skipped, never re-worded): charter in force; the ledger with adjacency pairs.
- **Summarized** (one LLM pass, structured: state reached, decisions, open commitments, recall pointers): the narrative.
- **Evicted whole → Swap:** remaining ballast; **plus the entire pre-fold L1 transcript** (so even summarized content is recoverable).
- **Re-hydrated** (reconstructed fresh, not preserved): whatever is live right now — the artifact being edited, active task state. Retention of the working set by reconstruction.
- L2 := summary + carried items. L1 restarts; append-only resumes.

**Stage 3 — BREAKER.** A re-fold within a few user turns of the previous one, repeatedly (Claude: within 3 turns, 3×), is not memory management — it is a pathology (something oversized is being re-admitted). Stop compacting, surface to the user. **Repeated summarization is a failure state, never an operating mode.** Today's Loom's steady state *is* this pathology.

**Where summarization happens — the complete answer: in exactly one place, the fold's narrative pass. Nowhere else, ever.** The admission gate diverts, the valve discards (recoverably), the fold summarizes (once, guarded), the breaker halts.

---

## 6. Invariants (the testable spec)

1. CHARTER and LEDGER items are never paraphrased, summarized, or valve-evicted.
2. L1 is verbatim and append-only between folds; nothing already compiled is rewritten (sole exceptions: stub-in-place at a valve event; the one fold restart).
3. Anything removed from L1 is recoverable by an explicit act (stub → Swap recall). Recalled content returns as a **new message at the tail** — never re-inflated into its old position.
4. Summarization occurs only at a fold; a fold occurs only at RED; repeated folds trip the breaker.
5. Everything per-iteration-dynamic is a turn-scoped message, never a mutation of system or history.
6. An item's treatment is a function of its role, never its age alone.
7. The valve fires only above the min-payoff threshold.

---

## 7. The worked example — verbatim, step by step

One fixed conversation (Doug's `new-data-access` onboarding), shown as a sequence of steps (B1–B18); each step is either a user message or one assistant iteration. Rules in force: budget 20,000 · YELLOW 14,000 · RED 17,000 · ROM+Kernel = 3,000 fixed, always counted. The pressure checks (VALVE/FOLD) run at the start of every iteration, before the LLM is called.

**B1 — USER:** *"Give data scientists read-only access to test_nda_titanic_db. Create role data_science, grant SELECT on Passenger_Data."*
```
adds:  M1 [LEDGER, 40]
L1: [M1]                                              total  3,040
L2: —                    Swap: —
```

**B2 — ASSISTANT:** check: GREEN → loads the skill.
```
adds:  M2 [call, 15]  manage_skills(load,"new-data-access")
       M3 [CHARTER, 6000]  "## Skill: New Data Access. Step 1... ⛔ GATE 3: no DDL..."
L1: [M1 M2 M3]                                        total  9,055
L2: —                    Swap: —
```

**B3 — ASSISTANT:** GREEN → discovers database and tables.
```
adds:  M4 [call,12] M5 [BALLAST,180] "DBC, td_metrics, test_nda_titanic_db (2.1GB)..."
       M6 [call,12] M7 [BALLAST,150] "Passenger_Data(891), Crew_Roster(908), Ticket_Ledger(1309), Lifeboat_Manifest(20)"
L1: [M1..M7]                                          total  9,409
L2: —                    Swap: —
```

**B4 — ASSISTANT:** GREEN → reads columns.
```
adds:  M8 [call,12]  M9 [BALLAST,420] "PassengerId INT, Name VARCHAR(200), Sex, Age, Fare, Cabin VARCHAR(20)..."
       M10 [call,36] M11 [BALLAST,1300] "Crew_Roster: FullName VARCHAR(150)... (3 tables)"
L1: [M1..M11]                                         total 11,177
L2: —                    Swap: —
```

**B5 — ASSISTANT:** GREEN → classifies sensitivity, counts rows.
```
adds:  M12 [NARRATIVE,700] "Name=HIGH PII; Sex/Age=MEDIUM; Cabin=MEDIUM location; Fare=LOW..."
       M13 [BALLAST,90] "891 / 908 / 1309 / 20"
L1: [M1..M13]                                         total 11,979
L2: —                    Swap: —
```

**B6 — ASSISTANT:** GREEN → readiness report, **no tool call → turn ends, control to user.**
```
adds:  M14 [NARRATIVE,900] "READINESS REPORT: exclude Name,Cabin,Ticket... ⛔ GATE 1 — reply 'approved'"
L1: [M1..M14]                                         total 12,879
L2: —                    Swap: —
```

**B7 — USER:** *"approved"*
```
adds:  M15 [LEDGER, 5]
L1: [M1..M15]                                         total 12,884
L2: —                    Swap: —
```

**B8 — ASSISTANT:** check: 12,884 → GREEN → generates SQL, ends turn.
```
adds:  M16 [NARRATIVE,1400] "CREATE ROLE data_science; CREATE VIEW nda_gov.v_passenger_data AS
       SELECT PassengerId,Pclass,Sex,Age,Fare,Embarked...; GRANT SELECT...; -- ROLLBACK...
       -- TESTS...  ⛔ GATE 2 — reply 'approved'"
L1: [M1..M16]                                         total 14,284   ← now over 14,000
L2: —                    Swap: —
```

**B9 — USER:** *"approved"*
```
adds:  M17 [LEDGER, 5]
L1: [M1..M17]                                         total 14,289
L2: —                    Swap: —
```

**B10 — ASSISTANT:** check: 14,289 → **YELLOW → VALVE fires before the LLM is called.** Oldest BALLAST only; CHARTER/LEDGER/NARRATIVE untouchable:
```
MOVED L1→Swap:   M5 →swap:S1   M7 →swap:S2   M9 →swap:S3   M11 →swap:S4    (−2,050)
LEFT IN PLACE:   M5'="[evicted: databaseList → recall_context('S1')]"       (+15 each)
                 M7'、M9'、M11' likewise
new total: 14,289 − 2,050 + 60 = 12,299 → compile → LLM asks Gate 3, ends turn.
adds:  M18 [NARRATIVE,60] "⛔ GATE 3 — I will execute the setup SQL. Approve?"
L1: [M1 M2 M3 M4 M5' M6 M7' M8 M9' M10 M11' M12 M13 M14 M15 M16 M17 M18]   total 12,359
L2: —                    Swap: S1(180) S2(150) S3(420) S4(1300)
```

**B11 — USER:** *"approved"*
```
adds:  M19 [LEDGER, 5]
L1: [...M19]                                          total 12,364
L2: —                    Swap: S1 S2 S3 S4
```

**B12 — ASSISTANT:** GREEN → executes. Results of a *mutating* tool = LEDGER (execution record), not ballast.
```
adds:  M20–M27 [calls 96 + LEDGER 200] "CREATE ROLE — OK" "CREATE VIEW — OK" "GRANT — OK"...
L1: [...M27]                                          total 12,660
L2: —                    Swap: S1 S2 S3 S4
```

**B13 — ASSISTANT (same user turn):** GREEN → writing tests, needs the exact column list — it's a stub now. **Reverse flow Swap→L1:** recall returns as a *new tail message*, old position stays a stub.
```
adds:  M28 [call,12] recall_context("S3")
       M29 [BALLAST,420] "PassengerId INT, Name VARCHAR(200)... Cabin VARCHAR(20)..." (same bytes as old M9)
       M30 [BALLAST,3000] test query dump "TOP 5 rows: (1,3,'male',22.0,7.25,'S')..."
L1: [...M30]                                          total 16,092
L2: —                    Swap: S1 S2 S3 S4  (S3 still there — recall is a read)
```
*(Note: with Stage-0 admission ON, M30 would have entered as preview+ref and B14–B15 pressure would be far milder. It is shown entering whole to exercise the valve-exhausted → fold path.)*

**B14 — ASSISTANT:** check: 16,092 → YELLOW → valve tries: only old ballast left is M13(90) — M29/M30 are too recent. Reclaims 90. **Valve exhausted.** Compile → test analysis.
```
adds:  M31 [NARRATIVE,600] "Positive SELECT ok; Name/Cabin rejected; counts match"
       M32 [BALLAST,900] second test dump
L1: [...M32, M13'=stub]                               total 17,517   ← over 17,000
L2: —                    Swap: S1 S2 S3 S4 S5(=M13,90)
```

**B15 — ASSISTANT:** check: 17,517 → **RED → FOLD, before the LLM is called.** Every L1 item disposed by role:
```
CARRIED VERBATIM → L2:   M3 (charter, 6000)
                         M1 (40) · M14+M15 (gate-1 Q + approved, 905) · M16+M17 (SQL + approved, 1405)
                         M18+M19 (gate-3 Q + approved, 65) · M20–M27 (exec records, 296)
SUMMARIZED → L2:         M12, M31, and the stubs   →  310-token summary (text below)
MOVED L1→Swap:           M29→(dup of S3, dropped) · M30→swap:S6 · M32→swap:S7
                         ENTIRE pre-fold L1 transcript → swap:F1
L1:  ← restarts EMPTY

L2 (written once, now exists):
┌─────────────────────────────────────────────────────────────────┐
│ SUMMARY: "Onboarding test_nda_titanic_db, role data_science,    │
│  skill new-data-access steps 1–11. Gates 1–3 approved. Setup    │
│  SQL executed OK (role, view, grant). Tests: access ok, Name/   │
│  Cabin rejected, counts match. Sensitivity: Name=HIGH PII —     │
│  excluded Name,Cabin,Ticket. Remaining: Gate 4. Evicted data:   │
│  S1,S2,S3,S6,S7. Full pre-fold transcript: recall('F1')."       │
│ CHARTER verbatim: M3 (full skill body, byte-identical)          │
│ LEDGER verbatim:  M1 · M14+M15 · M16+M17 · M18+M19 · M20–M27    │
└──────────────────────────────────────────────────── ~9,020 tok ─┘
then compile → LLM asks Gate 4, ends turn:
adds:  M33 [NARRATIVE,120] "All tests green. ⛔ GATE 4 — approve as final acceptance?"
L1: [M33]                                             total 12,140  (3,000 + 9,020 + 120)
L2: [summary + charter + ledger]     Swap: S1 S2 S3 S6 S7 F1
```

**B16 — USER:** *"approved"* — and the compiled context from here on, every iteration:
```
system   = ROM + Kernel                                (3,000 — unchanged since B1)
messages = [L2 block, 9,020] + [L1: M33, M34]          ← L2 renders as the head, every iteration
adds:  M34 [LEDGER, 5]
L1: [M33 M34]                                         total 12,145
L2: unchanged            Swap: unchanged
```
At Gate 4 the model has: the full skill text, every "approved" with the exact question it answered, the exact SQL that ran — and none of the 5,700 tokens of dumps, which sit behind stubs.

**B17 — USER (later):** *"remind me — why exactly did we exclude Cabin?"* (adds M35 [LEDGER, 12]). The reasoning was in M12 — summarized away. **Reverse flow out of the fold:**

**B18 — ASSISTANT:** GREEN → follows the summary's pointer.
```
adds:  M36 [call,14] recall_context("F1", query="Cabin exclusion")
       M37 [BALLAST,260] excerpt of pre-fold M12: "Cabin VARCHAR(20) — deck locations are
            quasi-identifiers; with Pclass+Age they re-identify individuals → excluded"
       M38 [NARRATIVE] answers the user, quoting it.
L1: [M33 M34 M35 M36 M37 M38]                         total ~12,500
L2: unchanged            Swap: unchanged
```

**Every segment movement in this conversation:** L1→Swap at B10, B14 (valve; ballast only; stubs left) · Swap→L1 at B13, B18 (recall; always a new tail message) · L1→L2 once at B15 (carry verbatim + summarize once) · L2→context every iteration after B15 (rendered as the fixed head). L1 was never edited except stubs-in-place and the one fold restart; every other movement was an append.

---

## 8. Validation against Claude Code (extracted from the v2.1.199 binary)

| This design | Claude's implementation (verbatim evidence) |
|---|---|
| Valve: evict old ballast whole, recoverable | microcompaction: clears whitelisted old tool results, `"[Old tool result content cleared]"`, content persisted (`<persisted-output>` pointers) |
| Min-payoff rule | fires only if savings ≥ `20000` tokens |
| Role-not-age | only whitelisted bulk-data tools are clearable; user/assistant text immune |
| Fold: rare, near budget | autocompact at ~80% of effective window (buffer fraction `0.2`; effective = window − output reserve) |
| Fold carries / re-hydrates | Claude keeps **nothing** (`messagesToKeep: []`) and re-hydrates live state (open files via `readFileState`, todos, memory). This design additionally carries charter+ledger — a deliberate strengthening for gated workflows, affordable because folds are rare. |
| Breaker | verbatim in the binary: *"Autocompact is thrashing: the context refilled to the limit within 3 turns of the previous compact, 3 times in a row… use /clear"* |
| Pinned-near-ceiling steady state | observed behavior: context sits at ~100% for many turns while microcompaction trims ballast one-for-one against growth; no summarization occurring |

---

