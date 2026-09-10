# Resource-Await Park

**Status:** Implemented (`pkg/agent/park_resource.go`)
**Relates to:** HITL park-and-resume (`docs/architecture/hitl-park-and-resume.md`),
in-turn resource wait (`pkg/mcp/adapter/resource_wait.go`, issue #343)

## Problem

A tool that starts a long-running job follows the MCP event-based resource
pattern: it returns immediately with a success result carrying a
`resource_link` (`gdp://jobs/{id}`), and the caller is expected to subscribe
and act on `notifications/resources/updated`. Neither existing mechanism fits:

- **In-turn resource wait** (issue #343) triggers only on *failed* calls,
  blocks inside the turn (60s budget, bounded further by the embedder's
  request watchdog), and *retries the tool* on wake — re-running a job
  starter starts a second job.
- **HITL park** ends the turn durably, but parks *pre-execution* on a human
  gate, and an approved resume *re-executes* ordinary tools — same
  double-start hazard — while its result-injection primitive was gated to
  `contact_human`.

## Mechanism

```
tool executes ── success result + _meta["com.teradata.loom/await-resource"]
        │                          (adapter stamps shuttle.Result.AwaitResource)
        ▼
ResourceAwaitHandler.PrepareWait(session, call, uri)     ← embedder subscribes
        │ error → result passes to the model unchanged (degrade)
        ▼
row withheld (call stays rowless) ─ after the batch loop, ONE parked
HumanRequest row (RequestType "parked", Kind "resource", descriptor
{seq, kind, tool, params, uri} per call) ─ TurnParkedError ends the turn
        │
        ▼  … resource reaches terminal state; embedder records the decision …
ResumeChat(ParkDecision{Approved, Results[callID] = terminal payload})
        │
        ▼
completeParkedBatch: resource items NEVER dispatch — the payload is injected
verbatim as the call's successful result Data (synthesizeParkedResult), or a
synthesized failure ("resource_wait_failed" / "MISSING_RESULT") lands so the
model can re-plan. The loop re-enters and the turn finishes.
```

Key decisions:

- **Subscribe-before-park.** `PrepareWait` runs before anything is withheld;
  a wait nobody can watch refuses the park and the original result passes
  through. The parked row is persisted only after every `PrepareWait` of the
  batch succeeded; the embedder correlates wait→row via the park notifier or
  the request store (descriptor key `"uri"`).
- **Fail-safe un-hold.** A park row that fails to persist commits every held
  call's original result as its normal tool row (`commitToolRow`) and calls
  `AbandonWait` — nothing strands rowless without a row to resume it.
- **No re-execution, ever.** At resume time the item's recorded descriptor
  kind (the row, not the caller's payload) routes resource items away from
  every dispatch arm, mirroring how the row's status overrides the payload's
  `Approved`.
- **Conversation loop only.** The hold gate (`batchState.parkableTail`)
  carries the pre-scan's durability preconditions and is set only by the
  loop; a resumed batch's remaining calls pass AwaitResource results through
  rather than nesting a park.
- **Binding rules inherited from HITL park.** Descriptors are keyed by
  `ToolCall.ID`; an empty or batch-duplicated ID cannot be bound (and a
  duplicate would alias the held call to a sibling's row in the tail walk),
  so such results pass through instead of holding.

## What the embedder owns

Loom owns the protocol and the turn lifecycle; the embedder owns everything
with a lifetime longer than a call:

- the subscription itself (`pkg/mcp/client.Subscribe` / `SubscribeResource`),
  its reconnect loop, and re-subscribe-after-restart reconciliation;
- reading the resource's terminal content (`ReadResource`) and composing
  `ParkDecision.Results`;
- recording the decision on the row and delivering the resume exactly once
  across processes (same obligation the HITL design doc states for
  embedder-recorded resumes);
- TTL/orphan policy for jobs that never complete (an unresumed row lapses at
  the park TTL like any other parked request).

## Marker

`protocol.MetaAwaitResource` = `"com.teradata.loom/await-resource"`, value
`{"uri": "<resource uri>"}`, on a *successful* `tools/call` result's `_meta`.
Core MCP 2026-07-28 defines no long-running-operation marker (the tasks
extension is a separate, unadopted spec), so the key lives under loom's
reverse-DNS prefix per the `_meta` naming rules. Servers loom's adapter does
not front can be bridged by any embedder-owned tool bridge stamping
`shuttle.Result.AwaitResource` itself.
