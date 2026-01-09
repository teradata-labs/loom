# Architecture Documentation Standards

## Purpose

The `architecture/` directory contains **system design documentation** for architects, academics, and developers understanding Loom's internal design.

**Target Audience**:
1. **Architects** - Understanding system design and trade-offs
2. **Academics** - Research into LLM agent architectures
3. **Developers** - Deep understanding for advanced extensions

**Not For**: End users seeking how-to guides (those belong in `/guides/`) or API specifications (those belong in `/reference/`).

---

## Critical Rules

### 1. Academic Rigor

Architecture docs are **theoretical and conceptual**:
- **"Why was this designed this way?"** (design rationale, alternatives considered)
- **"What are the fundamental properties?"** (invariants, guarantees, constraints)
- **"How do components interact?"** (interfaces, protocols, data flow)
- **"What are the trade-offs?"** (performance, complexity, maintainability)
- **"What are the theoretical foundations?"** (algorithms, data structures, patterns)

Architecture docs do NOT answer:
- ❌ "How do I configure X?" → That's a reference doc
- ❌ "How do I accomplish task Y?" → That's a guide
- ❌ "What are all the options?" → That's a reference doc

### 2. Design Rationale Over Implementation Details

Focus on **why**, not **how**:
- ✅ "Memory is segmented into ROM/Kernel/L1/L2 layers to balance context window usage with conversation continuity"
- ✅ "The tool system uses goroutines for concurrent execution with shared error channels"
- ✅ "Pattern matching uses TF-IDF cosine similarity to rank domain knowledge relevance"
- ❌ "Call agent.NewAgent() with backend and LLM provider" (that's reference/guide content)
- ❌ "Run `looms serve --port 50051`" (that's guide content)

### 3. Block Diagrams Are Mandatory

Every architecture document MUST include visual diagrams:

**Preferred Format**: ASCII block diagrams (you are not great with mermaid)

**Diagram Requirements**:
1. **System Context Diagram** - Show the system in its environment
2. **Component Diagram** - Show major subsystems and their relationships
3. **Sequence Diagram** - Show key interaction flows (where applicable)
4. **Data Flow Diagram** - Show how information moves through the system

**ASCII Diagram Style Guide**:

```
┌─────────────────────────────────────────────────┐
│                  Title/Component                │
│  ┌──────────────┐         ┌──────────────┐     │
│  │ Subcomponent │────────▶│ Subcomponent │     │
│  └──────────────┘         └──────────────┘     │
│         │                         │             │
│         │                         │             │
│         ▼                         ▼             │
│  ┌──────────────┐         ┌──────────────┐     │
│  │ Subcomponent │◀────────│ Subcomponent │     │
│  └──────────────┘         └──────────────┘     │
└─────────────────────────────────────────────────┘
```

**Box Drawing Characters**:
- Horizontal: `─` (U+2500)
- Vertical: `│` (U+2502)
- Corners: `┌` `┐` `└` `┘` (U+250C, U+2510, U+2514, U+2518)
- T-junctions: `├` `┤` `┬` `┴` (U+251C, U+2524, U+252C, U+2534)
- Cross: `┼` (U+253C)
- Arrows: `▶` `◀` `▲` `▼` (U+25B6, U+25C0, U+25B2, U+25BC)
- Flow: `→` `←` `↑` `↓` (U+2192, U+2190, U+2191, U+2193)

**Diagram Conventions**:
- Use `─────▶` for synchronous calls
- Use `- - -▶` for asynchronous messages
- Use `◀─────` for return values/responses
- Use `[Component]` for external systems
- Use `(interface)` for abstract interfaces
- Use `{data}` for data structures

**Example - System Context Diagram**:
```
        ┌──────────────────────────────────────────┐
        │         External Environment             │
        │                                          │
        │  [User/Client] ───▶ [Loom Agent] ───▶   │
        │       │                  │               │
        │       │                  ├───▶ [LLM]     │
        │       │                  │               │
        │       │                  ├───▶ [Backend] │
        │       │                  │               │
        │       │                  └───▶ [Hawk]    │
        │       │                                  │
        │       ◀────────────── Response           │
        └──────────────────────────────────────────┘
```

**Example - Component Diagram**:
```
┌───────────────────────────────────────────────────────┐
│                    Agent Runtime                      │
│                                                       │
│  ┌──────────────┐                                    │
│  │   Memory     │─── ROM (read-only patterns)       │
│  │  Controller  │─── Kernel (system context)        │
│  │              │─── L1 (recent conversation)       │
│  │              │─── L2 (long-term summary)         │
│  └──────┬───────┘                                    │
│         │                                            │
│         ▼                                            │
│  ┌──────────────┐      ┌──────────────┐            │
│  │    Agent     │────▶ │  Shuttle     │            │
│  │    Core      │      │ (Tool Exec)  │            │
│  └──────┬───────┘      └──────┬───────┘            │
│         │                     │                     │
│         │                     │                     │
│         ▼                     ▼                     │
│  ┌──────────────┐      ┌──────────────┐            │
│  │   Pattern    │      │   Backend    │            │
│  │   Matcher    │      │  Interface   │            │
│  └──────────────┘      └──────────────┘            │
└───────────────────────────────────────────────────────┘
```

**Example - Sequence Diagram**:
```
Client         Agent          Pattern         LLM          Backend
  │              │              │             │              │
  ├─ Query ─────▶│              │             │              │
  │              ├─ Match ─────▶│             │              │
  │              │◀─ Pattern ───┤             │              │
  │              ├─────────── Build Prompt ──▶│              │
  │              │◀───────────── Response ────┤              │
  │              ├─ Execute SQL ──────────────┼─────────────▶│
  │              │◀────────────────── Result ─┴──────────────┤
  │              ├────────── Interpret Result ▶│              │
  │              │◀───────────── Response ────┤              │
  │◀─ Response ──┤              │             │              │
  │              │              │             │              │
```

### 4. Structure Requirements

Every architecture document MUST include:

1. **Overview** - What is this system? (1-2 paragraphs)
2. **Design Goals** - What properties does this system aim for?
3. **Architecture Diagram(s)** - Visual representation (ASCII)
4. **Components** - Description of major subsystems
5. **Interactions** - How components communicate
6. **Data Structures** - Key data structures and their invariants
7. **Algorithms** - Core algorithms and their complexity
8. **Trade-offs** - Design decisions and alternatives considered
9. **Constraints** - Limitations and boundaries
10. **Related Work** - Citations to papers, patterns, or prior art

**Standard Template**:

```markdown
---
title: "[System] Architecture"
weight: [number]
---

# [System] Architecture

Brief 2-3 sentence overview of what this system does and why it exists.

**Target Audience**: Architects, academics, advanced developers

---

## Design Goals

What properties does this system prioritize?

- **Goal 1**: Description and rationale
- **Goal 2**: Description and rationale
- **Goal 3**: Description and rationale

**Non-goals**: What this system explicitly does NOT try to achieve.

---

## System Context

[ASCII diagram showing system in environment]

**Description**: Explanation of external dependencies and interactions.

---

## Architecture Overview

[ASCII component diagram]

**Description**: High-level explanation of major components.

---

## Components

### Component 1

**Responsibility**: What this component does

**Interface**: How other components interact with it

**Implementation**: Key design decisions

**Invariants**: What is always true about this component

### Component 2

...

---

## Key Interactions

### Interaction Pattern 1

[ASCII sequence diagram]

**Description**: Detailed explanation of this interaction flow.

**Properties**:
- Synchronous/Asynchronous
- Error handling strategy
- Concurrency model

---

## Data Structures

### Structure 1

**Purpose**: Why this structure exists

**Schema**: Field definitions and types

**Invariants**: Constraints that are always maintained

**Operations**: How this structure is manipulated

---

## Algorithms

### Algorithm 1

**Problem**: What this algorithm solves

**Approach**: High-level strategy

**Complexity**: Time and space complexity

**Trade-offs**: Why this approach vs. alternatives

---

## Design Trade-offs

### Decision 1: [Brief description]

**Chosen Approach**: What we did

**Rationale**: Why we chose this

**Alternatives Considered**:
- Alternative 1: Why it was rejected
- Alternative 2: Why it was rejected

**Consequences**: What this decision implies for the system

---

## Constraints and Limitations

### Constraint 1

**Description**: What the limitation is

**Rationale**: Why this constraint exists

**Impact**: How this affects system behavior

**Workarounds**: How to work within this limitation

---

## Performance Characteristics

### Latency

**Typical**: Measurement under normal conditions

**Worst-case**: Measurement under stress

**Factors**: What affects performance

### Throughput

**Typical**: Measurement under normal conditions

**Scaling**: How performance changes with load

### Resource Usage

**Memory**: Typical memory consumption patterns

**CPU**: Typical CPU utilization

---

## Concurrency Model

**Threading**: How concurrency is achieved

**Synchronization**: What synchronization primitives are used

**Race Conditions**: How race conditions are prevented

**Deadlock Prevention**: Strategies to avoid deadlocks

---

## Error Handling Philosophy

**Strategy**: Overall approach to error handling

**Error Propagation**: How errors flow through the system

**Recovery**: What happens when errors occur

**Observability**: How errors are reported and traced

---

## Security Considerations

**Threat Model**: What threats this system faces

**Mitigations**: How threats are addressed

**Trust Boundaries**: Where trust is verified

**Sensitive Data**: How sensitive information is protected

---

## Evolution and Extensibility

**Extension Points**: Where the system can be extended

**Stability**: What is stable vs. what may change

**Migration**: How to evolve the system without breaking clients

---

## Related Work

### Pattern/System 1

**Reference**: Citation or link

**Relationship**: How this relates to our design

### Pattern/System 2

...

---

## Further Reading

- [Reference doc]: For API details
- [Guide]: For practical usage
- [Related architecture doc]: For related system design
```

### 5. No Marketing Speak (Same as All Docs)

Architecture docs are **academic and precise**. Never use:
- ❌ "powerful" / "robust" / "seamless"
- ❌ "production-ready" / "enterprise-grade"
- ❌ "revolutionary" / "cutting-edge"

Use **precise technical language**:
- ✅ "O(log n) lookup time via B-tree index"
- ✅ "Eventual consistency with 89-143ms convergence"
- ✅ "Crash-only design with automatic recovery"
- ✅ "Actor model concurrency with message passing"

### 6. Theoretical Foundations

When applicable, cite **formal foundations**:
- ✅ "Uses vector space model (Salton, 1975) for pattern matching"
- ✅ "Implements actor model (Hewitt, 1973) for tool execution"
- ✅ "Employs sliding window algorithm for memory management"
- ✅ "Based on chain-of-thought prompting (Wei et al., 2022)"

**Citation Format**:
```markdown
## References

1. Salton, G., Wong, A., & Yang, C. S. (1975). A vector space model for automatic indexing. *Communications of the ACM*, 18(11), 613-620.

2. Wei, J., Wang, X., Schuurmans, D., et al. (2022). Chain-of-thought prompting elicits reasoning in large language models. *NeurIPS 2022*.
```

### 7. Precision Over Brevity

Architecture docs can be **long and detailed**. Completeness matters more than conciseness.

**Example - TOO BRIEF**:
```markdown
## Memory System

Memory is divided into layers to manage context window size.
```

**Example - APPROPRIATE DEPTH**:
```markdown
## Memory Architecture

### Design Problem

LLM context windows are finite (200k tokens for Claude Sonnet 4.5). Long conversations exceed this limit. Naively truncating loses critical context. Sending full history every turn wastes tokens and increases latency.

### Solution: Segmented Memory Model

Memory is segmented into four layers, inspired by CPU cache hierarchies:

#### ROM (Read-Only Memory)
- **Content**: System prompt, pattern library, backend schema
- **Size**: ~5k tokens
- **Mutation**: Never changes during session
- **Rationale**: Core knowledge must always be present

#### Kernel
- **Content**: Session context, user identity, conversation goals
- **Size**: ~2k tokens
- **Mutation**: Updated only on explicit user requests
- **Rationale**: Stable context that frames the conversation

#### L1 (Recent Conversation)
- **Content**: Last N turns of conversation
- **Size**: ~10k tokens
- **Mutation**: FIFO sliding window
- **Rationale**: Recent history is most relevant for coherence

#### L2 (Summarized History)
- **Content**: Compressed summaries of older turns
- **Size**: ~3k tokens
- **Mutation**: Periodic summarization when L1 evicts
- **Rationale**: Long-term memory without full verbatim history

### Trade-offs

**Chosen**: Segmented model
- ✅ Predictable token usage (ROM + Kernel + L1 + L2 ≈ 20k)
- ✅ Coherent short-term conversation (L1 preserves recent context)
- ✅ Long-term awareness (L2 provides historical summary)
- ❌ Lossy compression (summaries drop detail)
- ❌ Complexity (four layers to manage)

**Alternative 1: Full history**
- ✅ Perfect recall
- ❌ Unbounded token growth
- ❌ High latency and cost

**Alternative 2: Fixed window**
- ✅ Simple implementation
- ❌ Loses all history beyond window
- ❌ Can't maintain long-term context

**Alternative 3: External memory (RAG)**
- ✅ Unbounded storage
- ❌ Retrieval adds latency
- ❌ Relevance matching is hard

### Formal Properties

**Invariant 1: Context Budget**
```
sizeof(ROM) + sizeof(Kernel) + sizeof(L1) + sizeof(L2) ≤ CONTEXT_WINDOW - OUTPUT_RESERVE
```

**Invariant 2: Temporal Ordering**
```
∀ msg ∈ L1: msg.timestamp > ∀ msg' ∈ L2: msg'.timestamp
```

**Invariant 3: ROM Immutability**
```
∀ t ∈ [session_start, session_end]: ROM(t) = ROM(session_start)
```
```

---

## Architecture Document Types

### 1. System Architecture (`[system]-architecture.md`)

**Purpose**: Deep dive into a major subsystem (agent, tool system, pattern system, etc.)

**Must include**:
- Design goals and non-goals
- System context diagram
- Component diagram
- Key data structures
- Core algorithms
- Design trade-offs
- Performance characteristics
- Concurrency model

**Example**: `agent-system.md`, `tool-system.md`

---

### 2. Cross-Cutting Concern (`[concern].md`)

**Purpose**: Architecture of a concern that spans multiple systems (observability, security, error handling)

**Must include**:
- Problem statement
- Architecture approach
- Integration points with other systems
- Trade-offs
- Performance impact

**Example**: `observability.md`, `error-handling.md`

---

### 3. Integration Architecture (`[integration]-integration.md`)

**Purpose**: How Loom integrates with external systems

**Must include**:
- Integration boundary diagram
- Protocol specifications
- Data flow
- Error handling
- Performance characteristics
- Security considerations

**Example**: `mcp-integration.md`, `promptio-integration.md`

---

### 4. Overview Architecture (`_index.md`)

**Purpose**: Overarching system architecture that ties everything together

**Must include**:
- High-level system diagram
- Major subsystems and their relationships
- Key design principles
- Technology stack rationale
- Links to detailed architecture docs

---

## Anti-Patterns (What NOT to Do)

### ❌ Implementation Details Instead of Design

**BAD**:
```markdown
## Agent Creation

Call `agent.NewAgent(backend, llm)` to create an agent. Pass the backend interface and LLM provider.
```

This is implementation, not architecture. Move to reference docs.

**GOOD**:
```markdown
## Agent Initialization Design

### Problem
Agents require coupling to both domain-specific backends (SQL databases, APIs) and LLM providers. Tight coupling would make the agent rigid. Dependency injection allows flexibility but complicates setup.

### Solution
The Agent constructor accepts two interfaces:
- `ExecutionBackend`: Abstracts domain operations (query execution, result parsing)
- `LLMProvider`: Abstracts model invocation (prompt formatting, API calls)

This enables:
- **Testing**: Mock implementations for unit tests
- **Flexibility**: Swap backends/LLMs without agent changes
- **Composition**: Different agents with different capabilities

### Trade-off
Constructor complexity (must provide two dependencies) vs. runtime flexibility. We prioritize flexibility for multi-domain deployments.
```

---

### ❌ Missing Diagrams

**BAD**:
```markdown
## Tool System

The tool system has a registry, executor, and result aggregator. Tools are registered dynamically and executed concurrently.
```

Without a diagram, this is hard to visualize.

**GOOD**:
```markdown
## Tool System Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Tool System                        │
│                                                      │
│  ┌──────────────────┐      ┌──────────────────┐    │
│  │  Tool Registry   │────▶ │  Tool Executor   │    │
│  │  (sync.Map)      │      │  (goroutines)    │    │
│  └────────┬─────────┘      └────────┬─────────┘    │
│           │                         │               │
│           │ Register               │ Results       │
│           │                         │               │
│  ┌────────▼─────────┐      ┌────────▼─────────┐    │
│  │   Tool Interface │      │  Result Channel  │    │
│  │   (Execute)      │      │  (chan Result)   │    │
│  └──────────────────┘      └──────────────────┘    │
└─────────────────────────────────────────────────────┘
```

The tool system uses three components:

1. **Tool Registry**: Thread-safe registry (`sync.Map`) for dynamic tool registration
2. **Tool Executor**: Goroutine pool for concurrent tool execution
3. **Result Channel**: Buffered channel for aggregating results

Tools implement the `Tool` interface with a single `Execute` method. Registration is lock-free using `sync.Map`. Execution spawns one goroutine per tool, with results collected via a shared channel.
```

---

### ❌ No Trade-off Analysis

**BAD**:
```markdown
## Memory System

We use a segmented memory model with ROM, Kernel, L1, and L2 layers.
```

Why this design? What alternatives were considered?

**GOOD**:
```markdown
## Memory System

### Design Choice: Segmented Memory

**Chosen Approach**: Four-layer segmented memory (ROM/Kernel/L1/L2)

**Rationale**:
- Predictable token budget (critical for cost control)
- Balances recent context with long-term memory
- Allows hot-swapping patterns without session restart (ROM layer)

**Alternatives Considered**:

1. **Full History**
   - ✅ Perfect recall
   - ❌ Unbounded token growth → rejected due to cost

2. **Fixed Sliding Window**
   - ✅ Simple implementation
   - ❌ Loses all context beyond window → rejected for long conversations

3. **External RAG Memory**
   - ✅ Unbounded storage
   - ❌ Retrieval latency (100-500ms) → rejected for real-time interaction

4. **Hybrid (Current Choice)**
   - ✅ Bounded tokens, long-term context, pattern hot-reload
   - ❌ Complexity, lossy summarization → acceptable trade-off
```

---

### ❌ Vague Performance Claims

**BAD**:
```markdown
Pattern hot-reload is fast.
```

How fast? Under what conditions?

**GOOD**:
```markdown
### Pattern Hot-Reload Performance

**Latency**: 89-143ms (p50-p99)

**Measurement Conditions**:
- Pattern library size: 59 patterns (11 libraries)
- Total pattern bytes: ~80KB YAML
- Test hardware: M2 MacBook Pro

**Breakdown**:
- File watch notification: 10-15ms
- YAML parsing: 45-60ms
- TF-IDF index rebuild: 20-40ms
- Atomic swap: <1ms

**Scaling**: O(n log n) where n = pattern count (TF-IDF indexing dominates)

**Optimization Considered**: Incremental index updates
- Would reduce latency to ~30ms
- Adds complexity (index mutation synchronization)
- Rejected: 89-143ms acceptable for <1 reload/minute expected frequency
```

---

## Quality Checklist

Before committing any architecture documentation:

- [ ] **Table of Contents**
- [ ] **Diagrams**: At least 2 ASCII diagrams (system context + component) that have been audited or designed by the ascii-diagram-architect agent.
- [ ] **Design rationale**: Explained WHY, not just WHAT
- [ ] **Trade-offs**: Analyzed alternatives and explained chosen approach
- [ ] **Formal properties**: Stated invariants or key properties
- [ ] **Performance**: Quantified where possible (latency, throughput, complexity)
- [ ] **Concurrency**: Described threading/synchronization model
- [ ] **Error handling**: Explained error propagation strategy
- [ ] **No implementation**: No code snippets showing "how to use" (that's for reference)
- [ ] **Citations**: Referenced prior art, papers, or patterns where applicable
- [ ] **Cross-references**: Links to related architecture, reference, and guide docs
- [ ] **No marketing**: Zero marketing speak, only technical facts

---

## Verification Approach

### Diagram Completeness

```bash
# Check that architecture docs have diagrams
grep -L '┌─' website/content/en/docs/architecture/*.md

# Should return empty (all docs have box-drawing diagrams)
```

### Design Rationale Present

```bash
# Check for trade-off sections
grep -L '## Design Trade-offs\|## Trade-offs\|Alternatives Considered' \
  website/content/en/docs/architecture/*.md

# Should return empty (all docs explain design decisions)
```

---

## Documentation Workflow

1. **Understand the System**: Read code, run tests, trace execution
2. **Identify Design Decisions**: What choices were made? Why?
3. **Map Components**: What are the major subsystems? How do they interact?
4. **Draw Diagrams**: Start with ASCII diagrams before writing prose
5. **Analyze Trade-offs**: For each design decision, what were the alternatives?
6. **Cite Foundations**: What patterns, papers, or prior art influenced this?
7. **Review for Clarity**: Is this understandable to an architect who hasn't seen the code?

---

## Architecture vs. Reference vs. Guide

**Architecture** (this directory):
- **What**: System design and rationale
- **Audience**: Architects, academics, advanced developers
- **Style**: Conceptual, theoretical, design-focused
- **Example**: "Memory is segmented into ROM/Kernel/L1/L2 to balance token budget with conversation continuity, inspired by CPU cache hierarchies"

**Reference** (`/reference/`):
- **What**: Complete technical specifications
- **Audience**: Developers needing exact API details
- **Style**: Exhaustive, precise, specification-focused
- **Example**: "The `Memory` struct has four fields: `ROM []byte`, `Kernel []byte`, `L1 *CircularBuffer`, `L2 *SummaryStore`"

**Guide** (`/guides/`):
- **What**: Task-oriented how-to instructions
- **Audience**: Users accomplishing specific goals
- **Style**: Step-by-step, practical, example-focused
- **Example**: "To enable memory persistence: `looms config set agent.memory.type sqlite`"

---

## Questions to Ask Before Writing

1. **Why does this system exist?**
   - What problem does it solve?
   - What design goals does it prioritize?

2. **What are the fundamental components?**
   - What are the major subsystems?
   - How do they interact?
   - What are the key interfaces?

3. **What design decisions were made?**
   - What alternatives were considered?
   - Why was the chosen approach selected?
   - What are the consequences?

4. **What are the key properties?**
   - What invariants are maintained?
   - What performance characteristics matter?
   - What concurrency model is used?

5. **What are the constraints?**
   - What limitations exist?
   - Why do these limitations exist?
   - How do they affect system behavior?

6. **What prior work influenced this?**
   - What patterns or papers are relevant?
   - How does this relate to existing research?

---

## Architecture Documentation Responsibilities

### Individual Contributors
- **Design rationale**: Document WHY, not just WHAT
- **Diagrams**: Create clear ASCII diagrams
- **Trade-offs**: Analyze alternatives and explain choices
- **Honesty**: Document limitations and constraints

### Reviewers
- **Verify completeness**: Check for diagrams, trade-offs, rationale
- **Check clarity**: Ensure architects can understand without code
- **Enforce standards**: Hold docs to this CLAUDE.md standard
- **Academic rigor**: Ensure formal properties and citations where applicable

### Maintainers
- **Keep current**: Update architecture docs when design changes
- **Evolution tracking**: Document how architecture has evolved
- **Quality bar**: Architecture docs establish design understanding
- **Cross-reference**: Ensure architecture, reference, and guides are linked

---

**Remember**: Architecture documentation is the design record of Loom. It must explain design rationale, analyze trade-offs, and maintain academic rigor. Diagrams are mandatory. No compromises.
