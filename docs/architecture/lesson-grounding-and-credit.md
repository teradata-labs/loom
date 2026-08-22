# Lesson Grounding and Outcome Credit

**Status**: 🚧 In Development (branch `feat/graph-memory-lesson-extraction`)

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

Deliberately NOT built (yet): a mechanical vacuous-success detector. The
exact JSON keys (`activity_count`, `row_count`, `rows`) are one MCP
server's conventions; hardcoding them into the framework trades a general
gap for a hidden coupling. If model judgment proves insufficient, the
mechanical layer should arrive as a pluggable per-backend detector seam,
with the JSON-shape detector as its first implementation.

## What this does not do (yet)

- No consolidation/dedup of near-identical lessons; credit demotes bad ones
  but duplicates of good ones still compete with each other.
- Win/loss attribution is class-level, not causal: a conversation that
  recovered for unrelated reasons still credits the injected lesson. The
  claim is only that repeated injection into failing recoveries is evidence
  against a lesson, and repeated presence in successful recoveries is weak
  evidence for it.
- Outcome credit still counts a vacuous success as a recovery (a win): the
  mechanical credit pass has no result-shape judgment. Fix C only guards the
  minting side; credit-side vacuous detection needs the pluggable detector
  seam above.
