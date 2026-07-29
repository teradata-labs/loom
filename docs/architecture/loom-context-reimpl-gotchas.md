# Context/Skills Reimplementation — Gotchas

Traps discovered implementing the previous attempt (PR #266, TER-419). Planner
and coder must design around these. Each entry: the trap, then the exact
remediation.

Entries marked **BUG** are defects present in the existing code (main or the
prior attempt): the design phase must carry their fix as an explicit design
element, and the delivery must implement and test it. Unmarked entries are
constraints and traps — obeyed by construction, no discrete fix to ship.

## Skill lifecycle

1. **BUG** — **Load result shape caused reload loops from both extremes.**
   A metadata-map receipt rendered as Go-map garbage and lost the checklist.
   A bare body (the v5 correction) had no "it worked" signal — the model
   couldn't tell the load succeeded, nor tell later that the resident
   instructions meant the skill was loaded, so it re-loaded every turn
   (amplified by the always-present roster).
   Fix: the load result is the skill body as **plain text** (markdown, never
   JSON, never a map), **framed** with a one-line confirmation header:
   `# Skill loaded: <name>\n(Active for this conversation — do not reload.)\n\n<verbatim body>`.
   Header confirms + names + says don't-reload; body is verbatim. Not a
   receipt, not a bare body.

2. **BUG** — **Tools wired a turn late; never wired after restart.**
   Fix: fire wiring (activation + required-tool registration) inside
   `executeLoad`, and again on restore for every load body replay puts back in
   L1. Replay wiring = activation only: no task emission, no new tool_result.

3. **BUG** — **Deleting a load message from L1 orphaned its `tool_use` → provider 400s.**
   Fix: never remove or rewrite messages of the pair. No unload verb exists.
   Retirement = fold reclaims the body. Full stop.

4. **BUG** — **"Active" computed from two sources (process map vs context) → livelock,
   post-restart contradictions.**
   Fix: one source — a skill is active iff its load body is resident in
   context. `list` and wiring derive from that. No second bookkeeping set.

5. **BUG** — **Skill cap existed with no cost behind it → dead knobs, lying errors,
   count drift.**
   Fix: no skill-specific cap. Loads are tool calls; per-turn tool cap, dedup,
   and the pressure pipeline govern them like every other append.

6. **BUG** — **Loader results classified charter → bodies pinned forever, accumulating
   across reloads.**
   Fix: load results classify narrative. Fold summarizes them into residue;
   reload is the recovery path.

7. **BUG** — **Per-turn skill menu (tail note) → model re-loaded active skills every
   turn; menu-as-user-message broke role alternation.**
   Fix: no per-turn skill surface of any kind. Static bindings list rendered
   into ROM once at session creation; live discovery only via
   `manage_skills(search)` (router behind the tool).

8. **BUG** — **High-risk skills silently skipped.**
   Fix: high-risk load returns an explicit gate error result. Nil permission
   checker = gate disabled, load proceeds.

## Library / tests

9. **Unprimed library cache silently matches nothing.**
   Fix: call `library.List()` before any `FindBy*`.

10. **Register-only skills invisible to `ListAll`; host skills leak into
    tests.**
    Fix: list-path tests write real skill YAML files; every test pins
    `LOOM_SKILLS_DIR` to an empty temp dir.

## Context pipeline

11. **BUG** — **Large tool results enter context whole (admission disabled).**
    The admission threshold defaults to −1 (inline everything), so a 25k-token
    `SELECT *` lands in L1 raw and bloats the budget for the rest of the
    session.
    Fix: admission is a plain byte-size gate — result over threshold → store
    aside, put a preview + recall handle in context; a short exempt list
    (skill/pattern load, `recall_context`) always enters whole. **Default the
    threshold to a sane positive value (e.g. 4 KiB), never −1.** No roles, no
    classification — a size check, nothing more.

12. **Tool results rendered as Go-map syntax.**
    `map[action:list active_count:0 …]` is `fmt %v` of a Go map leaking into
    context; the model has to parse Go internals.
    Fix: in the result formatter — string `Data` passes through verbatim
    (skill bodies stay plain markdown, per #1); map/struct/slice `Data` is
    `json.Marshal`ed. Never `fmt %v` a Go composite into the conversation.

13. **Treated valve-stub loss on restart as a bug.**
    Fix: valve stubs are in-memory only by design; durable rows keep
    originals; the next pressure beat re-derives stubs. Never persist valve
    output.

14. **Fold carry orphaned tool pairs / trusted assistant claims.**
    Fix: carry must apply tool-pair closure and ledger-user adjacency. State
    derives from message structure (tool_result metadata), never from
    assistant text ("loaded X" may survive after its evidence folded).

15. **Breaker assumed one fold per turn.**
    Fix: fold streak counts within a single turn too; one saturated turn can
    trip the breaker — tests and recovery must handle it.

16. **A second system-role message silently corrupted the system prompt.**
    Fix: exactly one system message per compiled payload; providers join all
    system messages into one field.

17. **"Compressor not called" read as fold failure.**
    Fix: compressor runs only when the narrative pile is non-empty; carry can
    legitimately absorb everything.

## Tools block (position zero of the cache prefix)

18. **BUG** — **Tool registry is agent-shared; session-triggered registrations leak
    across users.** User A's skill load registers its required tools in
    `a.tools`; user B's next request on the same agent advertises them —
    tools B never gated for, with no event in B's transcript. Capability
    widening beyond the agent's configured surface, and cross-session cache
    invalidation.
    Fix: advertised tools = a per-session projection. Global registry holds
    what can exist; each session's advertised block changes only at that
    session's own events (its loads, its own first-error disclosure). One
    session's event never changes another session's bytes.

18b. **BUG** — **Per-session data stores are shared, so a leaked disclosure
    tool could read another session's data.** `get_error_details` /
    `query_tool_result` / recall resolve against the error store, SQL-result
    store, and shared-memory store. If those stores key by bare id, a
    session holding or guessing an id reads another session's (another
    user's) stored errors or query results.
    Fix: no sharing. Every store is partitioned by session (and user), and
    every lookup is scoped to the caller's own partition — a session
    physically cannot address another's data, independent of whether the
    tool is advertised. This is a hard isolation boundary, not a scoping
    check bolted onto a shared store.

19. **BUG** — **Fold leaks a trailing tools-block invalidation.** Fold reclaims a
    skill's body → active set shrinks → its excluded-tools filter lifts —
    on the NEXT turn. Result: fold pays the message history at its beat,
    then a total (position-zero) invalidation one turn later. Two cache
    events for one release.
    Fix: recompute the session's advertised set on the same beat the fold
    fires. One event, one paid invalidation.

20. **Any tools-block drift between events is a total cache invalidation.**
    The tools block serializes ahead of system and messages; one changed
    byte reprocesses the entire request.
    Fix: the advertised set may change only at sanctioned events (session's
    own load, first-need disclosure, fold beat per #19). Never per-turn
    recomputation drift. Ordering is name-sorted (already enforced in
    `ListTools`) — keep it that way.

## Process

21. **Go-struct config field with no proto/YAML path — operators can't set
    it.**
    Fix: any new knob ships proto field → `buf generate` → registry mapping →
    YAML key, or doesn't ship.

22. **Widened `SegmentedMemoryInterface` silently no-ops for old
    implementers.**
    Fix: log once on type-assertion miss, minimum.

23. **Hook fired under `Memory.mu` → re-entrant deadlock.**
    Fix: collect under lock, invoke hooks after unlock.

24. **CI lint failed on checks not run locally.**
    Fix: before every push: `gofmt -l` (CI's separate step) + golangci-lint at
    CI's pinned version, both clean.

## Testing pattern (sample for the test lead)

The reimplementation's acceptance (HLD invariant 13) is an LLM-consumer eval
over the compiled context. The test lead should design the suite in the
style of the samples below — not narrow unit tests, but a full multi-turn
end-to-end flow driving the real `Agent.Chat` loop with a scripted LLM,
capturing exactly what the model was sent each turn, asserting on that
compiled context, and finishing with an LLM-consumer eval.

The reference implementations already in-repo (from the prior attempt —
reuse the shape, not the v5-specific assertions):

- `pkg/agent/context_v5_e2e_test.go` — one long session through the pressure
  pipeline; per-turn capture of the provider-bound message slice; assertions
  on context shape (one system message, append-only prefix, admission
  wrapping, fold carry, breaker).
- `pkg/agent/skill_wiring_event_test.go` — the load event's effect (wiring)
  asserted at the boundary the model actually sees (advertised tools per
  call), plus restore replay.
- `pkg/agent/skill_discovery_activation_test.go` — the `dumpLLMCall` hook:
  writes per-call `state` + `context` pairs to disk under
  `LOOM_TEST_DUMP_DIR`.
- `cmd/loom-v5-eval/` — reads those dumps and runs the LLM-consumer eval:
  feeds (state, context) to a model positioned as the consumer, returns
  concrete concerns.

The pattern per test: **drive all steps → capture the compiled context each
turn → assert the shape → dump state+context → run the consumer eval at the
end.** A green structural pass with a clean consumer-eval verdict is the
definition of done for a context-affecting change.
