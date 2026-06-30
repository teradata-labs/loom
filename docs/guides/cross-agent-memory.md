---
title: "Cross-Agent Memory Sharing"
weight: 18
---

# Cross-Agent Memory Sharing

This guide explains how to share graph memory between Loom agents and external agents (such as Hermes, custom Python agents, or any gRPC/HTTP client) using the `GraphMemoryService` gRPC API.

**Status:** ✅ Available (since v1.2.0)

---

## Overview

Loom's graph memory uses an **agent_id scoping model**:

- **Entities** are owned by the agent that created them (scoped by `agent_id`).
- **Memories** (immutable episodic records) are owned by the agent that wrote them (scoped by `agent_id`).
- **Edges** (relationships between entities) can cross `agent_id` boundaries. The `Neighbors` traversal follows edges by entity ID without filtering by `agent_id`.

This means two agents can each maintain their own knowledge while sharing a relationship graph that connects them.

```
+---------------------------+       +---------------------------+
|   Loom Agent: td-analyst  |       | External: hermes-research |
|                           |       |                           |
|  Entities:                |       |  Entities:                |
|    - "Teradata"           |       |    - "Query Rewrite"      |
|    - "FastLoad"           |       |    - "Cost Model"         |
|                           |       |                           |
|  Memories:                |       |  Memories:                |
|    - "FastLoad best for   |       |    - "Rewrite rules cut   |
|       bulk inserts"       |       |       query time by 40%"  |
+---------------------------+       +---------------------------+
            |                                    |
            |  EDGE: "FastLoad" --[USES]--> "Query Rewrite"
            |  EDGE: "Cost Model" --[APPLIES_TO]--> "Teradata"
            |                                    |
            +-------- Shared Edge Graph ---------+
```

When `td-analyst` calls `Neighbors` on "FastLoad", it traverses the `USES` edge and discovers "Query Rewrite" -- an entity owned by `hermes-research`. The graph connects knowledge across ownership boundaries.

---

## Prerequisites

1. **Loom server running** with the graph memory store initialized. The `GraphMemoryService` is registered automatically when the storage backend provides a `GraphMemoryStore` (both SQLite and Postgres backends do).

2. **External agent** with a gRPC client targeting Loom's gRPC port (default `:60051`).

3. **Agreed-upon agent_id values** -- each participant uses a stable, unique `agent_id` string (e.g., `"td-analyst"`, `"hermes-research"`).

> **Note:** `GraphMemoryService` is reachable over gRPC only. The HTTP gateway (`pkg/server/http.go`) registers `LoomService` handlers exclusively, and `proto/loom/v1/graph_memory.proto` defines no `google.api.http` annotations, so there are no REST/JSON routes for `GraphMemoryService`. Use `grpcurl` (or a generated gRPC client) against the gRPC port for all examples below.

---

## Access Patterns

### 1. Read Another Agent's Context

The simplest integration: an external agent enriches its own prompts with a Loom agent's knowledge.

Call `ContextFor` with the Loom agent's `agent_id`:

```bash
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "entity_name": "Teradata",
  "topic": "bulk loading performance",
  "max_tokens": 4000
}' localhost:60051 loom.v1.GraphMemoryService/ContextFor
```

Response contains the same pre-formatted context block the Loom agent sees internally:

```json
{
  "recall": {
    "entity": {
      "id": "ent_abc123",
      "agentId": "td-analyst",
      "name": "Teradata",
      "entityType": "platform",
      "propertiesJson": "{\"version\":\"17.20\"}"
    },
    "memories": [
      {
        "memory": {
          "id": "mem_def456",
          "agentId": "td-analyst",
          "content": "FastLoad achieves 100K rows/sec on wide tables when sessions >= 8",
          "memoryType": "experience",
          "salience": 0.8,
          "tags": ["performance", "fastload"]
        },
        "computedSalience": 0.76,
        "relevanceScore": 0.92,
        "combinedScore": 0.70
      }
    ],
    "edgesOut": [
      {
        "id": "edge_001",
        "agentId": "td-analyst",
        "sourceId": "ent_abc123",
        "targetId": "ent_xyz789",
        "relation": "USES"
      }
    ],
    "totalTokensUsed": 312,
    "totalCandidates": 5
  }
}
```

The external agent can inject this into its prompt to gain the Loom agent's knowledge about a topic.

---

### 2. Write Learnings Back

After the external agent produces findings, persist them under its own `agent_id`:

```bash
grpcurl -plaintext -d '{
  "agent_id": "hermes-research",
  "content": "Query rewrite rules that push predicates below JOIN reduce scan by 40% on star schemas",
  "summary": "Predicate pushdown reduces star schema scans by 40%",
  "memory_type": "experience",
  "source": "agent",
  "owner": "hermes",
  "tags": ["optimization", "query-rewrite", "star-schema"],
  "salience": 0.7,
  "entity_ids": ["ent_qr_001"]
}' localhost:60051 loom.v1.GraphMemoryService/Remember
```

This memory is owned by `hermes-research`. The Loom agent `td-analyst` will not see it in its own `Recall` or `ContextFor` calls (those are scoped to `agent_id`). Cross-agent visibility requires explicit edges.

---

### 3. Link Knowledge Across Agents

Create a cross-agent edge to connect knowledge from different agents:

```bash
# First, create the entity in hermes-research's namespace
grpcurl -plaintext -d '{
  "agent_id": "hermes-research",
  "name": "Query Rewrite",
  "entity_type": "technique",
  "owner": "hermes"
}' localhost:60051 loom.v1.GraphMemoryService/CreateEntity

# Then create the cross-agent edge
# The Relate RPC uses entity names (resolved within the agent_id's namespace)
# For cross-agent edges, pass the agent_id of the edge owner
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "source_name": "FastLoad",
  "target_name": "Query Rewrite",
  "relation": "BENEFITS_FROM",
  "properties_json": "{\"context\":\"bulk load optimization\"}"
}' localhost:60051 loom.v1.GraphMemoryService/Relate
```

> **Important:** The `Relate` RPC resolves `source_name` and `target_name` within the specified `agent_id`'s entity namespace. For cross-agent edges, one approach is to create a "shadow" entity reference in the source agent's namespace, or use entity IDs directly if your implementation supports it. The edge's `agent_id` field indicates who owns (and can delete) the edge.

---

### 4. Bidirectional Recall via Neighbors

Once edges connect entities across agents, `Neighbors` traversal surfaces them:

```bash
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "entity_name": "FastLoad",
  "direction": "NEIGHBOR_DIRECTION_BOTH",
  "depth": 2
}' localhost:60051 loom.v1.GraphMemoryService/Neighbors
```

Response includes edges that cross into other agents' entity namespaces:

```json
{
  "edges": [
    {
      "id": "edge_cross_001",
      "agentId": "td-analyst",
      "sourceId": "ent_fastload",
      "targetId": "ent_qr_hermes",
      "relation": "BENEFITS_FROM"
    }
  ],
  "entities": [
    {
      "id": "ent_qr_hermes",
      "agentId": "hermes-research",
      "name": "Query Rewrite",
      "entityType": "technique"
    }
  ]
}
```

The Loom agent searching for "FastLoad" neighbors discovers "Query Rewrite" owned by `hermes-research`. The `Neighbors` query traverses by entity ID without filtering by `agent_id` -- this is the mechanism that enables cross-agent knowledge discovery.

---

## Implementation Walkthrough

Step-by-step integration for an external agent (using Hermes as an example):

### Step 1: Connect to Loom's gRPC Endpoint

From a Go client:

```go
import (
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

conn, err := grpc.NewClient("localhost:60051",
    grpc.WithTransportCredentials(insecure.NewCredentials()),
)
if err != nil {
    log.Fatalf("connect to loom: %v", err)
}
defer conn.Close()

client := loomv1.NewGraphMemoryServiceClient(conn)
```

From Python (using `grpcio`):

```python
import grpc
from loom.v1 import graph_memory_pb2, graph_memory_pb2_grpc

channel = grpc.insecure_channel("localhost:60051")
client = graph_memory_pb2_grpc.GraphMemoryServiceStub(channel)
```

### Step 2: Enrich Prompts with Loom Agent Knowledge

On each turn of the external agent, call `ContextFor` to retrieve relevant knowledge:

```bash
# Get td-analyst's knowledge about a topic relevant to the current task
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "entity_name": "Teradata",
  "topic": "loading strategies",
  "max_tokens": 4000
}' localhost:60051 loom.v1.GraphMemoryService/ContextFor
```

Or use `Recall` for a broader memory search without entity scoping:

```bash
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "query": "performance optimization patterns",
  "min_salience": 0.3,
  "max_tokens": 3000,
  "limit": 20
}' localhost:60051 loom.v1.GraphMemoryService/Recall
```

Inject the returned memories into the external agent's system prompt or context window.

### Step 3: Persist External Agent Findings

After the external agent produces analysis or learns something new:

```bash
# Create an entity for a concept the external agent discovered
grpcurl -plaintext -d '{
  "agent_id": "hermes-research",
  "name": "Predicate Pushdown",
  "entity_type": "technique",
  "properties_json": "{\"applies_to\":\"star_schema\",\"improvement\":\"40%\"}",
  "owner": "hermes"
}' localhost:60051 loom.v1.GraphMemoryService/CreateEntity

# Store the memory linked to the entity
grpcurl -plaintext -d '{
  "agent_id": "hermes-research",
  "content": "Pushing filter predicates below JOIN operators on star schemas reduces full-table scans by approximately 40%. Most effective when dimension tables are small relative to the fact table.",
  "summary": "Predicate pushdown reduces star schema scans by 40%",
  "memory_type": "experience",
  "source": "agent",
  "source_id": "hermes-session-2026-05-12",
  "owner": "hermes",
  "memory_agent_id": "hermes-research",
  "tags": ["optimization", "predicate-pushdown", "star-schema"],
  "salience": 0.75,
  "entity_ids": ["<entity_id_from_create_response>"]
}' localhost:60051 loom.v1.GraphMemoryService/Remember
```

### Step 4: Run a Linker Pass

Periodically, discover entities across agents that share semantics and create edges:

```bash
# List entities from the Loom agent
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "limit": 100
}' localhost:60051 loom.v1.GraphMemoryService/ListEntities

# List entities from the external agent
grpcurl -plaintext -d '{
  "agent_id": "hermes-research",
  "limit": 100
}' localhost:60051 loom.v1.GraphMemoryService/ListEntities

# For each pair that shares semantics, create a relationship
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "source_name": "Query Optimization",
  "target_name": "Predicate Pushdown",
  "relation": "INCLUDES",
  "properties_json": "{\"linked_by\":\"linker\",\"confidence\":0.85}"
}' localhost:60051 loom.v1.GraphMemoryService/Relate
```

In practice, the linker pass can be automated: iterate both entity lists, compute semantic similarity (via embeddings or keyword overlap), and create `CORROBORATES`, `INCLUDES`, or `CONTRADICTS` edges above a confidence threshold.

### Step 5: Monitor Graph Health

Check stats to understand the graph's size and composition:

```bash
grpcurl -plaintext -d '{
  "agent_id": "td-analyst"
}' localhost:60051 loom.v1.GraphMemoryService/GetGraphStats
```

Response:

```json
{
  "stats": {
    "entityCount": 42,
    "edgeCount": 87,
    "memoryCount": 156,
    "activeMemoryCount": 148,
    "totalMemoryTokens": 23400,
    "memoriesByType": {
      "experience": 67,
      "fact": 45,
      "decision": 22,
      "observation": 14
    }
  }
}
```

---

## HTTP/REST Access

📋 **Not available.** `GraphMemoryService` has no HTTP/REST surface. The HTTP gateway in `pkg/server/http.go` registers only `LoomService` (`RegisterLoomServiceHandlerFromEndpoint`), and `proto/loom/v1/graph_memory.proto` declares no `google.api.http` annotations. Requests to `http://localhost:5006/loom.v1.GraphMemoryService/<RPC>` are handled by the gRPC-gateway mux, which has no route for them and returns a 404-style error.

To call these RPCs from a non-Go client, use a gRPC client (e.g., `grpcurl`, or generated stubs in Python/Java/etc.) against the gRPC port. See the [Access Patterns](#access-patterns) and [Implementation Walkthrough](#implementation-walkthrough) sections above for `grpcurl` examples.

---

## Security Considerations

**`agent_id` is the only access boundary today.** There is no per-agent authentication or authorization token. Any client that can reach the gRPC/HTTP port can read or write any `agent_id`'s data.

Implications:

| Scenario | Mitigation |
|----------|-----------|
| Multi-tenant deployment | Use network isolation (separate Loom instances per tenant) or deploy behind an API gateway that enforces tenant-to-agent_id mapping |
| Untrusted external agents | Run behind a reverse proxy that restricts which `agent_id` values a given client IP can access |
| Postgres backend | Consider Row-Level Security (RLS) policies keyed on `agent_id` to enforce access at the database layer |
| Sensitive memories | Do not store secrets or PII in graph memory content. The system has no encryption-at-rest for memory content |

If your deployment requires per-agent authentication, implement it at the gRPC interceptor layer or via a sidecar proxy (e.g., Envoy with external auth).

---

## Integration Patterns

### Pattern 1: Learnings Aggregator

One coordinator agent reads from N specialist agents to synthesize knowledge.

```
                    +-------------------+
                    |   Coordinator     |
                    | (reads all agents)|
                    +-------------------+
                   /         |          \
    ContextFor    /   ContextFor   \    ContextFor
                 /           |            \
    +-----------+    +-----------+    +-----------+
    | Agent A   |    | Agent B   |    | Agent C   |
    | (SQL)     |    | (Python)  |    | (DevOps)  |
    +-----------+    +-----------+    +-----------+
```

The coordinator calls `ContextFor` or `Recall` against each specialist's `agent_id`, merges the results, and presents a unified answer. It writes its own synthesized memories under its own `agent_id`.

### Pattern 2: Specialist Network

Each agent writes to its own namespace. A background process traverses edges across all agents to surface connections.

```bash
# Background linker (runs periodically)
for agent in td-analyst hermes-research devops-agent; do
  grpcurl -plaintext -d "{\"agent_id\":\"$agent\",\"limit\":200}" \
    localhost:60051 loom.v1.GraphMemoryService/ListEntities
done
# Compare entities across agents, create Relate edges for shared concepts
```

Agents discover each other's knowledge organically through `Neighbors` traversal without needing explicit cross-agent `Recall` calls.

### Pattern 3: Feedback Loop

An external reviewer agent writes corrections that the Loom agent picks up via the salience system.

```bash
# External agent identifies an error in td-analyst's memory
# Step 1: Supersede the incorrect memory with a correction
grpcurl -plaintext -d '{
  "agent_id": "td-analyst",
  "old_memory_id": "mem_incorrect_123",
  "new_content": "FastLoad sessions should be limited to 20 (not 64) to avoid AMPs contention on systems with fewer than 32 AMPs",
  "new_summary": "FastLoad: max 20 sessions on <32 AMP systems",
  "new_tags": ["fastload", "correction", "amps"]
}' localhost:60051 loom.v1.GraphMemoryService/Supersede
```

The old memory is marked `is_superseded=true` and deprioritized in future `Recall` calls. The new memory carries higher salience by default, ensuring the Loom agent sees the correction on its next relevant query.

For cases where the external agent cannot supersede (lacks the `old_memory_id`), it can write a high-salience memory with a `CONTRADICTS` edge:

```bash
# Write the correction under the external agent's namespace
grpcurl -plaintext -d '{
  "agent_id": "reviewer-agent",
  "content": "Correction: FastLoad on <32 AMP systems should use max 20 sessions, not 64",
  "memory_type": "fact",
  "salience": 0.9,
  "tags": ["correction", "fastload"]
}' localhost:60051 loom.v1.GraphMemoryService/Remember

# Link it as a contradiction
grpcurl -plaintext -d '{
  "agent_id": "reviewer-agent",
  "source_name": "FastLoad Correction",
  "target_name": "FastLoad",
  "relation": "CONTRADICTS"
}' localhost:60051 loom.v1.GraphMemoryService/Relate
```

The Loom agent will discover this contradiction through `Neighbors` traversal and can reconcile it.

---

## Troubleshooting

### GraphMemoryService not available

If `grpcurl` returns `unknown service loom.v1.GraphMemoryService`:

1. Verify the Loom server log contains `"Graph memory service registered"`.
2. If missing, check that the storage backend implements `GraphMemoryProvider`. Both SQLite and Postgres backends do this when migrations are up to date.
3. Ensure database migrations are current. The graph memory schema is migration `000002_graph_memory` (SQLite) / `000010_graph_memory` (Postgres).

### Entity not found errors

`ContextFor` returns an empty recall (not an error) when the entity does not exist. If you expect data but get empty results:

1. Verify the entity exists: call `GetEntity` with the exact `agent_id` and `name`.
2. Entity names are case-sensitive.
3. Remember that entities are scoped by `agent_id` -- you cannot `GetEntity` for `agent_id="hermes"` if the entity was created under `agent_id="hermes-research"`.

### Cross-agent edges not appearing in Neighbors

1. Verify the edge was created successfully (check the `Relate` response).
2. `Neighbors` starts from an entity ID. If the starting entity belongs to agent A and the edge connects to agent B's entity, traversal works. But you must start from a known entity.
3. Check the `direction` parameter: `NEIGHBOR_DIRECTION_OUTBOUND` only follows edges where the starting entity is the source.

### Memory not appearing in Recall

1. `Recall` is scoped by `agent_id`. You can only recall memories owned by the specified agent.
2. Check `min_salience` -- memories with salience below this threshold are filtered out (default 0.1).
3. The FTS query must match the memory content. Try a broader query or empty string for all memories.

---

## Next Steps

- [Memory Management Guide](./memory-management.md) -- how Loom's layered memory system works
- [Task Board Guide](./task-board.md) -- dependency-aware task tracking
- [Graph Memory Architecture](/docs/architecture/graph-memory/) -- internal design and data model
