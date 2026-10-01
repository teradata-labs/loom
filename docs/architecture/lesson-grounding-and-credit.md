# Lesson Grounding and Outcome Credit

**Status**: ✅ Implemented and measured (branch `feat/graph-memory-lesson-extraction`);
final ablation and full-curriculum results in the **Measured results** section
at the bottom.

⚠️ The measured numbers predate the review fixes described in
"What 'verified lesson' means", "Scope of one mining pass", the credit rules
under Fix B, and the fragment-less-pair drop under Fix A. Those fixes change
what gets mined (fewer pairs, no invented causes) and what gets demoted (no
loss without observed contradiction), so the campaign has **not** been re-run
against them.

## Problem (measured, 2026-08-22 fleet curriculum)

The lesson pipeline (ledger mining → fleet pool → task-wording lane +
error-triggered lane) demonstrably delivers lessons, but two failure modes
poison the pool over time:

1. **Misattributed causality.** The miner verifies that a failure was
   followed by a success of the same class; the lesson *text*, however, is
   the LLM's narrative about *why*. When a recovery changed several things
   (widened `amt DECIMAL(14,2)` → `DECIMAL(18,2)` *and* fixed
   `card_id INTEGER` → `BIGINT`), the narrative often credits the wrong
   change. Measured: the pool's top three overflow lessons (481 reads each)
   all recommended the DECIMAL red herring; the causally correct BIGINT
   lessons sat at 0 reads.
2. **Error-echo ranking bias.** The error-triggered lane queries with the
   error text. Lessons that *quote the error verbatim* ("Numeric overflow
   occurred during computation when…") match more query terms than lessons
   phrased causally ("16-digit identifiers into an INTEGER column"), so
   misdiagnoses that parrot the error systematically outrank correct fixes.
   There is no correction signal: a lesson that keeps steering agents into
   failure keeps its standing.

## What "verified lesson" means (lane membership)

The injected heading — "Verified lessons from prior work (each fix was
observed to succeed)" — is a claim about **who wrote the row**, and it is
enforced structurally rather than by asking other writers to behave:

- **Own partition.** Mined lessons are stored under, and recalled from,
  `lessonPartition()`: `__fleet_lessons__` when `fleet_lesson_sharing` is on,
  otherwise `<agent name>__lessons`. Private mode used to reuse the agent's
  own partition — the same one the per-turn extractor and the LLM-callable
  `graph_memory` tool write to — so the lane's contents depended on a prompt
  instruction. A derived partition cannot be forgotten by a future caller
  the way a filter can.
- **Reserved type.** `memory_type: "lesson"` is refused on both other
  ingestion paths: the per-turn extractor drops such a memory (it is not
  relabelled — the same untested theory under another type is the same
  poison), and `graph_memory(action="remember", memory_type="lesson")`
  returns an `INVALID_PARAMETER` error explaining the reservation. The
  per-turn JSON schema no longer offers the class.
- **Both filters live in the store query.** `fleetLessons()` recalls with
  `AgentID = lessonPartition()` and `MemoryType = lesson`, so nothing it
  returns can have been written by the extractor or by the model.

This matters most in the default configuration: `fleet_lesson_sharing` is
off, and that is exactly the case where the lane used to share a namespace
with the two unverified writers.

## Scope of one mining pass

Mining runs at the end of **every `Chat()` call — one user message**, not
once per conversation, because that is where the tool ledger it consumes is
drained (`takeToolLedger`). Consequence, stated plainly: an error hit on turn
1 and fixed on turn 2 is **structurally unmineable** — the pair spans two
ledgers and no single pass ever sees both halves. Only within-turn error→fix
transitions mint lessons. (The measured campaign ran single-turn tasks, where
this distinction does not arise; interactive multi-turn use loses those
cross-turn pairs.)

## Fix A — diff-grounded lessons

Ground the lesson text in the *literal observed change*, computed
mechanically from the ledger, not the model's narrative.

- `tokenDiff(before, after)` (`pkg/agent/graph_memory_lessons.go`):
  case-preserving multiset token diff of two statements; returns the tokens
  **added** by the change (`BIGINT`, `DECIMAL(18,2)`, …) and those **removed**
  by it, punctuation filtered.
- Each mined pair carries `ChangedFragments`: the diff of the failing vs
  succeeding input, plus the diff of every intervening call against the most
  recent earlier call of the same class (this is where cross-statement fixes
  like a re-CREATE live).
- The mining prompt lists the fragments and requires the lesson to name
  **every** listed fragment — enumeration, not selection. A recovery that
  changed two things produces a lesson naming both, which keeps the true fix
  in the text even when a red herring rode along.
- **Fragment-less pairs are dropped before mining.** A pair with no changed
  fragments has nothing to explain. The commonest error→success shape in
  agent tool use is a *transient retry* — a rate limit or timeout, then the
  IDENTICAL input succeeding — which changed nothing; asking the miner to
  explain that change is asking it to invent one. Such pairs never reach the
  prompt (`groundedPairs`). Cost: a genuine cross-statement fix whose
  baseline predates the ledger window (a re-CREATE with no earlier CREATE to
  diff against) also produces no fragments and is dropped too. Recall traded
  for the guarantee that every stored lesson names something that happened.
- **Grounding gate**: a candidate lesson whose content names none of the
  changed fragments is dropped, not stored. The gate is **unconditional** —
  it used to be skipped when the fragment set was empty, i.e. it switched
  itself off in precisely the case where the evidence was weakest and the
  invented cause most likely. Enforced in code, not by trusting the model.

## Fix B — outcome credit

Close the loop between injecting a lesson and whether the conversation then
actually recovered.

- The error-triggered lane already tracks which lessons it injected per
  session; it now also records the ledger position at injection time.
- At the end of the pass that owns the ledger, each injected lesson is scored
  against what happened *after* its injection:
  - **win**: some tool-call class that had failed before the injection
    succeeded (non-vacuously) after it → salience `+0.02` (capped at 1.0).
  - **loss**: the ledger shows **observed contradiction** after the
    injection — a failure, or a mechanically-judged no-work "success" of the
    failing class → salience `−0.15` (floored at 0.05).
  - **nothing**: no post-injection events at all, or a tail holding only
    unrelated successful work. Absence of evidence is not evidence of
    failure. This case is the *normal* ending of an interactive turn: the
    ledger drains every `Chat()` call and the error lane fires on the failure
    that usually ends the turn, so an unguarded "empty tail ⇒ loss" rule
    demoted lessons for existing rather than for being wrong.
- The lesson recall lane sets `MinSalience = 0.3`: a lesson that repeatedly
  precedes failure sinks below the threshold within ~4 losses and stops
  being recalled by the ordinary lane; wins keep good lessons above it.
- **Demotion is not absorbing.** Below the floor a lesson would be
  unreachable forever — never recalled, so never injected, so unable to earn
  the win that is its only way back up (the store's other salience paths are
  decay; `TouchMemories` only counts accesses). Two mechanisms keep it
  reversible:
  - a **bounded re-trial slot**: one demoted lesson (store-side band
    `salience ∈ [0.01, 0.3)`, via the new optional `RecallOpts.BelowSalience`
    — implemented by the SQLite store; a store that ignores it just yields no
    candidate, since the caller re-checks) rides one in
    every `lessonRetrialInterval` = 8 recalls, and *only on the
    error-triggered lane* — the lane outcome credit scores. A re-trial
    nothing measures is cost without evidence, so the conversation-start lane
    never offers one. It is delivered under its own honest heading ("Under
    re-trial (this one has preceded failures before…)"), not as a verified
    fix.
  - **reinstatement on a win**: a demoted lesson that wins its re-trial is
    restored exactly *to* the floor rather than nudged by `+0.02` (13 wins
    from the 0.05 clamp is a theoretical path, not a real one). One observed
    recovery readmits it; one observed contradiction demotes it again.
  The alternative — flooring salience at the recall threshold — was rejected:
  it keeps genuinely bad lessons permanently eligible, which is the failure
  outcome credit exists to fix.
- Salience adjustment is an **optional store capability**
  (`AdjustSalience(ctx, memoryID, delta)` on the SQLite store, discovered by
  type assertion) — no `GraphMemoryStore` interface change, so other
  implementations are unaffected and credit is silently skipped where the
  capability is absent.
- Reads deliberately do NOT affect salience (they never did — `TouchMemories`
  only counts accesses). Outcome credit is in practice the **only** live
  salience signal: `GraphMemoryStore.DecayAll` exists on the interface but
  has no production caller today, which is exactly why demotion had to be
  made reversible by hand rather than left to decay.

## Fix C — vacuous-success judgment (endpoint-agnostic)

A third measured poisoning mechanism: **tool-level success is not task-level
progress.** A date-filter change that silenced a parse error by matching
zero rows (`{"activity_count":0,"status":"success"}`) was mined as a
verified lesson — it is grounded (the change was real) and scores an
outcome-credit win (the failing class stopped failing) — then propagated
fleet-wide and produced whole waves confidently reporting "0 cards, NULL
total, no errors".

The counter: the ledger records a short **result preview** of every
successful call, the mined pair carries the succeeding call's preview, and
the mining prompt shows it with an explicit rule — a success that did no
work (empty rowset, zero rows affected, all-NULL aggregates) is NOT a fix
and the item must be skipped. Judgment stays with the mining LLM, which
makes it endpoint-agnostic: it reads any payload text the way a reviewer
would, instead of `pkg/agent` parsing one server's result schema.

Judgment is tri-layer, endpoint-agnostic by construction:

1. **Tool-supplied** (`shuttle.VacuousResultJudge`, optional interface):
   exact knowledge stays with the endpoint's own adapter; a judging tool
   wins over everything below.
2. **Convention matcher** (`shuttle.ConventionVacuousResult`): recognizes
   the row-count / affected-rows / row-array conventions shared across
   SQL-ish tools in any spelling (`row_count`/`rowCount`,
   `activity_count`/`rowsAffected`/`affected_rows`, `rows`/`records`),
   applies the zero-activity rule only to row-moving SQL verbs (DDL acks
   with 0 activity are normal), and ABSTAINS on anything it cannot
   positively identify. Conventions, never one server's schema.
3. **Model judgment** (the result preview above): covers whatever the
   mechanical layers abstain on — prose payloads, unknown shapes.

Mechanical verdicts feed BOTH sides: a vacuous success cannot close a
mining pair (no lesson minted) and cannot score an outcome-credit recovery
(the win the measured poison would otherwise have earned).

## Trust boundary: tool output becomes durable system-prompt text

**A mined lesson is derived from text a tool server controls, and it is
re-emitted to the model at SYSTEM role.** This is a deliberate trust
decision, and it is written down here for the same reason the lease contract
is written down in `pkg/shuttle/lease.go`: the mechanism is invisible at the
call site and the posture it assumes is not obvious from the code.

The path, end to end:

1. A tool result's error text is server-controlled. For MCP tools, a server
   returning `isError` becomes a `ToolResultError` in `pkg/mcp/client/tools.go`
   and is rendered into `Result.Error.Message` by `pkg/shuttle/executor.go`
   (`"MCP tool execution failed: %v"`). Successful payloads are likewise the
   server's own bytes.
2. The ledger keeps both (`errText`, `resultPreview`), and the mining prompt
   quotes them verbatim: `ERROR:`, `SUCCEEDED RESULT:`, `LATER USE OF THE
   SAME OBJECT:`.
3. The mined lesson is stored and later injected as a message with
   `Role: "system"` — which the Anthropic client hoists into the API's
   top-level `system` field, i.e. the agent's real system prompt.
4. With `fleet_lesson_sharing` on, it lands in `__fleet_lessons__` and is
   visible to **every agent on the server, indefinitely**.

So: **tool servers are operator-chosen and trusted, and a hostile or
compromised server can plant durable, high-salience guidance in agent system
prompts.** The grounding gate is a defence against model *narrative*, not
against server *content*: an attacker who controls both the error string and
the succeeding call's input controls the changed fragments too, so a crafted
error→"fix" sequence produces a lesson that passes the gate. This matches the
deployment posture everywhere else in the tool layer (see the same statement
for lease events in `pkg/shuttle/lease.go` and the trust model in
`docs/reference/security-model.md`).

**Deployment rule: a deployment that runs untrusted MCP servers must not
enable `fleet_lesson_sharing`.** Private mode limits the blast radius to the
agent that talked to the server; fleet mode makes one server's output every
agent's system prompt. A deployment that runs untrusted servers *and* wants
lessons should also disable graph-memory extraction for those agents.

Cheap mechanical mitigations exist and are **noted, not built** in this
round:

- bound what fraction of a stored lesson may be verbatim server text
  (a lesson is supposed to be a generalization, so a near-copy of the error
  string is already a quality signal, not only a security one);
- refuse lessons whose text is instruction-shaped ("ignore previous",
  "always run", imperative second person at the start of a clause);
- cap per-server lesson minting rate, so a single server cannot flood the
  shared pool.

None of these are implemented today.

## What this does not do (yet)

- No consolidation/dedup of near-identical lessons; credit demotes bad ones
  but duplicates of good ones still compete with each other.
- Win/loss attribution is class-level, not causal: a conversation that
  recovered for unrelated reasons still credits the injected lesson. The
  claim is only that repeated injection into failing recoveries is evidence
  against a lesson, and repeated presence in successful recoveries is weak
  evidence for it.
- Credit-side vacuous detection is mechanical-only (layers 1–2): a vacuous
  success that only the model layer could catch (prose payload) still
  scores as a recovery, since credit runs without an LLM call.
- Cross-turn transitions are unmineable (see "Scope of one mining pass").
- Fragment-less pairs are dropped wholesale, including the genuine
  cross-statement fixes whose baseline predates the ledger window.
- Nothing bounds how much of a lesson may be verbatim tool-server text (see
  "Trust boundary").
- Private-mode lessons earned before this change live under the agent's own
  partition and are not migrated; they are simply no longer recalled by the
  lane. The measured campaign ran fleet-shared, where the partition is
  unchanged.

## Fix D — the auditor-miner

v15 measured the limit of tool-level signals: the pool converged on
defensive casts that NULLed the key column — INSERT activity 55316, real
sums, nothing mechanically vacuous, two of three answers wrong. Three
additions, all built from information the pipeline already holds:

1. **Auditor stance**: the mining prompt presents CLAIMED fixes ("an error
   going away proves nothing about the work being right") and requires
   naming what each change traded away. Lessons carry trade-offs and
   applicability conditions instead of verdicts ("…NULLs unconvertible
   values — safe only when the column is not used as a key or in counts
   downstream"), deferring judgment to the consuming agent, which has the
   task the miner lacks.
2. **Downstream evidence**: each pair carries later successful uses of the
   same object with their results (ledger events after the succeeding
   call, matched on the class's target identifier, last 3 kept). A later
   use showing zeros/NULLs where the task expected data instructs the
   miner to record a WARNING against the change.
3. **Task background**: the conversation's opening request rides along as
   context for judging whether a change served it, with the explicit rule
   that lessons must still generalize beyond the task.

## Measured results (12 × gpt-4o fleet, Teradata MCP curriculum, 2026-08-22/23)

**Ablation protocol** (L1 from wiped pool → L2 preserved → both again;
144 attempts per run, identical prompts and model):

| run | added layer | correct /144 | failure signature |
|---|---|---|---|
| v12 | grounding + error-time delivery + credit | 0 | confident empty answers |
| v14 | prompt rule "no-work success is not a fix" | 3 | same (miner ignored it) |
| v15 | mechanical vacuous detection (mint + credit) | 3 | poison mutated: NULLed key column, tool-level signals healthy |
| v16 | auditor-miner (Fix D) | **102** | learning curves; L1 revisit opened 12/12 |

**Full 10-level curriculum** (fresh pool, everything earned in-run):
scribe pipeline (v11) 117/360 (33%) → audited pipeline (v16) 199/360
(55%); trap tasks 33/252 → 111/252. **Second exposure** of the audited
pool: 240/360 (67%), with the entire gain at the cold-start levels
(L1–L2: 12→56 of 72) and everything else stable — where the scribe pool's
second exposure HALVED its trap-task score. Pool stable at 784 lessons /
8,658 reads.

**The boundary (level-9 autopsy)**: one level scored 0/36 in every
configuration. Per-metric scoring shows the fleet nearly solves it (24/36
and 23/36 exact on the two hard metrics); every attempt fails the third
identically — `MAX(txns)` on a per-card-per-hour table where the busiest
*hour* needs `SUM(txns) GROUP BY hour_str` first. The method is correct
on the earlier per-hour-grained level (29/36) and silently wrong here:
negative transfer of a genuinely correct method. It never errors, so the
error→fix pipeline is structurally blind to it; all twelve agents share
the bias, so consensus would confirm the wrong answer. Reaching it
requires channels outside errors: self-check disciplines, answer-validated
feedback, or graded scoring.
