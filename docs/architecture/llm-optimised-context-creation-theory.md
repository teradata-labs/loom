# LLM-Optimised Context Creation — Theory

Required reading for every HLD/LLD phase that touches context assembly,
memory, skills, or tool management. Any design that violates a law in this
document is wrong before it is implemented.

---

## The law

```
Tool definitions   : stable for the session
ROM / system prompt: stable for the session (rendered once, at creation)
Conversation       : append-only — until pressure forces release
Release            : valve first (in-place, reversible, visible),
                     fold last (lossy, rare, leaves residue + recall pointer)
Every mutation     : an event the model can see
```

This is one law, not a cache rule plus a cognition rule. The remainder of
this document derives it.

---

## The two readers

A request has exactly two consumers, and they read the same way.

The provider flattens every request into one token sequence, in fixed
order:

```
t1 ..................................................... tN
└─ tools block ─┘└─ system ─┘└─ msg1 ─┘└─ msg2 ─┘ ... └─ msgN ─┘
```

**Reader 1 — the cache.** Prefill computes an attention state (KV) for each
token; the state of token *i* is a function of every token before it. The
cache stores the computed state for a prefix and reuses it only when the new
request's tokens are byte-identical up to that point. This is not a lookup
policy — it is causal attention: change token *k* and the stored states for
every token after *k* are wrong *values*, not stale entries.

```
stored: t1 ... t39  t40   t41 ... t8000     ← t41's state was computed
new:    t1 ... t39  t40'  t41 ... t8000       while attending to t40
                     ↑
        first difference → everything after it recomputed
```

**Reader 2 — the model.** Its understanding of the session is built the
same way — causally, left to right. Position is time; position is
causality. A byte changed behind the model invalidates its accumulated
reading exactly as it invalidates the KV state.

Byte-stability and epistemic stability are therefore the same property.
One shape optimises both because both consumers are prefix-causal. Every
clause of the law is this observation applied to one region of the
sequence.

---

## The law, clause by clause

### Tool definitions: stable

Tools serialize first — they are the earliest tokens. Any change to the
advertised tool set or any tool's description recomputes the entire
request.

The model's reading: the action space must not shift underneath it.
Reliable tool use requires a constant menu; a drifting one produces
miscalls. The one sanctioned change is a change the model itself caused
(a skill load registering its required tools) — the new tool arrives with
its explanation already in context, at a moment the model chose.

### ROM / system: stable

Rendered exactly once, at session creation. Never re-rendered, never
appended to, never filtered. No volatile content — a timestamp in the ROM
is a whole-session cache defect and a lie to the model (the "current time"
is not current on turn 40).

The model's reading: standing orders that never move become trusted
ground. Identity, policy, and the skill roster cannot have changed since
the last turn, so no attention is spent re-verifying them. Background
stays background — which is also the salience truth: content in the head
reads as standing state, so only standing state may live there.

### Conversation: append-only

Nothing already compiled is rewritten. Every new item — user turn,
assistant turn, tool result, skill body — is appended at the tail: the
only free position for the cache and the freshest-attention position for
the model. The transcript and the model's world-model of the session
remain the same object: grounding chains (an approval and the exact
question it answered; a decision and its context) keep their anchors
forever, because anchors are positions and positions never move.

### Release: valve, then fold

Append-only meets a finite window; release is the necessary exception,
and it is engineered — the usual path is not.

**Valve** operates *within the conversation*: it substitutes bulk data
in place with a stub that names what was set aside and how to recall it.
Position preserved, reversible, legible — the model watches its context
being managed rather than having it silently rearranged. Cache cost: the
suffix after the oldest stub, once per beat (batching keeps beats rare).

**Fold** is the last resort and the only lossy event: disposable content
summarised into a residue, important content carried verbatim, a recall
pointer left for the rest. Cache cost: the message history, once.

Each rung of the ladder is more invasive, rarer, and still visible:

```
append  →  valve  →  fold
free       suffix     history
always     rare       last resort
lossless   reversible lossy (residue + recall)
```

---

## The closed set of cache events

Only these operations may change already-compiled bytes. Everything else
must be an append.

| Event | Invalidates | Frequency discipline |
|---|---|---|
| Tool-set change | entire request | only at events that already pay (see below) |
| ROM render | everything after tools | exactly once, at session creation |
| Valve | suffix from oldest stub | rare — payoff-batched beats |
| Fold | message history | last resort |
| Recovery trim / reset | message history | exceptional recovery only |

**The alignment rule:** a tool-set change may only coincide with an event
that already pays a cache cost. The skill-load event bundles its tool
registration, exclusion change, and body append into one paid
invalidation. Progressive tool disclosure pays once, at first need.
Nothing may drift the tools block between events — per-turn tool-list
variation is a per-turn total invalidation and is forbidden.

---

## Corollaries

**A dynamic catalog cannot live in the context.** In the head it violates
ROM stability — and appends at the head are, cache-wise, mutations of the
entire suffix. At the tail it is mis-weighted — an unsolicited list at the
freshest-attention position reads as a directive and provokes action.
Both horns are terminal. Dynamic discovery therefore lives outside the
context: a static roster in the ROM (rendered once) plus a search tool
the model calls at its own discretion, whose results arrive as answers to
its own question — correctly weighted, appended, free.

**Effects travel with events.** Anything a context event implies beyond
its bytes — tool wiring for a skill load, activation state — fires at the
event, and re-fires when the event is replayed (restore). State that is
derivable from the conversation is derived from it, never kept in a
second store that can diverge.

**Position is meaning.** Cause precedes effect in the transcript because
it happened that way. Instructions arrive adjacent to the need that
summoned them. Relevance decay is expressed by receding position. Any
design that places content at a position contradicting its time is lying
to the model.

---

## Invariants (testable)

1. The tools block is byte-identical between any two consecutive calls
   unless a sanctioned event occurred between them.
2. The system field is byte-identical for the session's entire life.
3. Between release events, every compiled payload is a strict extension
   of the previous one (append-only prefix).
4. Every deviation from (1)–(3) is attributable to exactly one event in
   the closed set, visible in the transcript or the event log.
5. No content in the compiled payload contradicts its position's time:
   nothing appears earlier than its cause.
