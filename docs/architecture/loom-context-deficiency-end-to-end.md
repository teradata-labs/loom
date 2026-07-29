# Loom's Context Management Deficiency — One Request, End to End

**Status:** findings, agreed diagnosis. Supersedes the four-layer edition
(`loom-context-management-deficiency.md`).
**Method:** one real conversation followed chronologically through the
machine. Each step names the functions that execute it, shows the concrete
context the model actually reads (Claude vs Loom on identical content where
the contrast is the point), marks the model's decision point, shows the
wrong move as a consequence of the shape, and states the causal law in one
line.
**Companions:** `llm-optimised-context-creation-theory.md` (the laws the
faults violate), `loom-context-reimpl-gotchas.md` (implementation traps and
BUG markers for delivery).

---

## The running example

> **User:** "I need to give data scientists read-only access to
> `test_nda_titanic_db`. Create a new role `data_science` and grant it
> SELECT on `Passenger_Data`."

Skill in play: `new-data-access` — 466 lines, 11 sequential steps, 4
approval gates ("do NOT execute until the user replies 'approved'"). On
Claude the gates hold; on Loom the agent does the domain work, skips the
waits, and in the worst case grants privileges to itself. The difference
is entirely in how the context is managed. Follow the request through.

---

## Step 1 — A user message arrives. Before the model is called

**What happens.** Inside `Agent.runConversationLoop`, the per-turn skill
block runs first: it finds the most recent user message, calls
`Discovery.Discover` — which invokes `index.Router.Route`, **a separate
LLM** prompted per node with `buildRouterPrompt` / `buildLeafPickPrompt`
("Pick the N most relevant skills for the user's message") — then
**force-activates every candidate** via `Orchestrator.ActivateSkill`
(only high-risk is skipped, with a log line the model never sees).
`ActivateSkill` evicts the lowest-confidence non-sticky skill past the
cap (default 3). `Orchestrator.FormatActiveSkillsForLLM` re-renders the
full bodies of all active skills — FIFO by activation time, silently
skipping any that exceed the character budget — and
`SegmentedMemory.InjectSkills` **overwrites** the skill slot with the
result. `Agent.enforceRequiredSkillTools` registers required tools into
the shared registry; `Agent.applySkillExcludedTools` re-filters the
advertised list.

**What the model's standing orders look like across two turns.** Turn 3
it is mid-way through the data-access procedure. Turn 4 the user asks one
side question about a document — the router re-picks, and the skill slot
is rebuilt:

```
── turn 3, system slot ─────────    ── turn 4, system slot ─────────────
# Active Skills                     # Active Skills
## New Data Access                  ## Doc Formatting          ← new pick,
  Step 1 confirm database             Step 1 ...                 inserted
  ...                               ## New Data Access          ← same body,
  Step 7 ⛔ approval gate             Step 1 confirm database      now at a
  Step 8 execute GRANT                ...                         different
                                      Step 7 ⛔ approval gate      byte offset
                                      Step 8 execute GRANT
```

The model issued no `load`, saw no tool call, got no message — yet the
instructions it is meant to be following silently moved and gained a
neighbour. If a third skill had pushed the render past budget,
`FormatActiveSkillsForLLM` would have dropped New Data Access **entirely**
while the orchestrator still reported it active — instructions gone, tool
exclusions still applied.

**Wrong move it enables.** The procedure the model is executing is not a
fixed reference — it shifts under it between turns, and can vanish
mid-run, with nothing in the conversation to signal either.

**Law.** *The judge that picks skills is a stateless side-channel LLM
seeing one message; the working model — the only informed judge — never
decides, sees, or vetoes. And the segment that should be most stable is
the most volatile.*

---

## Step 2 — The payload goes out

**What happens.** `Session.GetMessages` →
`SegmentedMemory.GetMessagesForLLM` compiles ROM, L2 summary, pattern,
skills, findings, promoted context — each as a system-role message — then
L1. The provider adapter (`anthropic.Client.convertMessages`) hoists every
system-role message into the single `system` field, joined `"\n\n"`, and
the request serializes for caching as **tools → system → messages**, one
token sequence, prefix-cached (`cache_control` set in
`convertMessages`).

**What the provider caches vs what changed.** The request is one token
sequence; the provider serves a byte-identical prefix from cache and
reprocesses from the first differing token onward (causal attention — the
KV state of every later token was computed from the bytes that changed).
Green = served from cache, RED = reprocessed:

```
this turn's request:   [TOOLS] [SYSTEM] [msg1] [msg2] ... [msg40]
last turn's prefix:    [TOOLS]···········
                        ▲ step 1 re-registered a tool AND rebuilt the
                          skill slot → first diff is inside TOOLS
                        RED ──────────────────────────────────────► all of it
```

The skill slot lives in SYSTEM and the required tools live in TOOLS —
both **ahead of** the entire conversation in the sequence. So a
per-turn skill change means the first differing byte is at the front,
and every message after it — the whole session — is recomputed.

**Wrong move it enables.** Nothing behavioural — this is the cost fault.
Per-turn input cost grows linear in history, session cost quadratic,
time-to-first-token degrades as the session lengthens. On a skill-enabled
agent the prompt cache (≈10× cheaper reads) buys almost nothing.

**Law.** *The two most volatile things in Loom's context — the tool set
and the skill slot — sit at the front of the cache prefix, so a change to
either reprocesses the entire conversation behind it.*

---

## Step 3 — The model reads it

**What happens.** No code — this is what the compiled layout *means* to
the reader. The model's job at every moment is to predict the next line,
so the deficiency is entirely about **where the procedure sits relative to
the model's decision point.** Same 466 lines, two placements:

**Claude — the procedure is the message right before the model:**

```
system: skills available: new-data-access — provision DB access, gated
[user]      Create role data_science, grant SELECT on Passenger_Data
[asst]      I'll use new-data-access.   →Skill("new-data-access")
[tool]      ## New Data Access
            Step 1 confirm the target database
            ...
            Step 7 ⛔ do NOT run any GRANT until the user replies "approved"
            Step 8 execute the GRANT
[asst]      ▮ ← predicts the next line HERE
```

The freshest message is a checklist, delivered *because the model asked
for it*. The document reads as "a checklist, now being worked." Likeliest
next line: Step 1 — and Step 7's gate is on the path. Correct behaviour
falls out of the shape.

**Loom — the same procedure, in the system preamble:**

```
system: [ROM: use tools, don't fabricate — silent on gates / approval]
        # Active Skills
        ## New Data Access
        Step 1 confirm the target database
        ...
        Step 7 ⛔ do NOT run any GRANT until the user replies "approved"
        Step 8 execute the GRANT
        Follow these skill instructions for this interaction.
[user]  Create role data_science, grant SELECT on Passenger_Data
[asst]  ▮ ← predicts the next line HERE
```

Now the freshest message is the **user commanding the end state**. The
procedure is up in the preamble as standing policy, and the command
already names the finish (`create + grant`) — so the middle steps read as
ceremony the request skipped. Likeliest next line: do what was just
ordered.

**Wrong move.** The model runs the domain work and steps past Gate 7 —
the gate is far away in the preamble, defended only by "please follow
instructions" against the far stronger pull of completing the command in
front of it. (The ROM `roms/START_HERE.md` adds nothing: it never says
stopping to ask is valid.)

**Law.** *The model follows the next event; it merely weighs ambient
policy. Same text is a gate when it is the next message and ceremony when
it is preamble.*

---

## Step 4 — The model works the turn

**What happens.** 8–10 tool iterations inside one user turn. Every large
result lands inline in L1 — the admission threshold defaults to −1
(`storage.DefaultSharedMemoryThreshold`: inline everything). The
compaction check in `SegmentedMemory.AddMessage` runs **per message**;
past the L1 cap (4,000–9,600 tokens by compression profile) it evicts the
oldest batch and summarizes it with the heuristic in `summarizeMessages`
— which emits `"Agent provided analysis"` / `"Tool result received"`.

**What L1 holds as the onboarding runs.** The cap is small; eviction is
oldest-first, per message. Watch what leaves and what stays:

```
early L1                         late L1 (after ~10 evictions)
─────────────────────────        ────────────────────────────────────
[user] onboard titanic_db        [asst] Agent provided analysis      ← summary
[tool] table list                [tool] Tool result received         ← summary
[tool] schema, table 1           [tool] CREATE ROLE ok               ← kept (recent)
 ...   schema, tables 2–10       [tool] GRANT ok                     ← kept (recent)
[tool] sensitivity report        [asst] running step 9...
[asst] READINESS REPORT          ────────────────────────────────────
       (approved column list)    GONE: original request, all schema,
[user] approved  (Gate 1)              sensitivity report, the approved
                                       design, the "approved" itself
                                 INTACT: the 466-line plan (its slot
                                       sits OUTSIDE L1, never evicted)
```

**Wrong move.** At the test phase the model must verify "excluded PII
columns were blocked" — but the approved column list lived in the
readiness report, now evicted to `"Agent provided analysis"`. It must
re-query the database or confabulate. Meanwhile the plan telling it the
steps is pristine.

**Law.** *Eviction is by age, not role: the load-bearing state (request,
decisions, approvals) dissolves while the standing plan is immortal — the
model ends knowing every step and none of its own history.*

---

## Step 5 — Turn N: "approved" arrives

**What happens.** No special code — that is the problem. The user types
one word, and the model must chain it to a scope:

```
approved →  approved WHAT?      → the question I asked last turn
         →  why did I ask it?    → because Gate 1 of the plan says ask
         →  so what may I do?    → the plan's NEXT step, Step 8, only Step 8
```

**Claude — every link is a real message, in order:**

```
[tool] ...Step 7 ⛔ get approval before any GRANT...
[asst] Readiness: role=data_science, SELECT on Passenger_Data,
       PII columns excluded. Approve to proceed to the GRANT?   ← the question
[user] approved                                                 ← lands right here
```

"Approved" sits under the question, which quotes the gate, which is part
of the procedure above it. The chain resolves by reading downward.
Meaning inherited: **narrow** — this gate, the next step, nothing else.

**Loom — the links were evicted (step 4) or never positional:**

```
system: # Active Skills
        ## New Data Access
        Step 1 confirm the target database ...   ← still says "Step 1",
               "for this interaction"               no notion we are at 7
[tool]  Tool result received                     ← the readiness report, gone
[asst]  Agent provided analysis                  ← the gate question, gone
[user]  approved
```

The question "approved" answers is evicted; the plan it should scope
against still opens at Step 1 and claims every turn is turn 1. The chain
breaks at link 2, and an approval with nothing to scope it defaults to the
**widest** reading: general clearance.

**Wrong move.** The model generates the SQL, skips the remaining gates,
executes — and self-grants if the database refuses. One human "yes" that
meant "do Step 8" becomes "do everything."

**Law.** *Position scopes an approval. Delete its referent and place the
plan positionlessly, and "approved" defaults from "this gate" to
"everything."*

---

## Step 6 — A later, unrelated ask

**What happens.** The user asks for a Slack note. Step 1 re-runs:
`InjectSkills` re-stamps the data-access plan at full freshness — an
expired plan never expires. If a newly matched skill exceeds the render
budget, `FormatActiveSkillsForLLM` skips it silently; if it was
high-risk, the discovery block dropped it at step 1 with only a log.

```
[user]  draft a slack note about the outage        ← unrelated ask
system: # Active Skills
        ## New Data Access
        Step 1 confirm the target database...       ← STILL here, full
        Step 7 ⛔ approval gate                        freshness, "for this
        Step 8 execute the GRANT                       interaction"
```

**Wrong move.** The model drafting a Slack note is still carrying a
466-line DDL-with-approval-gates procedure as active standing orders —
attention spent, gate-discipline primed, for a task that has nothing to do
with it. An expired plan never expires.

**Law.** *Skills persist by re-assertion, not by relevance; nothing
retires a plan, so stale procedures ride along on unrelated turns.*

---

## Step 7 — Restart

**What happens.** `Memory.GetOrCreateSession` reloads the session from
the store and `restoreSegmentedMemory` replays the durable rows —
fold-aware, correct: the conversation comes back exactly as it was. But
activation and tool wiring lived in `Orchestrator.activeSessions` — a
process-memory map — and in the process-lifetime registry. Nothing
re-derives them from the replayed conversation.

```
after restart, same session:
  conversation: [tool] ## New Data Access ... Step 4 run query_tool_result(...)
                                              ← model reads: "call query_tool_result"
  orchestrator: { }                           ← empty; nothing re-activated
  registry:     query_tool_result NOT present ← the named tool does not exist
```

**Wrong move.** The model follows the restored instructions to Step 4,
calls `query_tool_result` — and gets "unknown tool." The words survived
the restart; the machinery that made the words executable did not.

**Law.** *A load's effects (activation, tool wiring) lived in process
memory detached from the event, so replay rebuilds the instructions but
not the capability they name — and "active" has two sources of truth that
diverge at every restart.*

---

## Step 8 — Meanwhile: session B, a different user

**What happens.** In step 1, user A's skill load called
`Agent.enforceRequiredSkillTools` → `a.tools.Register` — the **shared**
registry. Progressive disclosure does the same from
`Agent.formatToolResult` (`get_error_details` after any session's first
stored error; `query_tool_result` after any session's first large
result). User B's next turn derives its advertised list from that same
registry via `Registry.ListTools`.

```
user A, session A:   →manage_skills(load, "new-data-access")
                      a.tools.Register("shell_execute")   ← lands in the
                                                            SHARED registry
user B, session B:   next turn, no skill loaded, no transcript event
  [TOOLS: ... shell_execute ...]   ← now advertised to B, who never
                                     loaded the skill or passed its gate
```

**Wrong move.** User B can call `shell_execute` — a capability the
operator never configured for B and B never earned — because user A's
action mutated shared state. B's tools block also changed with no cause in
B's own transcript, invalidating B's cache from position zero.

**Law (BUG — gotchas #18).** *Registration is session-triggered but the
registry is agent-shared, so one user's skill load widens every other
user's action space — capability leak, cross-session cache invalidation,
and (pending store read-path verification) possible cross-user data
access via the disclosed tools.*

---

# The pattern behind all eight steps

Three root causes generate every fault above:

1. **Standing-state channels instead of events.** Skill bodies, patterns,
   findings, promoted context — mutable slots re-rendered per turn,
   positionless, invisible to the transcript. Everything the model should
   experience as *an event at a position* is delivered as *wallpaper that
   was always there*.
2. **State at the wrong scope.** Activation in a process map instead of
   the conversation; capabilities in an agent-shared registry instead of
   a per-session view; a clock inside the session-constant segment.
   Every discontinuity (fold, restart, another user's action) exposes a
   scope mismatch.
3. **Prefix causality ignored.** Both readers of the payload — the cache
   and the model — consume it causally; a byte changed early invalidates
   everything after it for both. The design mutates the earliest bytes
   (tools, system) the most often. Byte-stability and epistemic
   stability are the same property, and the design violates both at once
   (theory doc, "The two readers").

And one closed-form result that constrains every future design:
**a dynamic catalog cannot live in the context.** At the head it violates
stability — and head-appends shift the whole suffix, so even append-only
growth there is a full invalidation. At the tail it is mis-weighted — an
unsolicited candidate list at the freshest position reads as a directive
(observed: the model re-loading its active skills every turn). Dynamic
discovery must live outside the context: a static roster rendered once,
plus a search the model invokes itself.

---

## The issue × state matrix

States: S1 any skill turn 1 · S2 gated skill one turn · S3 multi-turn ·
S4 long multi-turn. Read a column downward for that state's failure
recipe.

| Issue (step) | S1 | S2 | S3 | S4 |
|---|---|---|---|---|
| Wrong judge + force-activation (1) | interpretation tax | gates = ceremony | plan re-asserted, never expires | wallpaper is the only survivor |
| Standing-orders churn (1) | — | mid-turn reorder | eviction mid-procedure; half-alive skills | same, compounding |
| Head/tools volatility (1,2) | full reprocess | full reprocess ×10 iterations | quadratic cost | quadratic cost, worst case |
| Dilution + silent ROM (3) | mild "just do it" | no signal to end the turn | nothing scopes an approval | silence persists as state decays |
| Stale plan never expires (6) | — | — | re-asserted on unrelated asks | same, plus everything else compounding |
| Conveyor eviction + stock-phrase summaries (4) | — | — | approval loses its referent | request, design, approvals all gone |
| No admission control (4) | — | — | accelerates eviction | main fuel of the conveyor |
| Unscoped approval (5) | — | — | one yes = carte blanche | referent already evicted |
| Effects detached / two truths (7) | — | — | after any restart: dead tools | same |
| Shared registry (8) | any state, any user, silently | ″ | ″ | ″ |

Loom is weakest exactly where skills matter most — gated, multi-turn,
long-running workflows compound down the matrix. The evaluation blind
spot is structural: demos live in S1/S2, where the penalty is smallest or
shows only as a skipped gate a "did the work" check misses.

---

## Fix mapping

Each step maps to one element of the target design (consolidated HLD):

| Step | Fix |
|---|---|
| 1 | per-turn push deleted; static bindings roster in ROM (base catalog loader) + `manage_skills(search)` at the model's discretion (skill finder — router as its backend); activation only by the model's explicit `load` |
| 2 | the closed set of cache events; tools-block changes only at events that already pay; append-only compilation |
| 3 | body arrives as a tool result downstream of the ask; ROM gains approval/turn-yield guidance |
| 4 | role classification at admission; valve evicts ballast whole (recoverable); fold rare and role-aware; summarization only at the fold |
| 5 | position carried by transcript order; ledger items (approvals + their questions) immune to valve and fold |
| 6 | nothing re-asserts; a skill is present only while its load body is resident |
| 7 | effects fire at the load event and re-fire on replay; "active" derived from residency only |
| 8 | advertised tools = a per-session projection over the shared registry, changed only by that session's own events |

---

## Outside this diagnosis

- **Enforcement backstop** — permission gate is name-only, gates are
  prose, `contact_human` unused. Decides whether a failure becomes
  damage; owned by the sandbox / least-privilege work item.
- **Eval harness** — runs the Claude Agent SDK, not Loom; green certifies
  nothing for Tera.
- **Skill authoring quality** — domain bugs no harness can save.

## Remaining unknowns

1. **Error/result store read-path scoping** — decides whether step 8
   includes live cross-user data exposure. *Check: trace
   `get_error_details` / `query_tool_result` lookups for user/session
   scoping.*
2. **Sub-agent / ephemeral-agent context construction** — a second
   context surface, unexamined.
3. **Tera's production config** — deployed profiles, budgets, ROM text;
   all traces here used defaults.
4. **Findings summary / promoted swap-context clearing** — same
   standing-state pattern; does anything ever clear them?
