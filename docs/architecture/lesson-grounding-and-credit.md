# Lesson Grounding and Outcome Credit

**Status**: ✅ Implemented and measured (branch `feat/graph-memory-lesson-extraction`);
final ablation and full-curriculum results in the **Measured results** section
at the bottom.

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

## Fix A — diff-grounded lessons

Ground the lesson text in the *literal observed change*, computed
mechanically from the ledger, not the model's narrative.

- `sqlTokenDiff(before, after)`: case-preserving multiset token diff of two
  SQL statements; returns the tokens **added** by the change (`BIGINT`,
  `DECIMAL(18,2)`, …), punctuation filtered.
- Each mined pair carries `ChangedFragments`: the diff of the failing vs
  succeeding input, plus the diff of every intervening call against the most
  recent earlier call of the same class (this is where cross-statement fixes
  like a re-CREATE live).
- The mining prompt lists the fragments and requires the lesson to name
  **every** listed fragment — enumeration, not selection. A recovery that
  changed two things produces a lesson naming both, which keeps the true fix
  in the text even when a red herring rode along.
- **Grounding gate**: a candidate lesson whose content names none of the
  changed fragments (when fragments exist) is dropped, not stored. This is
  enforced in code, not by trusting the model.

## Fix B — outcome credit

Close the loop between injecting a lesson and whether the conversation then
actually recovered.

- The error-triggered lane already tracks which lessons it injected per
  session; it now also records the ledger position at injection time.
- At conversation end (same pass as mining, which owns the ledger), each
  injected lesson is scored against what happened *after* its injection:
  - **win**: some tool-call class that had failed before the injection
    succeeded after it → salience `+0.02` (capped at 1.0).
  - **loss**: a class that had failed before the injection never succeeded
    after it → salience `−0.15` (floored at 0.05).
- The lesson recall lane sets `MinSalience = 0.3`: a lesson that repeatedly
  precedes failure sinks below the threshold within ~4 losses and stops
  being recalled entirely; wins keep good lessons comfortably above it.
- Salience adjustment is an **optional store capability**
  (`AdjustSalience(ctx, memoryID, delta)` on the SQLite store, discovered by
  type assertion) — no `GraphMemoryStore` interface change, so other
  implementations are unaffected and credit is silently skipped where the
  capability is absent.
- Reads deliberately do NOT affect salience (they never did — `TouchMemories`
  only counts accesses); the only salience signals are outcome credit and
  the store's global decay.

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
