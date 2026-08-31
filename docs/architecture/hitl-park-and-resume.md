# HITL Park-and-Resume: A Turn That Ends at a Human Decision

**Status**: 🚧 In Development — PR #382 plus the review fixes on `fix/hitl-park-review`. The park pre-scan, `ResumeChat`, the request lifecycle, and the append-point guard are implemented with tests in `pkg/agent/hitl_park_test.go` and `pkg/agent/hitl_park_lifecycle_test.go`. Park is opt-in per agent (`WithHITLPark`) and is consumed by an embedder — loom itself ships no resume RPC.
**Related**: PR #374 (in-turn hold heartbeat), PR #382 (park-and-resume)

---

## 1. Two ways to wait for a human

Loom has two answers for a tool call that needs a person, and they are not
alternatives so much as different scopes.

| | In-turn hold (#374) | Park-and-resume (#382) |
|---|---|---|
| The turn | stays open, blocked inside the batch | **ends**, `TurnParkedError` |
| The caller's stream | kept alive by a 30s heartbeat | closes; the resume is a new call |
| Bounded by | the resolver's timeout | the request row's TTL (default 168h) |
| Enabled by | an ask resolver on the chain | `WithHITLPark(store, ttl, notifier)` |
| Good for | a decision measured in seconds | a decision measured in hours or days |

A nil park store leaves the in-turn path exactly as it was. An agent that
wires both keeps the hold as the fallback for any ask the pre-scan did not
classify as a park item.

### 1.1 What the progress stream shows

`loom.proto`'s `WeaveProgress.hitl_request` distinguishes three shapes, and
park adds a fourth case to the existing three:

- **absent** — a hold heartbeat, liveness only, never a card;
- **present, empty `request_id`** — the pre-creation ping, an unanswerable card;
- **present, real `request_id`** — an answerable card;
- **present, real `request_id`, then the stream ends** — a *park* card. The
  turn is over; the answer arrives through `ResumeChat`, not through this
  stream.

A consumer written against the hold semantics will wait for heartbeats that
never come. Treat the terminal card as the signal to close the run and hand
the decision to whatever drives resume.

## 2. The pre-scan

`maybeParkBatch` runs *before anything in a batch executes*, after the
assistant row carrying the tool calls is durable. Every call is preflighted
through `Chain.Preflight` — the same hook combination `Admit` uses, with the
blocking resolver never invoked.

| Preflight verdict | Outcome |
|---|---|
| `Deny` | never a park item; falls to dispatch and denies as today |
| `Ask` | **approval item** |
| `contact_human`, registered, not denied | **question item** |
| `contact_human`, unregistered | never an item; "tool not found" at execution |
| `Allow` / `NoDecision` | never an item; dispatches normally at resume |

One or more items parks the **whole batch**: a single grouped `human_requests`
row whose `params` carry one descriptor per item keyed by `ToolCall.ID`, a
durable tail of `…user, assistant(ToolCalls)` with no tool rows, and
`TurnParkedError`.

### 2.1 Durability is a precondition

A request row must never outlive the batch it describes. Park therefore
requires **both** that the assistant row's write succeeded and that a session
store exists at all — `Memory.PersistMessage` returns `nil` when none is
configured, so "persisted without error" is not the same as durable. Without a
store the batch dispatches inline instead, where a governed call with no
resolver fails closed.

## 3. Resume

`ResumeChat(ctx, sessionID, decision, progressCallback)` continues the parked
turn. It appends **no** user message: resuming is not a new turn.

### 3.1 The request row is the binding

`ParkDecision.RequestID` names the row; the batch's item IDs come from **that
row's params**, never from the caller's payload. `ParkDecision.ItemIDs` is an
optional cross-check that must name exactly the row's items when supplied.

This matters because a nested park chains — a resumed turn can park again, so
one session can hold more than one row over its life. A decision bound by a
caller-supplied list could be applied to a batch it never described simply by
omitting the list. Binding through the row makes that unrepresentable.

Two row states resume. A **pending** row is the standalone flow: `ResumeChat`
is the decision channel and closes the row after applying (§3.3). A
**decided** row (`approved`/`rejected`/`timeout`) is the embedder-recorded
flow: the embedder's respond door decided the row first, under its own expiry
CAS, and the resume applies that recorded verdict — the row's status is
authoritative over the caller's payload, so a mismatched payload can never
execute against a rejected row, and expiry is not re-judged at apply time (a
decision recorded in time stands, however much later the resume runs). A row
that is missing, owned by another session, or in any other state is refused
with `ErrUnknownRequest` before the session is touched.

Redelivery cannot re-execute a batch in either flow: the applied batch has
its tool rows and final reply, so a replay lands in the tail-walk terminals
(`ErrNothingParked`, or `ErrStaleDecision` when a nested park owns the tail).
The item binding is by CONTENT, not ID alone (`verifyItemBinding`):
LLM-assigned call IDs can collide across batches (per-response counters), so
each item's recorded tool and batch position must still describe the tail
call it names. And an applied batch whose turn died before its final reply
is NOT refused — the resume re-enters the loop (re-executing nothing) so the
model sees the results and produces the answer.

### 3.2 Applying the decision

| Call | Rejected | Approved |
|---|---|---|
| question item | `permission_denied`, human's reason verbatim | synthesized answer as the `contact_human` result |
| approval item | `permission_denied` | dispatched under the `AskGrant` |
| anything else in the batch | `permission_denied` | dispatched **ungranted** |

The grant lifts `Ask` only — a `Deny` from any hook still dominates — and it
covers **only the calls the card described**. An approval answers the question
it was asked: a call that preflighted `Allow` at park time was never on the
card, so if its verdict has since become `Ask` (a host gate tripping on budget,
quota, or time of day while the human deliberated) it parks or fails closed
like any fresh call, rather than borrowing an approval meant for something
else.

The grant lives only on the derived context handed to the parked batch's
dispatch. The loop re-entry that follows runs ungranted, so a new ask in the
continuation parks again.

### 3.3 Closing the row

In the standalone (pending-row) flow, the row is closed the instant its
decision is applied, **before** the loop re-entry that may park a new one — a
pre-decided row is already closed by the embedder's respond door and is left
untouched:

- approved → `RespondToRequest(…, "approved", …)`
- rejected → `RespondToRequest(…, "rejected", …)`
- past `ExpiresAt` → `ExpireRequest(…, "system:expiry")`, and the decision is
  applied as a refusal whatever it said — an approval must not execute on a
  lapsed authorization.

This is not bookkeeping. `guardParkedTail` refuses a new user turn while a
parked row is pending, so a decided row left pending wedges the session for
every later message. Expiry goes through `ExpireRequest` because the store's
expiry guard is its own and no status payload lifts it.

### 3.4 Terminals

All are terminal — finish the request's lifecycle, never retry the resume.

| Error | Meaning |
|---|---|
| `ErrParkDisabled` | the agent has no park wiring |
| `ErrUnknownRequest` | no pending parked row with that ID owns this session |
| `ErrStaleDecision` | supplied `ItemIDs` do not match the row's items |
| `ErrNotParkedTail` | a user row of a later turn follows the batch |
| `ErrNothingParked` | the turn already produced its final reply |

`ErrNothingParked` covers a turn that ended with rowless calls behind it — the
loop's `MaxToolExecutions` cap can end a turn that way, and those calls were
declined, not deferred.

## 4. Session handles across the gap

MCP session handles are scoped to one call by default, but a parked turn
spans two calls — so a park exit PARKS its `HandleCollector` (one slot per
session; `chat()`'s first park and `ResumeChat`'s nested re-park alike), and
a resume in the same process adopts it: handles minted before the park stay
live through the gap and are released once, by the call that actually ends
the turn.

Adoption only works when one `Agent` instance serves both calls. A pooled
embedder (a fresh `Agent` per call) adopts nothing, so it drains the slot at
each park terminal via `Agent.ReleaseParkedHandles(sessionID)` — keeping its
handles call-scoped with no leak; its resumed turns re-mint on demand. Both
lifecycles are first-class: continuity for same-process embedders, an
explicit release seam for pooled ones.

Only one parked turn per session can exist at a time — `guardParkedTail`
enforces it — so one collector slot per session is the whole contract.

## 5. The append-point guard

A new user turn on a session holding a pending parked row is refused with
`SessionParkedError`, raised at the append point rather than at admission:
the embedder's admission-time probe races a park landing mid-turn, and the
append point is the authoritative last moment. Store errors fail open with a
warning — the embedder's own probe is the primary gate, and a store hiccup
must not kill a session.

## 6. What the embedder owns

- Delivering the card to a human and collecting the verdict.
- Calling `ResumeChat` with the row's `RequestID`.
- Sweeping rows whose TTL lapsed with nobody deciding. Loom closes a row it is
  handed a decision for; it runs no timer of its own, so an abandoned park
  stays pending until a resume (with any decision) or an external sweep closes
  it.

## 7. Known gaps

- 📋 No proto/gRPC surface for resume; park is a library API for embedders.
  Whether loom should expose resume over gRPC is a product decision, not an
  oversight — every other conversation entry point reaches clients through the
  Weave surface, so the asymmetry is worth a deliberate answer.

## 8. A note on hook evaluation count

The pre-scan asks the admission chain the same question execution asks, so a
call that parks and then runs evaluates every matching hook **twice** (three
times for a `contact_human` re-classified at resume). This is safe because
`Hook.Evaluate` is required to be side-effect free — now stated on the
interface — with `PostToolHook` as the seam for anything that records. A host
hook that counts inside `Evaluate` will over-count; that was true before park
existed, but park is the first caller that makes it likely.

Dynamic registration is not doubled: `tryDynamicRegistration` ends in
`registry.Register`, so the pre-scan's registration is a hit for the later
`Execute` rather than a second discovery round-trip. The cost moves earlier,
it does not repeat.
