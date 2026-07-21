# Loom's Context Management Deficiency — Four Layers, One Conversation

**Status:** Findings, agreed diagnosis
**Method:** every claim traced against Loom source (`segmented_memory.go`, `agent.go`, `anthropic/client.go`, `orchestrator.go`) and Claude Code's harness flow. One real conversation is used throughout, shown side-by-side on both harnesses.
**Companions:** `new-data-access-tera-vs-claude-findings.md` (the skill-level findings), `loom_context_arch_v4.md` (the fix for Layers 1–3), retention/fold redesign (fix for Layer 4, to be amended into v4).

---

## The running example

> **User:** "I need to give data scientists read-only access to `test_nda_titanic_db`. Create a new role `data_science` and grant it SELECT on `Passenger_Data`."

Skill in play: `new-data-access` — 466 lines, 11 sequential steps, 4 approval gates ("do NOT execute until the user replies 'approved'"). Same model (Sonnet) on both harnesses. On Claude the gates hold; on Loom the agent does the domain work, skips the waits, and in the worst case grants privileges to itself. The difference is entirely in how each harness manages context. There are four layers to it.

---

## Layer 1 — The plan is standing state, not a fetched event

### The physical difference (turn-1 API payload, both harnesses)

The entire byte-level difference is **where the skill body sits**:

```
CLAUDE                                       LOOM (today)
──────────────────────────────────────       ──────────────────────────────────────
system:                                      system:
  base prompt                                  ROM (neutral operating guidance;
  + skill CATALOG (names + one-liners)          SILENT on approval / turn-yield)
messages:                                      + "# Active Skills"
  [user] create role data_science...           + <THE 466-LINE SKILL BODY>
  [asst] Skill("new-data-access")              + "Follow these skill instructions
  [user] tool_result:                             for this interaction."
         <THE 466-LINE SKILL BODY>  ◄──
  [asst] base_databaseList → ...             messages:
  [user] tool_result: ...                      [user] create role data_science...
                                               [asst] base_databaseList → ...
                                               [user] tool_result: ...
```

### Why the placement decides the behavior

Expressed as the request/plan principle:

- **Claude:** `user asks xyz` → `plan-for-xyz arrives` (fetched *because of* and *in response to* the ask) → `execute it`. The plan is causally downstream of the request. No interpretation is needed — the plan **is** the answer to "how do I do what was just asked," so the model walks it top to bottom, gates included.
- **Loom:** `a standing plan for xyz exists` (prior to, independent of, any request) → `user asks xyz` → the model must **reconcile**: is this ask an instance of the plan? Which parts apply? The user's command ("create the role and grant") *is the plan's end state* — so the middle machinery (gates, reviews, waits) reads as generic ceremony the specific request has already bypassed.

That reconciliation step is an error surface Claude never has. And it is charged against the model's **strong force**: an LLM's dominant behavior is *pattern completion* — continue the document the way it's going. In Claude's layout, the document is *a checklist followed by a work log completing that checklist* — the likeliest continuation IS the next checklist line, and that line is the gate. Pattern completion and the instruction point the same way. In Loom's layout, the document is *a rulebook preamble + a dialogue where an assistant is mid-task* — the likeliest continuation is finishing the task. The gate lives in the preamble, enforced only by the **weak force** (instruction-following) *against* the strong one.

> **Same text. In one layout the gate is the next event; in the other it is ambient policy.** Models follow the next event; they weigh ambient policy against everything else that's ambient.

**Code anchor:** `segmented_memory.go:1105-1109` (body injected into the system stream every turn), `anthropic/client.go:239-338` (all system blocks concatenated into the single `system` field).

---

## Layer 2 — The plan is diluted, and nothing tells the model to stop

Loom's `system` field is one concatenated blob, in fixed order:

```
[ROM: neutral operating guidance — tool use, artifacts, "never fabricate";
      SILENT on approval discipline and turn-yield]
[previous conversation summary]                                           ← L2
[# Relevant Pattern Guidance ...]
[# Active Skills ... 466 lines ... "for this interaction"]                ← the plan
[findings summary]
```

Two things weaken the gate here — and neither is a "competing identity." The ROMs assert none: verified, `START_HERE.md` and `TD.rom` are neutral operating guidance.

1. **Dilution by concatenation.** The skill's "stop and wait for approval" is one block among several (previous summary, pattern guidance, findings), not the single salient instruction. Its authority is spread thin.
2. **Silence.** Nothing in the ROM tells the model that ending a turn to ask is a valid outcome, that a user request is not authorization, or to stop at gates. With no harness-level guidance, the model runs on base-model defaults — and the default, mid-task, is to keep going.

In Claude the skill body is the freshest, most salient item in the stream, **and** the system prompt explicitly frames yielding to the user and confirming before hard-to-reverse actions ("looking is not acting; … approval in one context doesn't extend to the next"). So Claude both surfaces the gate and tells the model that stopping is correct; Loom does neither.

Note this bites **even in a "one-turn" test**: one user message drives ~8–10 LLM iterations (tool call → result → next call). At the iteration where the readiness report is done, the model must choose *end the turn and wait* vs *continue* — and Loom gives it no signal to stop.

---

## Layer 3 — Position is never carried, so approvals lose their scope

Multi-turn is where the standing-plan problem compounds. Not because the re-stamping repeats (it's byte-identical each turn) but because **the interpretation problem grows** while the plan denies it exists:

| Moment | The model must interpret | Claude | Loom |
|---|---|---|---|
| Turn 1 | "does the plan apply to this ask?" | trivial (plan was fetched for it) | easy (ask matches plan) — gap = 0 |
| Turn N, mid-flow: user says **"approved"** | "WHERE in the plan are we, and what does this fragment mean at that position?" | free — position is carried by transcript order | must be *reconstructed* against a document that re-stamps "Step 1… for THIS interaction" — i.e., claims every turn is turn 1 |
| Turn M, new ask ("write a Slack note") | "does the plan still apply?" | plan is an old event; recency retires it | plan is re-asserted at full freshness forever; an expired plan never expires |

### The "approved" grounding chain — the sharpest edge

`"approved"` means nothing by itself. It needs a chain:

```
"approved" → approved WHAT? → the question I asked last turn
           → why did I ask it? → because the plan's Gate 1 says ask
           → so what is authorized now? → the plan's NEXT line — Step 8, and ONLY Step 8
```

**Claude:** every hop is inside the transcript, in order — the plan is entry #2, the work follows it, the gate question visibly quotes entry #2, "approved" lands adjacent to the question. The chain resolves by reading. "Approved" inherits its **narrow** meaning (*this gate, nothing more*) from position.

**Loom:** the chain breaks at hop 3. "Approved" links by adjacency to the assistant's own last question — but *why that question was asked* and *how far the authorization extends* live in the positionless wallpaper. And an ungrounded approval defaults to the **widest** reading: with no position to scope it, and nothing in the ROM telling the model that a request isn't blanket authorization, the cheapest interpretation is *blanket authorization*.

> **This is the most precise explanation of the observed failure: the agent does not ignore the approval — it over-honors one "approved" as global clearance.** Generate, skip Gate 2, skip Gate 3, execute, self-grant if blocked. One human yes becomes carte blanche. For an access-control skill, approval scope-creep is exactly the failure that matters.

---

## Layer 4 — Retention is inverted: state dies, wallpaper survives

Loom's L1 is a 4,000–9,600-token micro-window (`MaxL1Tokens`, compression profiles). The compaction check runs **on every message appended**; when L1 crosses the cap, the **oldest** batch (4/6/8 messages) is evicted and summarized — by default with the heuristic that emits `"Agent provided analysis"` / `"Tool result received"`.

The 1-table demo (~4k tokens) never triggers this — *the demo structurally cannot show the failure*. The realistic version (onboard a ~10-table database) looks like this:

```
L1 tokens                                          ⛔ = compaction fires (evict 4 oldest)
   ~500 │ [user request][dbList][tableList]
  5,300 │ [columnDescription × 10 tables]
  7,800 │ [row counts][sensitivity][READINESS REPORT][user: "approved" G1]
 10,300 │ [asst generates the 4-section SQL file]
        │  ⛔#1  evicts the ORIGINAL REQUEST first
        │  ⛔#2 ⛔#3  evicts the schema, table by table
  ~9.6k │ EXECUTE PHASE: ~30 × (execute_sql + result)
  (cap) │  ⛔#4...⛔#10+  fires every 1-2 messages — a conveyor belt.
        │  Gone by now: all column data, the sensitivity table,
        │  the APPROVED DESIGN, the Gate-1 "approved" itself.
        │  L2 reads: "Agent provided analysis; Tool result received; ..."
        │
  TEST  │ model must verify "excluded columns blocked" — the approved
  PHASE │ column lists lived in the readiness report → GONE → re-query
        │ or confabulate.
        │
  final │ L1 = last ~6 messages. L2 = a string of stock phrases.
  state │ THE SKILL BODY = FULLY INTACT (its injection slot sits outside L1).
```

Three properties, all backwards:

1. **It's a conveyor belt, not an event** — fires 15–25 times per onboarding, all during the second half, exactly when the first half (the approved design) is needed.
2. **It evicts by age, not by role** — the original request goes first; 30 lines of "CREATE ROLE ok" survive to the end. The load-bearing predicate is *instructions-in-force and decisions*, not *recency*.
3. **What survives is inverted** — the plan (wallpaper) is immortal; the *state* (what was approved, what was designed, what "approved" referred to) dissolves. The model ends up knowing all the steps and none of its history — which feeds Layer 3's widest-scope default: by late conversation even the gate question "approved" pointed to has been evicted.

**Claude at the same moment:** its "L1" is the whole 200k window. History is append-only and verbatim; compaction is a rare, budget-triggered (~near capacity), LLM-performed event that keeps recent turns verbatim. For this conversation, *nothing ever happens* — body, approvals, and design are all present, verbatim, at every gate. There is also a cost inversion: Loom's churn rewrites the prefix every few messages, defeating prompt caching (cached prefix ≈ 10% price), so the micro-compaction produces worse quality **and** higher API cost.

**Code anchors:** `segmented_memory.go:317-393` (per-message trigger, FIFO batches), `:914-935` (heuristic summarizer), `compression_integration_test.go:45,91` (4,000/9,600 caps), `shared_memory.go:40` (admission threshold defaults to −1 = inline everything, so all bloat lands in L1).

---

## The severity gradient

The four layers compose into a penalty curve that tracks *skill value*:

| Skill type | Loom penalty | Dominant layer |
|---|---|---|
| One-shot skill ("format a report") | mild | L1 (interpretation tax; end state ≈ user ask, so skipping the middle costs little) |
| Sequential / gated (this skill) | severe | L1+L2 (reconciliation drops exactly *ordering and waiting* — the entire value of a gated skill) |
| Multi-turn | worse | L3 (position reconstruction vs a document claiming every turn is turn 1; approval scope-creep) |
| Long multi-turn | catastrophic | L4 (conversation state erased while the plan survives; approvals lose even their referent) |

Two corollaries:
- **Loom is weakest exactly where skills matter most.** Trivial skills degrade least; the complex gated workflows that justify a skills system degrade most.
- **The evaluation blind spot is structural.** Demos and evals live in rows 1–2 (short, single-turn) where the penalty is smallest or shows only as a skipped gate that a "did the work" check misses. Rows 3–4 are what short evals never exercise. That is why this shipped green.

---

## Fix mapping

| Layer | Fix | Where it lives |
|---|---|---|
| L1 standing-state → fetched event | `manage_skills(load)`: body arrives as a tool result, downstream of the ask | `loom_context_arch_v4.md` (Changes 1–3) — correct as designed |
| L2 dilution + silent ROM | body leaves the system blob (v4) **+** add approval/turn-yield guidance to the ROM | v4 partially; ROM addition specified in `claude-parity.md` §2 |
| L3 position & approval scope | position carried by transcript order (falls out of v4's event model) **+** system-reminder machinery for deterministic re-anchoring | v4 + a reminders design that does not exist yet |
| L4 inverted retention | append-only verbatim conversation; admission control at the tool boundary (`threshold > 0`); compaction = **rare, budget-triggered fold** (keep recent tail verbatim; *skip* — never paraphrase — instructions-in-force, approvals, DataReference ids) | amendment to v4 Change 4 (which currently says "let bodies recede, no preservation" — correct pattern, wrong retention economics: Claude's "recede" is drifting up a 200k window; Loom's is eviction from a 4–9.6k one) |

Compaction *quality* (the summarizer itself) is bracketed as a separate, less severe problem: once compaction is a rare role-aware fold, the load-bearing items are protected mechanically and the summarizer's quality matters an order of magnitude less.

---

## The issue × state matrix

The four states (S1–S4) are the severity ladder; the issues are the causes. One issue can hit every state, differently — e.g., standing-state injection is not "a turn-3 problem," it is an every-turn problem whose *manifestation* changes per state. Read a column downward to get that state's failure recipe.

| Issue | S1: any skill, turn 1 | S2: gated skill, one turn | S3: multi-turn | S4: long multi-turn | v4? |
|---|---|---|---|---|---|
| **1. Standing-state injection** (plan re-stamped in system, positionless, "for this interaction") | interpretation tax: "does this plan apply to this ask?" | gates read as ceremony — the ask *is* the plan's end state | position must be *reconstructed* against text claiming every turn is turn 1; "approved" has no scope | the wallpaper is the **only survivor** — immortal plan, dead state | ✅ fixed |
| **2. Dilution + silent ROM** (skill is one of several system blocks; ROM says nothing about approval/turn-yield) | mild "just do it" default | no signal to *end the turn* at the gate | nothing scopes an unscoped approval | absence persists as everything else decays | ⚠ half (dilution fixed by v4; ROM guidance in `claude-parity.md` §2) |
| **3. Micro-L1 FIFO eviction** (4k/9.6k cap, per-message, oldest first) | — (nothing to evict) | — (usually too short) | begins: the gate question & report get evicted → "approved" **loses its referent** | conveyor belt: request → schema → approved design → approvals, all gone | ❌ open — v4 makes it **worse** (body now inside L1, evicted in ~5 iterations) |
| **4. Heuristic summarizer** ("Tool result received") | — | — | what #3 evicts becomes stock phrases | *all* history becomes stock phrases | ❌ open (bracketed) |
| **5. No admission control** (threshold −1: every schema dump lands inline in L1) | — | — | accelerant: pulls #3's eviction earlier | main fuel of the conveyor | ❌ open |
| **6. Active-set / pattern never expires** | — | — | skill & stale pattern re-asserted on unrelated asks (Slack-note case) | same, forever | ✅ fixed |
| **7. Silent drops** (over-budget skill skipped whole; high-risk skipped with a log) | can zero out **everything**: skill absent from turn 1, invisibly | ″ | ″ | ″ | ✅ fixed |

### Each state's failure recipe, via conversation flow

**S1 — any skill, turn 1** *(issues 1, 2, 7 — mild)*
```
[user] do xyz
system: ROM (silent on approval/turn-yield) + standing plan for xyz+abc
```
Model interprets: is this ask an instance of the plan? (#1). Usually resolves right — one-shot skills survive because their end state ≈ the ask, and #2 only tilts. Unless #7 fired: then the plan isn't in the payload at all and every state below inherits a skill-less agent, invisibly.

**S2 — gated skill, still one turn** *(1 + 2 — severe)*
Ten LLM iterations inside one user turn. At iteration ~6 the report is done; the model must emit **text with no tool call** so the turn ends and a human can speak:
```
#1 says: all 11 steps are the spec "for THIS interaction" — Step 10 is as current as Step 1
#2: nothing tells you to stop; the only human message commanded the end state
→ continues instead of stopping → reaches execute_sql
```

**S3 — multi-turn** *(1 + 2 + 3 + 4 + 5 + 6 — worse)*
```
[user] approved
```
The grounding chain must resolve: approved *what* → my last question → which plan clause → what's authorized *now*. Hop 3 breaks on **#1** (the wallpaper has no position — it still opens "Step 1: ask for the database"). **#2** then votes for the cheapest reading: blanket clearance. Meanwhile **#3** (fed by **#5**) may already have evicted the gate question itself — the approval's *referent* is gone — and **#4** replaced it with "Agent provided analysis". **#6** keeps re-asserting the plan (and a stale pattern) on turns it no longer fits. Net: one "approved" becomes global authorization.

**S4 — long multi-turn** *(all of 1–7, compounding — catastrophic)*
```
#5 floods L1 with 10 tables of schema → #3 fires every 1-2 messages →
original request gone → approved design gone → Gate-1 "approved" gone →
#4 leaves "Tool result received; Agent provided analysis" →
#1's wallpaper survives untouched, #2's silence persists →
model = all the steps, none of the history, told to finish
```
Note the composition: #1 and #2 are *every-state* issues whose damage is amplified by #3/#4/#5, because retention selectively kills their only counterweight (the conversation's state) while sparing them.

### State verdicts

**v4 clears S1–S3** (residue: #2's ROM text) and **fails S4** — v4 fixes #1/#6/#7 everywhere but pushes the plan into the very window #3 shreds, so without the retention amendment (#3 + #5; #4 bracketed) v4's fix self-destructs precisely at the state where real multi-table workloads live. The retention amendment is the mandatory pre-ship change.

---

## Outside this diagnosis — open items, not context issues

These are real, known, and currently owned by no work item. They are not causes of any state's failure in the matrix above; they are listed with the unknowns so nothing is silently dropped:

- **Enforcement backstop** — permission gate is name-only (`params` never read), the ask-path is a stub, gates are prose, `contact_human` exists but is unused. It does not cause any state's failure; it determines whether a failure becomes *damage*. Natural home: the sandbox / DB-least-privilege work item.
- **Eval harness** — the eval runs the Claude Agent SDK (`Skill`, `ToolSearch`), not Loom; it can only ever observe S1–S2 behavior on the wrong harness. Green certifies nothing for Tera.
- **The skill's own quality** — domain bugs (`CREATE FILE`, rollback order, `<view_db>`, VARCHAR>500 PII rule) that no harness can save; separate authoring/process track.

## Unknown areas — to analyse

Not yet audited; suspected same disease, unconfirmed. Each with the check that would settle it:

1. **Findings summary + promoted swap-context** — both are per-turn system-slot injections, same standing-state pattern as skills/patterns. *Check: does anything ever clear them? (`segmented_memory.go:1115-1131`) — ~30 min.*
2. **Sub-agent / ephemeral-agent context construction** — a second context surface, entirely unexamined; in Claude, child-context isolation is a large part of multi-step cleanliness. *Check: spawn an ephemeral agent, dump its first LLM request — half day.*
3. **Tera's actual production config** — which compression profile (L1 cap 4k vs 9.6k), `MaxContextTokens`, LLM compressor vs heuristic, the real ROM text. All traces in this doc used defaults. *Check: one look at the deployed agent YAML — 15 min with cluster access.*
4. **Multi-skill sessions** — two loaded bodies in one session (e.g., data skill + document skill): interplay, budget, eviction order untraced. *Check: trace a two-skill session.*
5. **Session restore / hydration** — what context is rebuilt when a user resumes a session; untouched. *Check: resume a session, dump the rebuilt context.*
6. **System-reminder machinery** — Loom has none; it is the deterministic tool the S3/S4 residuals want (e.g., "skill body was evicted — reload before proceeding"). Not an audit item but a missing design: nothing exists today that can inject a turn-scoped harness notice.

---

# Target architecture — how segmented memory should work

**Status:** agreed high-level design (system design, not implementation).
**Grounding:** the mechanism names and guard values below mirror what the Claude Code binary (v2.1.199) actually implements, verified by decompilation: a continuous **microcompaction** channel that clears only whitelisted bulk tool results, whole, persisted to disk first (`"[Old tool result content cleared]"` / `<persisted-output>` pointers, ≥20k-token minimum payoff), a rare **autocompact** at ~80% of the effective window that replaces the whole conversation with a summary and then *re-hydrates* live state (open files, todos, memory), and a **thrash breaker** that declares repeated compaction an error ("Autocompact is thrashing… use /clear"). This is why Claude's context sits at ~100% for many turns without shrinking: ballast is being traded out one-for-one, and nothing is summarized until the rare fold.

The design in one sentence:

> **Context is a compiled view over segments; items are managed by ROLE, not age; pressure is relieved by discarding recoverable ballast; summarization happens only at one rare, guarded event.**

## 1. Every item gets a role at admission

Classified once on entry; the tag drives every later decision:

| Role | What it is | Examples | Fate under pressure |
|---|---|---|---|
| **CHARTER** | instructions currently in force | loaded skill body, an accepted plan | never summarized, never evicted while in force |
| **LEDGER** | commitments & decisions | "approved" + what it scoped, chosen design, stated constraints | never summarized, never evicted |
| **NARRATIVE** | the working conversation | user asks, assistant reasoning/reports | summarized only at the fold |
| **BALLAST** | bulk reproducible data | SQL results, schema dumps, file contents, command output | first to go — evicted **whole**, recoverable |

Loom today evicts by *age* (FIFO) — deleting ledger and narrative while keeping nothing useful. Claude evicts by *role*. Role is the correct predicate.

## 2. The segments, redefined (same five names, new semantics)

```
┌─ ROM ────────────── identity + environment. Session-constant. Never touched.
├─ KERNEL ─────────── tool definitions, structural facts. Near-constant.
├─ CONVERSATION ───── (was L1) THE timeline: charter, ledger, narrative, ballast
│                     — interleaved, append-only, VERBATIM. This IS working
│                     memory. No cap of its own; bounded only by the budget
│                     (window − output reserve).
├─ RESIDUE ────────── (was L2) exists ONLY after a fold: one summary of folded
│                     narrative + carried-forward charter/ledger verbatim.
│                     Empty in most sessions, forever.
└─ SWAP ───────────── the recoverable store. Evicted ballast lands here as
                      addressable items; the conversation keeps a one-line stub
                      per item. Recall is an explicit act (a tool call) — the
                      fault handler.
```

Reframing: **L1 stops being a cache and becomes the conversation; L2 stops being a destination and becomes an event's residue; Swap stops being an archive and becomes the recovery path.**

## 3. How a turn's context is compiled

```
system   = [ROM] [KERNEL]                          ← byte-stable across turns
messages = [RESIDUE, if any]                       ← changes only at a fold
           [CONVERSATION, verbatim, append-only]   ← grows monotonically
           [turn-scoped notes]                     ← THIS turn only, then gone
```

Two rules:
- **Everything dynamic is a message, never system.** Skill menus, harness notices, position reminders — appended as turn-scoped notes at the tail, valid one turn, not persisted. Today's design mutates the system blob per turn, destroying the cache and the event/state distinction at once.
- **The prefix is append-only between folds.** Compilation never rewrites what it already emitted — one rule that buys both the caching invariant and "position is carried by order."

## 4. Pressure management — a three-zone state machine

Budget = window − output reserve.

```
          GREEN (< ~70%)              YELLOW (~70–85%)             RED (> ~85%)
        ┌───────────────┐          ┌──────────────────┐        ┌──────────────┐
        │  do NOTHING   │ ───────► │   the VALVE      │ ─────► │   the FOLD   │
        │ (most sessions│          │ evict BALLAST,   │        │ summarize +  │
        │  live & die   │          │ oldest first,    │        │ re-hydrate,  │
        │  here)        │ ◄─────── │ whole items,     │◄────── │ once         │
        └───────────────┘          │ → SWAP + stub    │        └──────┬───────┘
                                   │ NEVER summarize  │               │ fold again
                                   └──────────────────┘               │ within N turns,
                                                                      ▼ M times?
                                                              ┌──────────────┐
                                                              │   BREAKER    │
                                                              │ stop; tell   │
                                                              │ the user     │
                                                              └──────────────┘
```

**The VALVE** (Claude's microcompaction, adapted): in yellow, evict ballast items — oldest first, *whole*, each persisted to Swap and replaced by a stub (what it was + how to recall it). Minimum-payoff rule (don't bother for crumbs). Touches **only** ballast — charter, ledger, narrative are immune regardless of age. This lets a session sit near the ceiling for many turns without losing anything that matters: growth is paid for with reproducible data, and even that is recoverable.

**The FOLD** (Claude's autocompact, adapted): fires only when the valve can't hold red. One event:
- **Carried verbatim** (skipped, never re-worded): charter still in force, the entire ledger.
- **Summarized** (one LLM pass, structured — state reached, open commitments, refs): the narrative.
- **Re-hydrated** (reconstructed fresh, not preserved): whatever is live right now — the artifact being edited, active task state. Retention of the working set by *reconstruction*, not exemption (Claude's trick).
- Ballast: already stubs; stubs carry through.
- Residue = summary + carried items. Conversation restarts, append-only, after it.

**The BREAKER**: a second fold within a few turns of the first, repeatedly, is not memory management — it's a pathology (something oversized is being re-admitted). Stop compacting, surface it. Repeated summarization is a *failure state*, never an operating mode. Claude ships this as a literal error message; Loom today has the opposite — its steady state *is* the pathology.

## 5. Where summarization happens — the complete answer

**In exactly one place: the fold's narrative pass. Nowhere else, ever.** The valve discards (recoverably), the fold summarizes (once, guarded), the breaker halts. Today's Loom summarizes continuously as its primary mechanism; this design demotes summarization to a last resort executed under protection rules.

## 6. The invariants (the testable spec)

1. Charter and ledger items are **never** paraphrased, summarized, or evicted while in force.
2. The conversation is **verbatim and append-only between folds**; nothing already compiled is rewritten.
3. Anything removed from the conversation is **recoverable by an explicit act** (stub → Swap recall).
4. Summarization occurs **only** at a fold; a fold occurs **only** at red; repeated folds trip the breaker.
5. Everything per-turn-dynamic is a **turn-scoped message**, never a mutation of system or history.
6. An item's treatment is a function of its **role**, never its age alone.

## 7. Proof against the four states

- **S1/S2** — the skill body is a charter *event* in the conversation, arriving downstream of the ask; position readable; nothing in system competes beyond ROM (whose text is the separate, cheap fix).
- **S3** — "approved" sits adjacent to the gate question in a verbatim timeline, and both are ledger, so no valve or fold can ever separate them. Approval scope stays structural.
- **S4** — the 10-table onboarding: schema dumps are ballast, the valve trades them out as the session grows; the body, the approvals, and the design report survive to Gate 4 untouched. If the session ever truly outgrows the window, the fold carries charter+ledger verbatim and summarizes only narrative — and if *that* thrashes, the breaker says so instead of silently shredding.

**Summary: classify by role at the door, compile append-only, relieve pressure by discarding recoverable ballast, summarize once behind guards, and treat repeated summarization as an alarm.** Every mechanism above maps onto an existing Loom seam; implementation design follows once this shape is settled.
