# Graph Memory Architecture

Salience-driven graph-backed episodic memory for persistent, cross-session agent knowledge. Entities (mutable nodes) represent current state; memories (immutable records) represent historical record; edges (mutable relationships) form a knowledge graph. FTS5 provides full-text search; salience scoring with time decay and access boosting drives retrieval ranking.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.2.0

**Status**: Implemented on `graph-memory` branch


## Table of Contents

- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
  - [Knowledge Graph (Entities + Edges)](#knowledge-graph-entities--edges)
  - [Episodic Memory (Memories)](#episodic-memory-memories)
  - [Memory-Entity Bridge](#memory-entity-bridge)
  - [Lineage Tracking](#lineage-tracking)
  - [Salience Engine](#salience-engine)
  - [FTS5 Search](#fts5-search)
  - [Agent Tool](#agent-tool)
  - [Context Injection](#context-injection)
- [Key Interactions](#key-interactions)
  - [Remember Flow](#remember-flow)
  - [Recall Flow](#recall-flow)
  - [Supersede Flow](#supersede-flow)
  - [Context Injection Flow](#context-injection-flow)
- [Data Structures](#data-structures)
- [Algorithms](#algorithms)
  - [Salience Scoring](#salience-scoring)
  - [Token Budgeting](#token-budgeting)
  - [Graph Traversal](#graph-traversal)
- [Design Trade-offs](#design-trade-offs)
- [Constraints and Limitations](#constraints-and-limitations)
- [Performance Characteristics](#performance-characteristics)
- [Concurrency Model](#concurrency-model)
- [Configuration](#configuration)
- [Related Work](#related-work)
- [References](#references)
- [Further Reading](#further-reading)


## Design Goals

1. **Cross-Session Persistence**: Knowledge survives session boundaries -- agents remember users, decisions, and context across conversations
2. **Salience-Driven Retrieval**: Not all memories are equal; salience scoring with time decay ensures the most important and recent memories surface first
3. **Immutable Audit Trail**: Memory content is append-only with lineage tracking (SUPERSEDES, CONSOLIDATES), providing a complete historical record
4. **Token Budget Control**: Automatic context truncation prevents memory injection from overflowing the LLM context window
5. **Agent-Scoped Isolation**: Each agent maintains its own knowledge graph; no cross-agent leakage
6. **Opt-Out by Default**: Graph memory is enabled when a store is available; agents must explicitly disable it

**Non-goals**:
- Vector/embedding-based semantic search (FTS5 keyword search is the current approach)
- Real-time inter-agent knowledge sharing (each agent has its own graph)
- Unbounded memory growth (salience decay and token budgeting control retrieval)


## System Context

```
┌─────────────────────────────────────────────────────────────────────────┐
│                       External Environment                              │
│                                                                         │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐               │
│  │ LLM Provider │   │ Agent Runtime│   │ User / MCP   │               │
│  │ (200K ctx)   │   │ (Chat loop)  │   │ Client       │               │
│  └──────┬───────┘   └──────┬───────┘   └──────┬───────┘               │
│         │                  │                   │                        │
│         │    context_for   │   graph_memory    │                        │
│         │◀─────────────────┤   tool calls      │                        │
│         │                  │◀──────────────────┤                        │
│         │                  │                   │                        │
│         │                  ▼                   │                        │
│  ┌────────────────────────────────────────────────────────────────┐    │
│  │                   Graph Memory System                          │    │
│  │                                                                │    │
│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐              │    │
│  │  │  Entities   │──│   Edges    │  │  Memories   │              │    │
│  │  │  (mutable)  │  │ (mutable)  │  │ (immutable) │              │    │
│  │  └──────┬──────┘  └────────────┘  └──────┬──────┘              │    │
│  │         │                                │                     │    │
│  │         └────────────┬───────────────────┘                     │    │
│  │                      ▼                                         │    │
│  │              ┌──────────────┐                                  │    │
│  │              │  Junction    │  (memory_entities: N:N bridge)   │    │
│  │              └──────────────┘                                  │    │
│  └────────────────────────────────────────────────────────────────┘    │
│                         │                                              │
│                         ▼                                              │
│  ┌────────────────────────────────────────────────────────────────┐    │
│  │              SQLite / PostgreSQL Storage                        │    │
│  │  graph_entities, graph_edges, graph_memories,                  │    │
│  │  graph_memory_entities, graph_memory_lineage,                  │    │
│  │  graph_memories_fts (FTS5), graph_entities_fts (FTS5)         │    │
│  └────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────┘
```

**External Dependencies**:
- **LLM Provider**: Context window constraint; graph memory context injected as system message
- **Agent Runtime**: Calls `injectGraphMemoryContext()` each turn, registers `graph_memory` tool
- **Storage Backend**: Must implement `GraphMemoryProvider` interface to supply a `GraphMemoryStore`


## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                      Graph Memory System                                │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                     Agent Integration Layer                      │   │
│  │                                                                  │   │
│  │  ┌──────────────────┐        ┌──────────────────┐              │   │
│  │  │  GraphMemoryTool │        │  Context Injector │              │   │
│  │  │  (8 actions)     │        │  (per-turn query) │              │   │
│  │  │                  │        │                   │              │   │
│  │  │  remember        │        │  Extract topic    │              │   │
│  │  │  recall          │        │  from messages    │              │   │
│  │  │  forget          │        │       │           │              │   │
│  │  │  supersede       │        │       ▼           │              │   │
│  │  │  consolidate     │        │  ContextFor()     │              │   │
│  │  │  context_for     │        │  query with       │              │   │
│  │  │  entities        │        │  token budget     │              │   │
│  │  │  relate          │        │       │           │              │   │
│  │  └────────┬─────────┘        │       ▼           │              │   │
│  │           │                  │  Inject as        │              │   │
│  │           │                  │  system message   │              │   │
│  │           │                  └──────────────────┘               │   │
│  └───────────┼──────────────────────────────────────────────────────┘   │
│              │                                                          │
│              ▼                                                          │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                   GraphMemoryStore Interface                     │   │
│  │                                                                  │   │
│  │  Entity CRUD ─── CreateEntity, GetEntity, UpdateEntity,         │   │
│  │                   ListEntities, SearchEntities, DeleteEntity     │   │
│  │                                                                  │   │
│  │  Edge CRUD ───── Relate (upsert), Unrelate, Neighbors,         │   │
│  │                   ListEdgesFrom, ListEdgesTo                     │   │
│  │                                                                  │   │
│  │  Memory Ops ──── Remember, GetMemory, Recall, Forget,          │   │
│  │                   Supersede, Consolidate, GetLineage            │   │
│  │                                                                  │   │
│  │  Salience ────── TouchMemories, DecayAll                        │   │
│  │                                                                  │   │
│  │  Composite ───── ContextFor (entity + edges + ranked memories)  │   │
│  │                                                                  │   │
│  │  Stats ───────── GetStats                                       │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│              │                                                          │
│              ▼                                                          │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │               Storage Implementations                            │   │
│  │                                                                  │   │
│  │  ┌────────────────────┐     ┌────────────────────┐             │   │
│  │  │  SQLite Store      │     │  PostgreSQL Store   │             │   │
│  │  │  FTS5 + triggers   │     │  Migration only    │             │   │
│  │  │  Recursive CTEs    │     │  (Go store NOT yet │             │   │
│  │  │  for graph walk    │     │   implemented)     │             │   │
│  │  └────────────────────┘     └────────────────────┘             │   │
│  └─────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

> **PostgreSQL Status**: Only the SQL migration exists (`pkg/storage/postgres/migrations/000010_graph_memory.up.sql`). No Go `GraphMemoryStore` implementation exists for PostgreSQL yet. The SQLite store (`pkg/storage/sqlite/graph_memory_store.go`) is the only working implementation.


## Components

### Knowledge Graph (Entities + Edges)

**Responsibility**: Represent mutable current-state knowledge as a directed graph.

**Entities** are nodes with:
- `name` (UNIQUE per agent_id) -- the human-readable identifier
- `entity_type` -- person, tool, pattern, concept, project, device, etc.
- `properties_json` -- arbitrary structured data
- Mutable: name, type, properties can all be updated

**Edges** are directed relationships with:
- `source_id` -> `relation` -> `target_id` (e.g., "Alice" -> WORKS_ON -> "Project X")
- UNIQUE constraint on (source_id, target_id, relation) -- upsert semantics via `Relate()`
- `properties_json` for edge metadata
- Mutable: properties can be updated, edges can be removed via `Unrelate()`

**Graph Traversal**: Recursive CTEs support multi-hop neighbor discovery with configurable depth and cycle prevention.

**Invariant**:
```
Entity(agent_id, name) is UNIQUE per agent
Edge(source_id, target_id, relation) is UNIQUE (upsert on conflict)
```


### Episodic Memory (Memories)

**Responsibility**: Immutable, append-only historical record of facts, decisions, experiences, and observations.

**Key Properties**:
- **Content is immutable**: Once created, `content`, `summary`, `memory_type`, and `tags` never change
- **Provenance fields** (immutable): `source` (conversation/observation/manual/agent), `source_id`, `owner`, `memory_agent_id`, `properties_json`
- **Token tracking** (immutable): `token_count` and `summary_token_count` are precomputed at write time for budget allocation
- **Only mutable fields**: `accessed_at`, `access_count`, `salience`, `expires_at`
- **No `updated_at` column**: Deliberate schema design enforcing immutability
- **Soft delete via `expires_at`**: `Forget()` sets `expires_at` to now; content preserved for lineage

**Memory Types** (7):
- `fact` -- verifiable information ("User prefers dark mode")
- `preference` -- user/agent preferences
- `decision` -- recorded decisions with rationale
- `experience` -- procedural knowledge from interactions
- `failure` -- what went wrong and why (failure learning)
- `observation` -- ambient observations
- `consolidation` -- merged summary of multiple memories

**Invariant**:
```
Memory.Content immutable after creation
Memory.ExpiresAt = nil (active) or non-nil (forgotten/expired)
Memory.Salience in [0.0, 1.0]
```


### Memory-Entity Bridge

**Responsibility**: Many-to-many junction linking memories to entities with typed roles.

**Roles** (4):
- `about` -- memory is about this entity
- `by` -- memory was created by this entity
- `for` -- memory is intended for this entity
- `mentions` -- memory mentions this entity

**Schema**: `graph_memory_entities(memory_id, entity_id, role)` with composite PK.

This bridge enables the composite `ContextFor` query: given an entity, find all related memories with their roles.


### Lineage Tracking

**Responsibility**: Maintain explicit chains for memory evolution.

Two relation types:
- **SUPERSEDES**: New memory replaces old (correction). The old memory's `IsSuperseded` status is computed at read time by querying the lineage table (`SELECT COUNT(*) FROM graph_memory_lineage WHERE old_memory_id = ? AND relation_type = 'SUPERSEDES'`), not stored as a column.
- **CONSOLIDATES**: New memory merges N old memories into summary. Source memory salience is decayed (multiplied by `consolidation_decay`, default 0.5) -- they are NOT soft-deleted.

**Schema**: `graph_memory_lineage(new_memory_id, old_memory_id, relation_type, created_at)` with PK on `(new_memory_id, old_memory_id)`

**Rationale**: Enables audit trail reconstruction. Given any memory, `GetLineage()` returns the full chain of corrections/consolidations.


### Salience Engine

**Responsibility**: Rank memories by importance using time decay and access boosting.

**Salience Model**:
- Initial salience set at creation (default 0.5, range 0.0-1.0)
- **Time decay**: `salience *= decay_rate` per day (default 0.995)
- **Access boost**: `salience = min(1.0, salience + boost_amount)` on each access (default 0.05)
- **DecayAll**: Batch operation applying decay factor to all memories for an agent

**Ranking Formula** (for recall):
```
combined_score = computed_salience * relevance
```

Uses multiplicative combination (`CombineScores` in `pkg/memory/salience.go:122`). There is no `relevance_weight` tuning parameter. `computed_salience` factors in base salience, time decay since last access or creation. `relevance` comes from FTS5 rank when a search query is present.


### FTS5 Search

**Responsibility**: Full-text search over memory content and entity names.

**Two FTS5 virtual tables**:
1. `graph_memories_fts` -- indexes `content`, `summary`, `tags` (Porter stemming + Unicode61)
2. `graph_entities_fts` -- indexes `name`, `entity_type`, `properties_json`

**Triggers**: Automatic sync on INSERT + DELETE (memories) and INSERT/UPDATE/DELETE (entities). No UPDATE trigger on memories because content is immutable; DELETE trigger exists for cleanup when rows are removed.


### Agent Tool

**Responsibility**: Expose graph memory to agents via the `graph_memory` tool with 8 actions.

**Tool Name**: `graph_memory`

**Actions**:

| Action | Purpose | Required Params |
|--------|---------|-----------------|
| `remember` | Store new memory | `content` |
| `recall` | Search memories by query | (none -- all optional filters) |
| `forget` | Soft-delete memory | `memory_id` |
| `supersede` | Replace memory with correction | `memory_id`, `content` |
| `consolidate` | Merge memories into summary | `memory_ids` (2+), `content` |
| `context_for` | Entity profile + memories | `entity_name` |
| `entities` | Search/list entities | (none -- optional `query`, `entity_type`) |
| `relate` | Create entity relationship | `source_name`, `target_name`, `relation` |

**Progressive Disclosure**: Tool registered lazily via `checkAndRegisterGraphMemoryTool()` -- only when store is available, config is enabled, and tool not already registered.

**Auto-Create Entities**: The `relate` action auto-creates entities (type "concept") if they don't exist, reducing friction for graph building.

**Implementation**: `pkg/agent/graph_memory_tool.go`


### Context Injection

**Responsibility**: Automatically inject relevant graph memory context into each conversation turn.

**Flow** (called from `runConversationLoop()` at each turn):
1. Extract topic from the most recent user message (truncated to 200 chars)
2. Query `ContextFor()` on the agent's own entity name, using topic as search filter
3. Format result via `EntityRecall.Format()` into structured text
4. Inject as system message: `[Graph Memory Context]\n{formatted_recall}`

**Token Budget**: Controlled by config. Default: 10% of context window (200K * 10% = 20K tokens). Can be overridden with absolute `max_context_tokens`. Within `ContextFor()`, the budget is allocated in three phases: entity profile (200 tokens reserved), graph neighborhood (300 tokens reserved), and remaining budget for ranked memories via `AllocateMemoryBudget()` in `pkg/memory/budget.go`.

**System Prompt Supplement**: When graph memory is enabled, `graphMemoryPromptSupplement()` appends usage instructions to the agent's system prompt. This teaches the agent when and how to use the `graph_memory` tool (store, retrieve, update, remove, graph, context actions) without requiring manual prompt engineering.

**Implementation**: `pkg/agent/agent.go:injectGraphMemoryContext()` (per-turn context), `pkg/agent/agent.go:graphMemoryPromptSupplement()` (system prompt)


## Key Interactions

### Remember Flow

```
Agent          GraphMemoryTool    GraphMemoryStore    SQLite
  │                  │                  │               │
  ├─ remember ──────▶│                  │               │
  │  {content,       │                  │               │
  │   summary,       ├─ Remember() ───▶│               │
  │   salience,      │                  ├─ INSERT ─────▶│
  │   tags,          │                  │  graph_memories│
  │   entity_ids}    │                  │               │
  │                  │                  ├─ INSERT ─────▶│
  │                  │                  │  memory_entities│
  │                  │                  │  (per entity)  │
  │                  │                  │               │
  │                  │◀─ Memory ───────┤               │
  │◀─ {id, tokens, ─┤                  │               │
  │    salience}     │                  │               │
```

### Recall Flow

```
Agent          GraphMemoryTool    GraphMemoryStore    FTS5
  │                  │                  │               │
  ├─ recall ────────▶│                  │               │
  │  {query,         │                  │               │
  │   min_salience,  ├─ Recall() ─────▶│               │
  │   limit}         │                  ├─ FTS5 match ─▶│
  │                  │                  │◀─ ranked ─────┤
  │                  │                  │               │
  │                  │                  ├─ Filter by    │
  │                  │                  │  salience,    │
  │                  │                  │  type, tags,  │
  │                  │                  │  superseded   │
  │                  │                  │               │
  │                  │                  ├─ TouchMemories│
  │                  │                  │  (boost access)│
  │                  │                  │               │
  │                  │◀─ []*Memory ────┤               │
  │◀─ {results} ────┤                  │               │
```

> Note: Token budget truncation occurs in `ContextFor()` (via `AllocateMemoryBudget()`), not in `Recall()`. The `Recall()` method returns results up to `Limit` with salience-based ordering.

### Supersede Flow

```
Agent          GraphMemoryTool    GraphMemoryStore    SQLite
  │                  │                  │               │
  ├─ supersede ─────▶│                  │               │
  │  {memory_id,     │                  │               │
  │   new_content}   ├─ Supersede() ──▶│               │
  │                  │                  ├─ INSERT new ─▶│
  │                  │                  │  graph_memories│
  │                  │                  │               │
  │                  │                  ├─ INSERT ─────▶│
  │                  │                  │  lineage      │
  │                  │                  │  (SUPERSEDES)  │
  │                  │                  │               │
  │                  │                  │  (is_superseded│
  │                  │                  │   computed at  │
  │                  │                  │   read time)   │
  │                  │                  │               │
  │                  │◀─ new Memory ───┤               │
  │◀─ {new_id,      ─┤                  │               │
  │    old_id}       │                  │               │
```

### Context Injection Flow

```
Chat Loop      Agent              GraphMemoryStore    Session
  │              │                      │               │
  ├─ new turn ──▶│                      │               │
  │              ├─ extract topic       │               │
  │              │  from last user msg  │               │
  │              │                      │               │
  │              ├─ ContextFor() ──────▶│               │
  │              │  {agent_name,        ├─ GetEntity    │
  │              │   topic,             ├─ ListEdgesFrom│
  │              │   max_tokens}        ├─ ListEdgesTo  │
  │              │                      ├─ Recall       │
  │              │                      ├─ Token budget │
  │              │                      │  truncation   │
  │              │◀─ EntityRecall ──────┤               │
  │              │                      │               │
  │              ├─ Format() ──────────▶│               │
  │              │  "[Graph Memory      │               │
  │              │   Context]\n..."     │               │
  │              │                      │               │
  │              ├─ inject system msg ──┼──────────────▶│
  │              │                      │               │
  │◀─ continue ──┤                      │               │
```


## Data Structures

### Database Schema (SQLite)

```
┌───────────────────┐     ┌───────────────────┐     ┌─────────────────────┐
│  graph_entities   │     │   graph_edges      │     │  graph_memories     │
├───────────────────┤     ├───────────────────┤     ├─────────────────────┤
│ id (PK)           │◀────│ source_id (FK)    │     │ id (PK)             │
│ agent_id          │◀────│ target_id (FK)    │     │ agent_id            │
│ name (UNIQUE/aid) │     │ agent_id          │     │ content             │
│ entity_type       │     │ relation          │     │ summary             │
│ properties_json   │     │ properties_json   │     │ memory_type         │
│ owner             │     │ UNIQUE(s,t,rel)   │     │ source              │
│ user_id           │     │ user_id           │     │ source_id           │
│ created_at        │     │ created_at        │     │ owner               │
│ updated_at        │     │ updated_at        │     │ memory_agent_id     │
│ deleted_at        │     │ deleted_at        │     │ tags (JSON array)   │
└────────┬──────────┘     └───────────────────┘     │ salience            │
         │                                           │ token_count         │
         │                                           │ summary_token_count │
         │                                           │ access_count        │
         │                                           │ properties_json     │
         │                                           │ user_id             │
         │                                           │ created_at          │
         │         ┌───────────────────┐             │ accessed_at         │
         │         │ graph_memory_     │             │ expires_at          │
         └────────▶│ entities          │◀────────────│ deleted_at          │
                   │                   │             │ (NO updated_at!)    │
                   │                   │             │ (is_superseded      │
                   ├───────────────────┤             │  computed at read   │
                   │ memory_id (FK)    │             │  time via lineage)  │
                   │ entity_id (FK)    │             └────────┬────────────┘
                   │ role              │                      │
                   │ PK(mem,ent,role)  │     ┌───────────────────────┐
                   └───────────────────┘     │ graph_memory_lineage  │
                                             ├───────────────────────┤
                                             │ new_memory_id (FK)    │
                                             │ old_memory_id (FK)    │
                                             │ relation_type         │
                                             │ created_at            │
                                             │ PK(new,old)           │
                                             └───────────────────────┘
```

**FTS5 Virtual Tables**:
- `graph_memories_fts`: content, summary, tags (INSERT + DELETE triggers; no UPDATE trigger since content is immutable)
- `graph_entities_fts`: name, entity_type, properties_json (INSERT + UPDATE + DELETE triggers)


### Go Domain Types

**Key types in `pkg/memory/types.go`**:

| Type | Purpose | Mutability |
|------|---------|------------|
| `Entity` | Graph node | Mutable (name, type, properties) |
| `Edge` | Directed relationship | Mutable (properties); upsert on (source, target, relation) |
| `Memory` | Episodic record | Immutable content; mutable accessed_at, salience, expires_at |
| `MemoryLineage` | SUPERSEDES/CONSOLIDATES chain | Immutable |
| `ScoredMemory` | Memory + computed ranking scores | Read-only (computed at query time) |
| `EntityRecall` | Composite context query result | Read-only (entity + edges + memories + token usage) |
| `GraphStats` | Entity/edge/memory counts | Read-only snapshot |

**Key types in `pkg/memory/budget.go`**:

| Type | Purpose | Fields |
|------|---------|--------|
| `BudgetConfig` | Phased token budget for `ContextFor` | MaxTokens, EntityProfileBudget (default 200), GraphBudget (default 300) |


## Algorithms

### Salience Scoring

**Problem**: Rank memories by importance for retrieval, balancing recency, access frequency, and initial importance.

**Approach**: Three-factor salience model:
1. **Base salience**: Set at creation (0.0-1.0), representing inherent importance
2. **Time decay**: `salience *= decay_rate^days_elapsed` (default 0.995/day)
3. **Access boost**: `salience = min(1.0, salience + boost_amount)` per access (default 0.05)

**Complexity**: O(1) per memory for scoring; O(n) for DecayAll batch

**Recommended salience levels**:
- Critical decisions: 0.8-1.0
- Important facts: 0.5-0.7
- Casual observations: 0.3-0.5

### Token Budgeting

**Problem**: Prevent graph memory context injection from overflowing the LLM context window.

**Approach**: Two-level budget system:

**Level 1 -- Agent-level budget** (`graphMemoryTokenBudget()` in `pkg/agent/agent.go`):
1. If `max_context_tokens > 0`, use it directly
2. Otherwise: `context_window * context_budget_percent / 100` (default: 200K * 10% = 20K tokens)

**Level 2 -- Phased allocation** (`ContextFor()` in `pkg/storage/sqlite/graph_memory_store.go`, using `BudgetConfig` from `pkg/memory/budget.go`):
1. **Phase 1**: Reserve 200 tokens for entity profile
2. **Phase 2**: Reserve 300 tokens for graph neighborhood (1-hop edges)
3. **Phase 3**: Remaining budget for ranked memories via `AllocateMemoryBudget()`:
   - If full content fits within budget, include content
   - Elif summary fits, include summary (set `UsedSummary=true`)
   - Else skip the memory

**Complexity**: O(n) where n = candidate memories

### Graph Traversal

**Problem**: Find entity neighborhood for context building (edges and connected entities).

**Approach**: Recursive CTEs in SQL with configurable depth and cycle prevention:
```sql
WITH RECURSIVE neighbor_walk AS (
    SELECT source_id, target_id, relation, 1 AS depth
    FROM graph_edges WHERE source_id = ?
    UNION ALL
    SELECT e.source_id, e.target_id, e.relation, nw.depth + 1
    FROM graph_edges e JOIN neighbor_walk nw ON e.source_id = nw.target_id
    WHERE nw.depth < ?  -- max depth
)
SELECT DISTINCT * FROM neighbor_walk
```

**Complexity**: O(b^d) where b = average branching factor, d = max depth (typically d=1 or 2)


## Design Trade-offs

### Decision 1: Immutable Memories vs. Mutable Records

**Chosen**: Immutable memory content with SUPERSEDES/CONSOLIDATES lineage

**Rationale**:
- Historical record is never lost -- corrections create new memories, not overwrites
- Lineage chains enable auditing ("why did the agent change its mind?")
- Append-only writes are simpler for concurrent access

**Alternatives**:
1. **Mutable memories** (UPDATE content): Simpler, but loses history. Rejected: audit trail is critical for agent trust.
2. **Versioned rows** (version column + history table): More complex schema, similar benefits. Rejected: lineage table is simpler and more explicit.

### Decision 2: FTS5 Search vs. Vector Embeddings

**Chosen**: FTS5 with Porter stemming for memory and entity search

**Rationale**:
- Zero external dependencies (FTS5 built into SQLite)
- Sub-millisecond search latency
- Sufficient for keyword-based memory retrieval in agent workflows

**Alternatives**:
1. **Vector embeddings + cosine similarity**: Higher semantic accuracy. Rejected for now: requires embedding model, adds 100-300ms latency, external dependency.
2. **BM25 + LLM reranking** (used in conversation_memory): Better semantic matching. Considered for future: adds LLM cost per recall.

### Decision 3: Agent-Scoped Isolation vs. Shared Knowledge Graph

**Chosen**: Per-agent knowledge graph (agent_id scoping on all tables)

**Rationale**:
- Prevents cross-agent memory contamination
- Each agent builds domain-specific knowledge
- Simpler authorization model

**Alternatives**:
1. **Shared graph with ACLs**: Enables knowledge sharing but adds complexity. Considered for future.
2. **Global graph with read-only cross-agent access**: Middle ground. Deferred.

### Decision 4: Opt-Out Default vs. Opt-In

**Chosen**: Enabled by default when graph memory store is available

**Rationale**:
- Agents benefit from persistent memory without explicit configuration
- Users who don't want it can set `enabled: false`
- Aligns with progressive enhancement philosophy

**Implementation**: If `graph_memory` config section exists but `enabled` is not specified, defaults to `true`. Explicit `enabled: false` required to disable.


## Constraints and Limitations

### Constraint 1: FTS5 Build Tag Required

**Description**: Graph memory uses FTS5 virtual tables; builds must include `-tags fts5`

**Impact**: Standard `go test` without the tag will fail; must use `just test` or `go test -tags fts5 -race ./...`

### Constraint 2: Agent-Scoped Only

**Description**: Knowledge graphs are isolated per `agent_id`. No cross-agent memory sharing.

**Impact**: Two agents cannot share knowledge unless explicitly copied. This is by design for isolation.

### Constraint 3: Keyword Search Only

**Description**: FTS5 provides keyword-based search, not semantic/conceptual matching.

**Impact**: Queries like "database optimization" will not find memories about "SQL tuning" unless those exact words appear. Summaries and tags help mitigate this.

### Constraint 4: Token Budget Approximation

**Description**: Token counts are estimated, not exact. Budget enforcement is best-effort.

**Impact**: Injected context may slightly exceed the configured budget. The system errs on the side of including fewer memories.


## Performance Characteristics

### Latency

| Operation | Typical | Notes |
|-----------|---------|-------|
| Remember (single) | 2-5ms | INSERT + FTS trigger + junction table |
| Recall (FTS) | 1-10ms | Depends on index size and query complexity |
| ContextFor | 5-20ms | Entity lookup + 1-hop edges (ListEdgesFrom/To) + recall + token budgeting |
| Forget | 1-2ms | UPDATE expires_at |
| Supersede | 3-8ms | SELECT old salience + INSERT new + INSERT lineage (no UPDATE to old) |
| Consolidate | 5-15ms | INSERT new + N lineage rows + N salience decays |
| Relate | 2-5ms | Entity lookup/create + edge upsert |
| DecayAll | 10-50ms | Batch UPDATE on all active memories |
| Context injection (per turn) | 5-25ms | Topic extraction + ContextFor + format |

### Storage

| Table | Row Size (approx) | Growth |
|-------|-------------------|--------|
| graph_entities | 200-500 bytes | Slow (bounded by domain) |
| graph_edges | 150-300 bytes | Moderate |
| graph_memories | 500-5000 bytes | Fastest (append-only) |
| FTS5 indexes | ~10% of source data | Proportional |


## Concurrency Model

**Agent-level serialization**: Each agent runs a single conversation loop. Graph memory operations are called sequentially within a turn (context injection, then tool calls). No concurrent writes to the same agent's graph from multiple goroutines.

**Store-level thread safety**: The SQLite store uses the existing connection pool with WAL mode. (PostgreSQL graph memory store is not yet implemented; only the migration SQL exists.)

**Race detection**: All graph memory tests run with `-race` flag. Zero race conditions.


## Configuration

```yaml
agent:
  memory:
    type: sqlite
    graph_memory:
      enabled: true                    # opt-out (default: true if section exists)
      context_budget_percent: 10       # % of context window for injection (default: 10)
      max_context_tokens: 0            # absolute override (0 = use percentage)
      decay_rate: 0.995                # salience decay per day (default: 0.995)
      boost_amount: 0.05               # salience boost per access (default: 0.05)
      min_salience_threshold: 0.1      # recall filter threshold (default: 0.1)
      max_recall_candidates: 50        # max memories to consider (default: 50)
      default_salience: 0.5            # new memory salience (default: 0.5)
```

**Proto definition**: `loomv1.GraphMemoryConfig` in `proto/loom/v1/agent_config.proto` (field 8 of `MemoryConfig`). The graph memory gRPC service is in `proto/loom/v1/graph_memory.proto`.

**Wiring**: Server checks if storage backend implements `GraphMemoryProvider`, then calls `WithGraphMemoryStore(store, config)` during agent initialization.


## Related Work

### Memory Systems for LLM Agents

1. **MemGPT** (Packer et al., 2023): Virtual context management with main/archival memory tiers. Graph memory differs by adding entity-relationship structure and salience-based ranking rather than pure recency.

2. **Mem0** (formerly EmbedChain Memory): Graph-based memory for AI agents with entity extraction and relationship tracking. Similar entity-edge-memory architecture; Loom's graph memory adds salience decay, lineage tracking, and token budgeting.

3. **LangGraph Memory**: Checkpointed state with cross-thread persistence. Graph memory provides finer-grained control with per-memory salience and explicit knowledge graph structure.

### Knowledge Graphs

4. **Property Graph Model** (Rodriguez & Neubauer, 2010): Entities with typed properties and directed labeled edges. Graph memory follows this model with the addition of immutable episodic memories bridged to entities.

### Salience and Memory Decay

5. **Ebbinghaus Forgetting Curve** (1885): Memory retention decays exponentially over time. The `decay_rate` parameter models this phenomenon, with `access_boost` implementing the spacing effect (retrieval practice strengthens memory).


## References

1. Packer, C., et al. (2023). *MemGPT: Towards LLMs as Operating Systems*. arXiv:2310.08560.
2. Rodriguez, M. A., & Neubauer, P. (2010). *Constructions from Dots and Lines*. Bulletin of the American Society for Information Science and Technology.
3. Ebbinghaus, H. (1885). *Memory: A Contribution to Experimental Psychology*.


## Further Reading

### Architecture
- [Memory Systems Architecture](memory-systems.md) -- 5-layer segmented memory (ROM/Kernel/L1/L2/Swap)
- [Agent System Architecture](agent-system-design.md) -- Agent runtime and conversation loop
- [Agent Private Memory](agent-private-memory.md) -- AGENT namespace isolation

### Reference
- [Agent Configuration](../reference/agent-configuration.md) -- YAML config options including graph_memory
- [Tool Registry](../reference/tool-registry.md) -- How tools are registered and discovered

### Source Files
- `proto/loom/v1/graph_memory.proto` -- Proto definitions (gRPC service + messages)
- `proto/loom/v1/agent_config.proto` -- `GraphMemoryConfig` message (field 8 of `MemoryConfig`)
- `pkg/memory/store.go` -- `GraphMemoryStore` interface + `RecallOpts`, `ContextForOpts`
- `pkg/memory/types.go` -- Domain types (`Entity`, `Edge`, `Memory`, `ScoredMemory`, `EntityRecall`, `GraphStats`)
- `pkg/memory/salience.go` -- Salience engine (`ComputeSalience`, `BoostSalience`, `RankBySalience`, `CombineScores`)
- `pkg/memory/budget.go` -- Token budget phased allocation (`BudgetConfig`, `AllocateMemoryBudget`)
- `pkg/agent/graph_memory_tool.go` -- Agent tool (8 actions)
- `pkg/agent/agent.go` -- `injectGraphMemoryContext()`, `graphMemoryPromptSupplement()`, `checkAndRegisterGraphMemoryTool()`
- `pkg/storage/backend/backend.go` -- `GraphMemoryProvider` interface
- `pkg/storage/sqlite/graph_memory_store.go` -- SQLite implementation
- `pkg/storage/sqlite/migrations/000002_graph_memory.up.sql` -- SQLite schema
- `pkg/storage/postgres/migrations/000010_graph_memory.up.sql` -- PostgreSQL migration (SQL only, no Go store)
- `test/e2e/graph_memory_e2e_test.go` -- E2E tests
