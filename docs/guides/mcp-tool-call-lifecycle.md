# MCP Tool-Call Lifecycle: Park-and-Wake and Session-Handle Auto-Release

**Version**: v1.4.0
**Status**: ✅ Implemented

Two runtime behaviors of Loom's MCP tool adapter that manage the lifecycle of
a tool call beyond the single request/response exchange:

- **Park-and-wake** (issue #343): a failed tool call that links a resource
  waits for that resource to update, then retries — one agent-visible call
  instead of a retry loop.
- **Session-handle auto-release** (issue #345): session handles minted by MCP
  tools during a conversation are released by the runtime when the
  conversation ends, instead of relying on the agent to clean up.

Both behaviors are conventions the **server opts into**. A server that does
not follow either convention sees exactly the pre-existing behavior.

## Table of Contents

- [Park-and-Wake](#park-and-wake)
  - [The Convention](#the-convention)
  - [Requirements and Budget](#requirements-and-budget)
  - [How a Wait Ends](#how-a-wait-ends)
  - [What Does NOT Trigger a Park](#what-does-not-trigger-a-park)
- [Session-Handle Auto-Release](#session-handle-auto-release)
  - [The Convention (Schema-Gated)](#the-convention-schema-gated)
  - [Release Semantics](#release-semantics)
  - [Scope: Per Chat, Not Per Session](#scope-per-chat-not-per-session)
- [Observability](#observability)
- [Limitations](#limitations)

## Park-and-Wake

A tool failure on a transient exhaustion condition (for example, a
session-handle budget that is temporarily full) previously forced agents into
client-side retry loops: one wasted round-trip and one wasted LLM turn per
retry.

### The Convention

A server declares "this resource's next update may clear the failure" by
including a **`resource_link`** content item in a failed `CallToolResult`
(`isError: true`):

```json
{
  "isError": true,
  "content": [
    { "type": "text", "text": "session-handle budget full" },
    { "type": "resource_link", "uri": "example://session-handles", "name": "availability" }
  ]
}
```

When Loom's MCP tool adapter sees this, it parks the call: it opens a
`subscriptions/listen` stream filtered to that URI and retries the original
call when `notifications/resources/updated` arrives for it. The agent sees
one tool call that succeeds late — no retry loop, no polling, no extra turns.

The trigger is **`resource_link` content only**. An embedded plain `resource`
content item in an error result is treated as payload (for example,
diagnostic data), not as a retry condition, and never triggers a park.

### Requirements and Budget

- Requires a **2026-07-28 (stateless) connection** — the wait rides on
  `subscriptions/listen`. Legacy connections surface the error unchanged.
- The wait is bounded by a **60-second budget**, further capped by the
  caller's context deadline (minus a 1-second margin). When the budget is
  exhausted the original error surfaces.
- Anything that prevents waiting — no linked resource, a legacy connection,
  a failed subscription — degrades to the pre-existing error, unchanged.

### How a Wait Ends

1. **Subscription acknowledgment arrives.** The server's ack echoes the
   subset of the requested filter it agreed to honor.
   - If the resource subscription for the linked URI was **not honored**
     (or the ack carries no parseable honored subset), no wake will ever
     come: the adapter fails fast to the original error instead of stalling
     out the budget.
   - If it **was honored**, the adapter performs **one immediate optimistic
     retry**. The ack proves the subscription is registered; a resource
     update that raced the registration window would otherwise be lost, and
     this retry covers it.
2. **`notifications/resources/updated` arrives** for the URI: the adapter
   retries the original call.
3. A retry that fails with the **same** linked condition keeps the park
   alive within the budget. A retry that fails **differently** surfaces that
   new error immediately.
4. **Budget exhausted / context cancelled / stream ended**: the most recent
   error surfaces.

### What Does NOT Trigger a Park

- Successful results — unless the result's `_meta` carries the
  `com.teradata.loom/await-resource` marker AND the embedding application
  wired `agent.WithResourceAwait`, in which case the TURN parks durably
  rather than the call waiting in-turn (a different mechanism entirely; see
  `docs/architecture/resource-await-park.md`).
- Error results without `resource_link` content.
- Error results with an embedded plain `resource` content item only.
- Any call on a legacy (pre-2026-07-28) connection.

## Session-Handle Auto-Release

A 3×64-agent live study showed that when releasing session-scoped resources
is left to agent discretion, it does not happen: across 192 agent runs, zero
agents released a handle, so server-side budget slots never churned until
TTL expiry. The lifecycle therefore belongs to the runtime.

### The Convention (Schema-Gated)

Both ends of the convention are gated on the minting tool's **own
`inputSchema`**:

1. **Tracking**: when a successful tool result carries a top-level
   `session_handle` string in its payload, the handle is tracked for
   end-of-conversation release — but **only if** the tool's `inputSchema`
   `properties` declare a release property, spelled either `releaseHandle`
   or `release_handle`. A tool that never declared the property is never
   tracked and never called back. This prevents auto-release calls against
   permissive servers that would treat the unknown argument as a fresh mint
   request.
2. **Release**: at conversation end, each tracked handle is released by
   calling the tool that minted it with a single argument — the release
   property **as spelled in that tool's schema** (never a client-side case
   convention):

```json
{ "releaseHandle": "tdsh_abc123" }
```

The release is only attempted when the schema's `required` fields are
satisfiable by that single argument (`required ⊆ {release property}`).
Otherwise the release is skipped with a warning and the handle is left to
the server's TTL — the pre-existing behavior, which beats a call that
client-side schema validation is guaranteed to reject.

A handle the agent releases itself mid-conversation (a successful call
carrying the release argument) is untracked and not double-released.

Multi-item results are handled: the `session_handle` may appear in the
payload of any `text` content item of a successful result.

### Release Semantics

- **Best-effort**: failures are logged and skipped; the conversation's
  outcome is never affected.
- **Bounded latency**: the whole release pass shares one **3-second total
  budget** (not per handle) and runs releases concurrently (bounded to 4 in
  flight). The pass is synchronous on the chat return path, so a hung server
  adds at most ~3 seconds to the conversation's return — never 3 seconds per
  handle.
- Runs on a fresh background context, because the conversation's own context
  is typically already cancelled at that point.

### Scope: Per Chat, Not Per Session

Handles are collected and released **per agent message exchange** (each
`Chat`/`ChatWithProgress`/`ChatWithContentBlocks` call). A handle the agent
reuses from conversation history in a **later** message will already have
been released at the end of the message that minted it; the follow-up call
fails and the agent must mint a fresh handle.

This is deliberate: releasing per chat is the only boundary the runtime
currently observes. Callers wanting session-long handle lifetimes need a
session-end hook, which does not exist yet.

Workflow paths and direct executor use do not plant a collector, so their
behavior is unchanged: no tracking, no auto-release.

## Observability

The adapter logs every lifecycle event with `server`, `tool`, and (for
releases) `handle` fields:

- Park entry (`resource-wait: parked failed tool call on linked resource`,
  with `uri` and `budget`), every retry outcome, and every park exit with
  its reason (budget exhausted, subscription declined, stream ended,
  context cancelled).
- Every release attempt outcome: success (`session handle auto-released at
  conversation end`), failure (best-effort warning), and every skip with
  the reason (no release property; required fields beyond the release
  property).

Adapters created through the agent integration paths
(`RegisterMCPTools`, `RegisterMCPServer`, `RegisterMCPTool`, dynamic tool
discovery) have their loggers wired automatically.

## Limitations

- Park-and-wake requires a 2026-07-28 connection; there is no legacy
  (`resources/subscribe`) fallback for parking.
- Session handles are released per chat, not per session (see
  [scope](#scope-per-chat-not-per-session)).
- Handle detection is limited to a top-level `session_handle` string field
  (max 256 characters) in the result payload; other field names are not
  recognized.
- The release pass is best-effort: if the server is unreachable within the
  3-second budget, the handle leaks until server TTL.
