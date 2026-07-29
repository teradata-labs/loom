# Loom Context & Skill Management — Mutation Design

**Type:** mutation design — a set of deltas against `main`, not a greenfield
spec. Every change names what main does today, the exact change, the code it
touches, and why. Everything not listed is **explicitly unchanged** (§2).
**Grounding:** all baseline claims verified against `main`
(`pkg/agent/segmented_memory.go`, `memory.go`, `pkg/skills/…`,
`pkg/shuttle/executor.go`, `pkg/patterns/…`).
**Companions (human reference, not required to implement this doc):**
`llm-optimised-context-creation-theory.md` (the laws the changes serve),
`loom-context-deficiency-end-to-end.md` (the diagnosis). This design is
self-contained: mutations (§1), the unchanged bound (§2), a worked trace
(§3), testable invariants (§4), and acceptance (§5).
**Vocabulary:** main's. Segments are ROM / Kernel / L1 / L2 / Swap.
Reduction of L1 into L2 is **compaction**. There is no new terminology.

---

## 0. Baseline — what main does today

`SegmentedMemory` holds five segments:

```
ROM      romContent — static per session
KERNEL   tools · toolResults · schemaCache · findingsCache
L1       l1Messages — recent conversation, verbatim
L2       l2Summary  — cumulative compressed summary of older L1
SWAP     sessionStore — durable; L2 overflow + large results by reference
```

**Compilation** (`GetMessagesForLLM`) assembles, as messages:

```
system  = ROM
        + L2 summary                     ("Previous conversation summary: …")
        + pattern    (patternContent)     ← per-turn injection
        + skills     (skillContent)        ← per-turn injection, FULL bodies
        + findings   (findingsCache)
messages = promotedContext + l1Messages
```
The provider adapter concatenates every `system`-role message into one
`system` field.

**Skills today.** Each turn the agent's skill block runs discovery
(`Discovery.Discover` → router / FTS), **force-activates** every candidate
via `Orchestrator.ActivateSkill` (evicting the lowest-confidence skill past
`MaxConcurrentSkills`), renders all active skill bodies with
`FormatActiveSkillsForLLM`, and calls `SegmentedMemory.InjectSkills` to
**overwrite** `skillContent`. So full skill bodies are re-injected into the
system slot on every turn.

**Patterns today.** `InjectPattern` → `patternContent`, rendered into the
system slot the same way. Patterns are large (templates + examples + syntax).

**Compaction today** (`AddMessage`, per message):
```
shouldCompress := l1Tokens > maxL1Tokens || budgetUsage > warningThreshold
if shouldCompress && len(l1Messages) > minL1Messages:
    n := batchSize(zone)                      // normal/warning/critical
    n  = adjustCompressionBoundary(n)         // never split a tool_use/result pair
    compressToL2(l1Messages[:n])              // compressor, else heuristic;
    l1Messages = l1Messages[n:]               //   l2Summary += summary (CUMULATIVE)
    if l2Summary > maxL2Tokens: evictL2ToSwap()
```
`minL1Messages` is a recency floor (compaction never drops below it).
`maxL1Tokens` is a **fixed** 4 000 / 6 400 / 9 600 by profile (a static
fallback; the dynamic sizer `NewSegmentedMemoryWithDynamicAllocation` exists
but is **not wired** into the live path).

**Large results** (`Executor.handleLargeResult`): a result over
`threshold` bytes is stored in `SharedMemoryStore` / `SQLResultStore` and
replaced inline by a preview + reference; retrieved via `query_tool_result`.
`threshold` defaults to `storage.DefaultSharedMemoryThreshold = -1` — **off**
(everything inlined).

---

## 1. The mutations

### M0 — New builtin `manage_skills` (list/load) — the skill pull verb

**Current.** Main has no way for the model to load a skill on demand. The
activation machinery exists — `Orchestrator.ActivateSkill`
(`pkg/skills/orchestrator.go:378`), `Library.Load`
(`pkg/skills/library.go:123`), `Library.ListAll`
(`pkg/skills/library.go:169`) — but it is driven only by the per-turn
`Discovery.Discover` push (`agent.go:2058+`), which activates candidates
automatically and injects their bodies via `InjectSkills`. M1/M2 delete that
push, so the model needs an explicit verb to act on.

**Change.** Add a builtin `manage_skills` tool with two actions, each a thin
model-facing surface over a function main already has:
- `load(name)` — wraps `Orchestrator.ActivateSkill`: load the body via
  `Library.Load`, activate it for this session, and return the body
  (`Skill.FormatForLLM()`) as `Result.Data` — a **raw string**, so
  `formatToolResult`'s `%v` prints it verbatim (M1) — with operational fields
  (skill / source_path / risk / activated_at, including the skill name for
  restore, § Skill load effect) in `Result.Metadata`, off-band from the
  model. On load it also wires the skill's required tools and re-takes the
  turn's advertised-tool snapshot so the same turn sees them (M8).
- `list()` — wraps `Library.ListAll`: the whole library annotated with which
  skills are active this session — the model's on-demand full view when the
  ROM summary (M2) isn't enough.
Registered unconditionally when the orchestrator is set: a candidate menu is
useless if the model has no verb to act on it.

**Touches.** new `manage_skills` builtin under `pkg/agent`, registered on the
builtin path; wires to `Orchestrator`, `Library`, and the per-session tool
registry (M8).

**Why.** M1/M2 turn skills from push to pull. The activation function already
exists (`ActivateSkill`); `load` is a new action that calls it and returns
the body, and `list` a new action over `ListAll`. The tool is new; the
mechanism under it is main's.

### M1 — Skills stop being a system-slot injection; a load is a message

**Current.** `InjectSkills`/`skillContent` re-render full skill bodies into
the `system` slot every turn; discovery force-activates.

**Change.** A skill is loaded by an explicit `manage_skills(load, name)`
tool call (M0). Its result is the skill body as **plain text (markdown)** in
`Result.Data` (a raw string), which enters L1 as an ordinary tool-result
message via the existing tool-result path (`formatToolResult` →
`session.AddMessage`, `agent.go:2863`–`2877`) — an event at a position, not
standing state. The load result keeps a one-line confirmation prefix so the
model can tell the load succeeded and, later, that the resident body means
the skill is loaded (the body alone is not self-confirming):

```
Skill loaded: new-data-access

<verbatim skill body>
```

Skill bodies flow through compaction like any message (M6 keeps L1 large;
old bodies compress into L2, recoverable from Swap).

**Delete.** `InjectSkills`, `skillContent`/`skillNames` fields and their
token cache, the skills block in `GetMessagesForLLM`, and the per-turn
force-activation in the agent's skill block.

**Touches.** `segmented_memory.go` (InjectSkills, skillContent,
GetMessagesForLLM), `agent.go` (per-turn skill block), `manage_skills` tool.

**Why.** The system slot must be byte-stable (theory doc); per-turn full-body
injection is the standing-state / cache-kill / positionless bug (deficiency
§Step 1–3). A load-as-message puts the instructions at the point of need,
append-only.

### M2 — Skill discovery: a static list in ROM + on-demand `list`

**Current.** Per-turn router/FTS discovery, force-activating candidates.

**Change.** Two surfaces, both outside the mutating context:
- **Static list in ROM.** At session creation, resolve the agent's bound
  skills (`Bindings`) and render `name — description` into `romContent`,
  once. Byte-stable for the session. This is the always-available menu the
  model reads to decide what to load.
- **Full library on demand.** `manage_skills(list)` (M0) returns the whole
  library (`Library.ListAll`), annotated with what's active, whenever the ROM
  summary isn't enough — a pull the model makes, not a per-turn push.

**Delete.** The per-turn discovery push.

**Touches.** ROM construction (session-prompt function); the agent's per-turn
discovery block. (`manage_skills(list)` is M0.)

**Why.** A dynamic per-turn menu is either cache-killing (head) or reads as
a directive (tail); a static list + pull tool is neither (theory doc,
deficiency §Step 3).

### M3 — Patterns become on-demand data a skill loads by reference

**Current.** `InjectPattern`/`patternContent` inject a pattern into the
system slot per turn.

**Change.** Patterns are not a channel. A skill's body references a pattern;
the model loads it **on demand**, and the pattern data returns as a tool
result — subject to the large-result threshold (M7) and compaction, like any
data. Patterns are consulted for a specific template, then age out.

**Delete.** `InjectPattern`, `patternContent` and its token cache, the
pattern block in `GetMessagesForLLM`.

**Touches.** `segmented_memory.go`, `GetMessagesForLLM`, the pattern
orchestration in the agent.

**Why.** Patterns are large reference data (templates/examples/syntax) the
model consults once — not a procedure it follows. Standing them in the
system slot is the same bloat relocated. On-demand keeps them out of context
until asked for.

### M4 — Retire the findings channel; Kernel narrows toward the tool block

**Current.** `findingsCache` + a findings block in `GetMessagesForLLM` +
the finding-extraction wiring.

**Change.** Remove the findings channel. Kernel keeps the **tool block**;
`schemaCache` and the recent-`toolResults` cache are not separate injected
channels — schema and results are just messages/data in the conversation
subject to the pipeline.

**Delete.** `findingsCache`, its block, the extractor.

**Touches.** `segmented_memory.go`, `GetMessagesForLLM`, finding extractor.

**Why.** Findings were a re-assertion channel compensating for L1 shredding;
with a large verbatim L1 (M6) they are redundant, and they were another
per-turn system-slot mutation.

### M5 — Compaction uses the LLM compressor by default, with a decision-preserving prompt

**Current.** `compressToL2` uses the LLM compressor when set, else the
heuristic `summarizeMessages` (which emits "Agent provided analysis" /
"Tool result received"). The prompt is not specified to preserve decisions.

**Change.** The **LLM compressor is the default** in the live path (the
heuristic remains only as a no-compressor fallback). Its prompt is written
to **preserve fidelity of decisions and approvals with their scope** (an
approval and the exact question it answered), open commitments, and any
reference/reload pointers; it compresses reasoning and reproducible data.

**Touches.** compressor wiring in `memory.go`/registry, the compressor
prompt.

**Why.** Oldest-into-L2 is fine; losing *what was approved* is not. The
mechanism is main's — the change is the compressor being on by default and
its prompt preserving the load-bearing facts (deficiency §Step 4–5).

### M6 — Compaction is budget-driven; L1 uses the whole window

**Current.** `shouldCompress := l1Tokens > maxL1Tokens || budgetUsage >
warningThreshold`. The fixed `maxL1Tokens` (4–9.6k) fires far too early on a
large window — the micro-window bug.

**Change.** Remove the fixed-cap trigger; compact on real budget pressure
only:
```
shouldCompress := budgetUsage > warningThreshold        // % of the real window
```
and drop `l1Tokens <= maxL1Tokens &&` from the post-compaction re-check.
`maxL1Tokens` survives only as a stat/log value.

**Effect.** L1 grows to fill the budget not held by ROM+Kernel+L2. Empty L2
(the common, early case) → L1 gets nearly the whole window; as L2
accumulates (bounded — it caps at `maxL2Tokens` and evicts to Swap) L1
yields exactly that much. Compaction fires only under genuine total
pressure. The `budgetUsage` trigger is already live; this removes the bug
riding beside it.

**Touches.** `segmented_memory.go:351`, `:538`, `:550`.

**Why.** L1 must be the real conversation, sized to the actual window, not a
fixed 6 400 (deficiency §Step 4). Budget-driven sharing between L1 and L2 is
the correct allocation — no fixed pre-split, no idle budget.

### M7 — The tool-result path: bounded offload, one threshold, clean rendering

**Current.** Two facts, both wrong for this design:
- **Two offload sites, and they disagree.** A result is offloaded by *either*
  `Executor.handleLargeResult` (`executor.go:284`, byte threshold
  `e.threshold`) *or* `agent.formatToolResult` (`agent.go:3727`, byte
  threshold `a.sharedMemoryThreshold`, **plus** a separate 1000-token fallback
  at `:3739`). Both default to `storage.DefaultSharedMemoryThreshold = -1`
  (inline everything). The `:3739` path name-exempts
  `get_tool_result`/`query_tool_result` but **nothing else**.
- **Rendering via `%v`.** Success data is rendered `fmt.Sprintf("%v",
  result.Data)` (`agent.go:3704`). A string passes through cleanly; a
  composite (`map[string]interface{}`) renders as Go-map soup — the confusion
  that made a metadata-returning skill load unreadable.

**Change.**
- **One threshold, positive and bounded — 64 KiB — at both sites.** Set the
  default at `handleLargeResult` *and* `formatToolResult` to the same 64 KiB.
  A result at/above it is stored by reference (preview + handle inline),
  retrieved via `query_tool_result`; below it stays inline and directly
  readable. Never −1; operators may lower it.
- **One exempt set, applied at both sites.** Skill body (the `manage_skills`
  load result, M0/M1) and the recall tools
  (`query_tool_result`/`get_tool_result`) always enter whole — add the
  skill-load tool to `formatToolResult:3739`'s name exemptions and skip
  `handleLargeResult` for it in `Execute` (the tool name is known there;
  `*Result` alone does not carry it). Recall-tool exemption prevents an
  offload→recall→offload loop; skill-body exemption keeps M1's body inline.
- **Clean rendering.** Render string `Data` as-is; `json.Marshal` a composite
  `Data`; never `fmt %v` a map (backs invariant 13).

**Touches.** `storage.DefaultSharedMemoryThreshold` and the two threshold
wirings (`executor.go`, `a.sharedMemoryThreshold`); the exempt checks in
`Execute` and `formatToolResult:3739`; the render branch at
`formatToolResult:3704`.

**Why.** −1 lets a 25k-token `SELECT *` bloat the conversation for the rest
of the session; the default should be **generous, not aggressive** — up to
~64 KiB (~16k tokens) stays inline, only genuine monsters get stored aside.
Composes with M6: once L1 is sized to the real window, medium results need
not be evicted; L1 has room. Two disagreeing thresholds would offload a body
one site kept inline (breaking M1); `%v` on a map is unreadable to the model.
One threshold, one exempt set, clean rendering — the tool-result path is then
predictable end to end.

### M8 — The advertised tool set is per-session and event-driven (two BUG fixes)

**Current.** One `a.tools` registry per agent instance, shared by every
session. Session-triggered registrations (a skill load's required tools;
progressive disclosure of `get_error_details`/`query_tool_result`) mutate
the set every session advertises. And the error / result / shared-memory
stores are keyed such that a disclosure tool can address another session's
data.

**Change.**
- **Per-session advertised tools.** The global registry holds what *can*
  exist; each session advertises what *its own events* have registered. A
  skill load registers its required tools **for that session** at the load
  event; nothing another session does changes this session's tool block.
  The advertised list stays name-sorted (already enforced in `ListTools`).
- **Per-session/user data stores.** The error, SQL-result, and
  shared-memory stores are partitioned by session (and user); every lookup
  is scoped to the caller's partition. A session cannot address another's
  data, independent of what tools are advertised.

**Touches.** the tool-registry projection per session, the disclosure-tool
registration path, the three store lookups.

**Why.** BUGs in main: cross-user capability widening and possible
cross-user data reads. The tool block also leads the cache prefix, so a
foreign registration invalidates a session's cache with no event of its own.


### M9 — Context-dump and debug logging (the design's own instrument)

**Current.** No path emits the exact compiled context. `LOOM_DEBUG_BEDROCK`
prints only loop config (`MaxTurns`, `MaxToolExecutions`); the memory
mutations that produce and reshape the context — compaction, skill load,
large-result storage, per-session tool assembly — emit nothing that
reconstructs their behaviour. The invariants in §4 are therefore
unobservable from outside the process, and the eval in §5 has no source.

**Change.** Add two instruments, both off by default behind one debug switch,
both no-ops on the production path when off:
- **Context dump at the choke point.** `chatWithRetry`
  (`pkg/agent/llm_retry.go`) is the single function every real per-turn
  provider call passes through — streaming and non-streaming alike. Gated by
  the switch, it serializes the exact `(messages, tools)` it is about to
  dispatch — one record per turn, tagged with session id and turn number — to
  a dump sink. This is the compiled context and advertised tool block
  byte-for-byte; nothing is reconstructed.
- **Debug logging at each mutation point.** `zap` debug logs at the events
  that create or change the context, each carrying session id and turn:
  compaction (batch size, boundary adjustment, L1/L2 tokens before→after,
  whether L2 evicted to Swap), skill load (name, body size, tools registered,
  active-set delta), large-result storage (reference id, size vs. threshold),
  per-session tool-set assembly (advertised names). Restore-replay logs the
  same load/register events it re-fires.

**Touches.** `chatWithRetry`; the compaction path (`AddMessage`/
`compressToL2`); the `manage_skills(load)` effect; `handleLargeResult`; the
per-session tool projection (M8).

**Why.** A segment-memory design can only be judged from the consumer's seat,
and the consumer sees the compiled context, not the struct — the dump is the
only place that truth is observable. The debug logs explain *how* each turn's
context came to be, so a failing invariant is traceable to the event that
broke it. §5 reads from both.

**Safety.** The compiled context can carry user data, schema, and query
results, so both instruments stay off by default and, when on, write to a
local per-run sink — never to shared or Hawk logs. The dump is deliberately
un-redacted (the eval judges what the model actually saw); its safeguard is
the gate and the local sink, not filtering.


---

### Skill load effect — wiring, and the single source of "loaded"

Two facts must hold when a skill loads: its body is in the conversation, and
its required tools are advertised to the model. Both are **effects of the
load event**:

- **Live load.** `manage_skills(load)` appends the body to L1 (M1), records
  the skill on the orchestrator's active set (`Orchestrator.ActivateSkill`),
  and registers its required tools into this session's advertised set (M8).
  One event, three effects — no lag.
- **The single source.** "Which skills are loaded this session" is the
  orchestrator's active set — one record, maintained by load events. The
  conversation carries the *bodies* (for the model to read); the orchestrator
  set serves *wiring* and `manage_skills(list)`. Nothing re-derives one from
  the other per turn — **there is no walk, no catalog-filter** — so nothing
  can diverge. Compaction moving an old body into L2 does not
  un-load the skill: its tools stay advertised and the model can reload for
  the verbatim steps if it needs them.
- **Restore.** Process memory (the orchestrator set, the advertised tools) is
  lost on restart, but the durable conversation replays.
  `SegmentedMemory.ReplayMessages` (`segmented_memory.go:520`, called from
  `memory.go:226`) restores L1/L2/Swap but does **not** re-fire load effects —
  that is a separate pass this design adds. After `ReplayMessages` returns
  (so **`Memory.mu` is released**), the restore path walks the restored
  messages, finds each load by its **structured skill-name marker** (set on
  the load result by M0 — not by parsing the "Skill loaded:" prose), and
  re-fires `Orchestrator.ActivateSkill` + per-session tool registration for
  each. Re-activate and re-register only — no task emission, no new message.
  T18 then sees a session identical to a live one.
- **No unload, no cap.** A load is a message; it is retired by compaction,
  never by deletion (deleting would orphan its tool pair). There
  is no skill-specific cap: a loaded skill is a message, bounded
  by the same budget/compaction as everything else. `MaxConcurrentSkills`
  survives only as the search-candidate bound, never an active-set cap.
- **High-risk.** A high-risk `load` returns an explicit approval-required
  result, never a silent skip; a nil permission checker disables the gate


---

## 2. Explicitly unchanged (the blast-radius bound)

Everything here stays as main has it. The mutations must not reinvent it.

- **The five segments** — ROM · Kernel · L1 · L2 · Swap — names and roles.
- **Compaction mechanism** — `AddMessage` → compress the oldest batch →
  **cumulative** `l2Summary` → L2 evicts to Swap at `maxL2Tokens`. Batch
  sizes by zone. Only the *trigger* changes (M6); the mechanism does not.
- **`minL1Messages`** — the recency floor. Kept.
- **`adjustCompressionBoundary`** — keeps a `tool_use`/`tool_result` pair
  together across a compaction cut. Kept; it already prevents the orphaned-
  pair provider-400, so no separate fix is needed.
- **`GetMessagesForLLM` shape** — ROM(system) + L2 + L1 — minus only the
  deleted injection blocks (M1, M3, M4). L2 stays a system-role message;
  the provider joins system messages, so ROM+L2 is fine.
- **Skill `Library`** (disk/embedded on OSS; DB-registered in cloud via
  `execLib.Register`), **`Orchestrator`** (active set, `ActivateSkill`),
  **`Discovery`/`Router`** (repurposed as the `search` backend), **Swap**,
  `handleLargeResult`, `SharedMemoryStore`, `query_tool_result`/
  `get_tool_result`.
- **No breaker, no valve, no roles** — main has none; this design adds none.
  Pressure is relieved by compaction (L1→L2) and large-result storage,
  exactly as main does.

---

## 3. End-state trace — Doug across ~20 turns on the mutated system

Window 200k · reserve 20k · warning 60% (~108k) · `minL1Messages` floor ·
large-result threshold 64 KiB (M7). ROM carries the static skill list from
turn 0.

- **T1** Doug: "grant data scientists read-only on `test_nda_titanic_db` —
  role `data_science`, SELECT on `Passenger_Data`." Model reads ROM's list,
  calls `manage_skills(load, new-data-access)`. Body enters L1 as a
  confirmed message; orchestrator records it; the skill's required tools
  register **for this session**. Context: `tools | ROM(static list) | [L1]`.
- **T2–T6** Model works the skill's steps: profiling reads. Any dump at/above
  64 KiB is stored by reference (M7) — L1 gets a preview + handle, not raw
  rows; smaller reads stay inline and directly readable. Sensitivity + readiness report (messages). Gate 1 → Doug "approved"
  (a user message, in L1 verbatim). L1 grows; total context still < 60%; no
  compaction.
- **T7–T10** SQL generation, gates 2–3, execution (mutating-tool results).
  L1 keeps growing — it can, because M6 lets L1 use the window. Still under
  warning; nothing compacts. The skill body from T1 is still verbatim in L1.
- **T11** Total context crosses 60% → **compaction** fires. The oldest batch
  (the T2–T4 profiling reads, now old) compresses into L2; `l2Summary` is
  written, preserving Gate-1's approval-with-scope (M5). `adjustCompression
  Boundary` keeps tool pairs whole. `minL1Messages` keeps the recent turns
  verbatim. Context now: `tools | ROM | [L2 summary] [L1 recent]`. The skill
  body: if still recent, verbatim; if old, in the L2 summary — the model
  reads the summary and, needing exact steps, calls `load` again (a fresh
  body appends). No walk, no forced reload — the model's own read decides.
- **T12–T16** More work; more compaction as needed, each time the oldest
  batch → L2 (cumulative). L2 approaches `maxL2Tokens` → evicts oldest
  summary to Swap. Approvals remain in L2's preserved fidelity; the full
  pre-compaction history is durable in Swap, reachable by `query_tool_result`
  / the durable session store.
- **T17** Doug: "also grant SELECT on `Crew_Roster`." The workflow returns to
  the skill's DDL steps. If the body is in L1 → the model just follows it. If
  it compacted to L2 → the model reads the summary, reloads for the verbatim
  gates, and proceeds. Its required tools are still advertised (they never
  de-registered), so it can act immediately.
- **Restart between T17 and T18** New process. The session replays from the
  durable store: L1, L2, Swap refs restored. The replay re-fires each load
  event — orchestrator re-activated, required tools re-registered (after
  `Memory.mu` releases). T18 sees a session identical to a live one:
  instructions present (or summarized-with-reload-pointer), tools wired,
  `list` truthful.
- **T18–T20** Continues. `data-quality-audit` also loaded — a second body in
  L1. Two skills' worth of bodies is just more messages; compaction absorbs
  the older one into L2 under pressure, no thrash and no breaker, because
  nothing pins a body and nothing forces a reload. The session runs to
  completion.

**Cross-cutting invariants held the whole run:** the `tools` block and ROM
were byte-stable except at this session's own load events; the `messages`
prefix was append-only between compactions; every compaction was the oldest
batch → cumulative L2 with a durable Swap backstop; no cross-user tool or
data leakage; approvals kept their scope. The system runs; it does not break
down at the second compaction or after restart.

---

## 4. Invariants (the testable spec)

Every one must hold after the mutations; each is checkable against the
compiled context or the segment state.

**Head stability**
1. The `tools` block is byte-identical between two consecutive LLM calls
   unless *this session's own* load (or its own first-need disclosure)
   occurred between them. It is per-session — no other session's event
   changes it.
2. ROM (including the static skill list) is byte-identical for the whole
   session.
3. Exactly one thing is injected per turn into the system slot: ROM. No
   per-turn skill / pattern / findings injection exists.

**Append-only conversation**
4. Between two compactions, the compiled `messages` are a strict
   prefix-extension of the previous turn's — prior messages unchanged, only
   the new turn appended.
5. The only operation that rewrites already-compiled `messages` is a
   compaction (oldest batch → L2). Nothing else edits the interior.

**Compaction**
6. Compaction fires only when `budgetUsage` crosses the warning zone (of the
   real window) — never on a fixed L1 cap.
7. Compaction compresses the **oldest** batch into a **cumulative** `l2Summary`,
   keeps ≥ `minL1Messages` recent verbatim, and never splits a
   `tool_use`/`tool_result` pair (`adjustCompressionBoundary`).
8. The compressor preserves decisions and approvals **with their scope**; an
   approval never loses its referent through a compaction. The full pre-
   compaction history is recoverable from Swap.

**Skills**
9. A skill enters the conversation only as an explicit `manage_skills(load)`
   result — a plain-text body with a load-confirmation line — appended to L1.
   There is no per-turn skill surface anywhere.
10. "Which skills are loaded" has **one** source: the orchestrator active
    set, maintained by the load event. `list` and tool-wiring derive from it;
    nothing re-derives it from the conversation per turn.
11. A skill load registers its required tools **at the load event** (and re-
    fires on restore replay). There is no unload and no skill-specific cap.
12. A high-risk load returns an explicit approval-required result, never a
    silent skip.

**Isolation**
13. A tool result is JSON when its data is a composite, plain text when it is
    a string (skill body) — never `fmt %v` of a Go map.
14. One session's load never changes another session's advertised tools; one
    session can never address another session's stored errors/results/blobs.

**Large results**
15. A tool result at/above the size threshold is stored by reference (preview
    + handle); the threshold default is positive (64 KiB), never −1; the
    exempt set (skill body, `query_tool_result`/`get_tool_result`) always
    enters whole.

---

## 5. Acceptance

Both the structural pass and the consumer eval read from **the M9 context-dump
and debug logs** — the same instrument the design ships. The dump is the exact
`(messages, tools)` handed to the provider each turn, captured at
`chatWithRetry`; nothing is reconstructed, so the eval judges exactly what the
model saw. Acceptance is not a separate test harness that reconstructs context;
it consumes M9.

The change is done when both hold:

**Structural tests** — drive the real `Agent.Chat` loop with a scripted LLM
over a long multi-turn session (Doug's, §3, and a data-heavy variant), read
the per-turn dumps, and assert the invariants (§4) directly on them: one
system message that is byte-stable except at this session's own load events;
append-only `messages` between compactions; skill bodies present as L1 events,
not system injections; large results stored by reference above threshold;
approvals surviving a compaction with their scope; tool block unchanged by
another session's activity.

**LLM-consumer eval** — feed each dumped turn to a model positioned as the
consumer ("this is the context you were handed for this turn — is it the one
you'd want to reason on?"). A clean verdict — no contradiction between what the
transcript says and what is resident, no stale skill claims, no lost approval
scope, readable tool results — is required alongside the green structural pass.

**Test-sample pattern** (for the test lead): a single end-to-end test =
*enable the dump → drive all turns → assert the invariants on each dumped turn
→ run the consumer eval over the dumps.* Build the eval runner fresh (read the
dump records, call the consumer model, report concerns); do not carry the v5
tooling names.
