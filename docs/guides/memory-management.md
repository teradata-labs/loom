
## Overview

Loom uses a **segmented memory architecture** to efficiently manage conversation history and context for long-running agent sessions. As conversations grow, Loom intelligently compresses older messages to stay within LLM context windows while preserving important information.

**Key Features:**
- ✅ Adaptive compression based on workload type
- ✅ Three preset profiles: data_intensive, conversational, balanced
- ✅ Configurable thresholds and batch sizes
- ✅ Integrated observability with Hawk metrics
- ✅ Backwards compatible defaults

**Status:** Implemented in v1.0.0-beta.5


## Memory Architecture

### Layered Memory System

Loom organizes conversation context into four tiers:

1. **ROM Layer** (Read-Only Memory)
   - Static documentation and system prompts
   - Never changes during the session
   - Always included in context

2. **Kernel Layer**
   - Tool definitions
   - Recent tool execution results
   - Cached schemas (LRU eviction, max 10)
   - Per-conversation state

3. **L1 Cache** (Hot Memory)
   - Recent messages (last N exchanges)
   - Size depends on workload profile:
     - Data intensive: 5 messages
     - Balanced: 8 messages
     - Conversational: 12 messages
   - Adaptive compression triggered by token budget

4. **L2 Cache** (Warm Memory)
   - Compressed summaries of older conversation
   - LLM-powered compression (when configured)
   - Falls back to heuristic summarization

5. **Swap Layer** (Cold Storage)
   - Database-backed long-term storage
   - Automatic eviction when L2 exceeds 5000 tokens
   - Enables "forever conversations"


## Workload Profiles

Loom provides three preset compression profiles optimized for different agent types.

### Data Intensive Profile

**Best for:** SQL agents, data analysis, large result sets

```yaml
memory:
  type: memory
  memory_compression:
    workload_profile: data_intensive
```

**Configuration:**
- **Max L1 Messages:** 5 (aggressive compression)
- **Warning Threshold:** 50% (compress early)
- **Critical Threshold:** 70%
- **Batch Sizes:** normal=2, warning=4, critical=6

**Use Cases:**
- Teradata SQL agents with large query results
- Data analysis agents processing tables/reports
- Agents handling file uploads or large documents

**Rationale:** Data-intensive workloads generate large tool results that quickly consume context. Aggressive compression keeps token usage low while preserving query history.


### Conversational Profile

**Best for:** Chat agents, Q&A assistants, tutoring

```yaml
memory:
  type: memory
  memory_compression:
    workload_profile: conversational
```

**Configuration:**
- **Max L1 Messages:** 12 (preserve recent context)
- **Warning Threshold:** 70%
- **Critical Threshold:** 85%
- **Batch Sizes:** normal=4, warning=6, critical=8

**Use Cases:**
- Customer support chatbots
- Educational tutoring agents
- General-purpose Q&A assistants
- Multi-turn dialogue systems

**Rationale:** Conversational workloads benefit from larger recent context windows to maintain coherence across many exchanges. Compression happens later to preserve conversational flow.


### Balanced Profile (Default)

**Best for:** General-purpose agents, mixed workloads

```yaml
memory:
  type: memory
  memory_compression:
    workload_profile: balanced
```

**Configuration:**
- **Max L1 Messages:** 8
- **Warning Threshold:** 60%
- **Critical Threshold:** 75%
- **Batch Sizes:** normal=3, warning=5, critical=7

**Use Cases:**
- Multi-purpose agents
- Unknown/variable workload patterns
- Development and prototyping

**Rationale:** Balanced profile provides reasonable defaults for most use cases. This is the default when no profile is specified (backwards compatibility).


## Custom Configuration

Override specific profile values for fine-tuned control:

```yaml
memory:
  type: memory
  memory_compression:
    workload_profile: balanced
    max_l1_messages: 15          # Override max L1 size
    warning_threshold_percent: 55 # Compress at 55% budget
    batch_sizes:
      normal: 2
      warning: 4
      critical: 6
```

**Precedence:**
1. Explicit config values (highest priority)
2. Profile defaults
3. Balanced profile defaults (fallback)

**Validation:**
- `max_l1_messages` must be > 0
- `min_l1_messages` must be >= 1
- `warning_threshold_percent` must be in [1, 100]
- `critical_threshold_percent` must be >= warning threshold
- Batch sizes must be > 0


## Configuration Reference

### Memory Compression Config

```yaml
memory:
  type: memory  # or sqlite
  memory_compression:
    # Profile selection (data_intensive, conversational, balanced)
    workload_profile: balanced

    # L1 cache size (number of messages to keep hot)
    max_l1_messages: 8    # Compress when exceeded
    min_l1_messages: 4    # Minimum to preserve

    # Token budget thresholds (percentage)
    warning_threshold_percent: 60   # Start compressing aggressively
    critical_threshold_percent: 75  # Maximum compression

    # Batch sizes (messages per compression cycle)
    batch_sizes:
      normal: 3    # Below warning threshold
      warning: 5   # Between warning and critical
      critical: 7  # Above critical threshold
```

### Complete Agent Config Example

```yaml
agent:
  name: sql-agent-optimized
  description: SQL agent with data_intensive compression

  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    context_size: 200000
    reserved_output_tokens: 20000

  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/sessions/sql-agent.db
    memory_compression:
      workload_profile: data_intensive
      # Optionally override specific values
      max_l1_messages: 6
      warning_threshold_percent: 45
```


## Observability

### Hawk Metrics

Loom records compression events to Hawk for observability:

**Metrics:**
- `memory.compression.events` - Compression event counter
- `memory.compression.messages` - Messages compressed per event
- `memory.compression.tokens_saved` - Tokens saved by compression
- `memory.compression.budget_pct` - Budget percentage at compression time
- `memory.l1.size` - L1 cache size after compression

**Labels:**
- `profile` - Workload profile name (data_intensive, conversational, balanced)
- `batch_size` - Batch size used (normal, warning, critical)
- `trigger_threshold` - Budget percentage that triggered compression

**Events:**
- `memory.profile_configured` - Profile selection and configuration

### Monitoring Example

Check Hawk dashboard to monitor compression behavior:

```bash
# View compression events
curl http://localhost:9090/metrics | grep memory.compression

# Example output:
memory.compression.events{profile="data_intensive",batch_size="warning"} 15
memory.compression.tokens_saved{profile="data_intensive",batch_size="warning"} 12450
memory.compression.budget_pct{profile="data_intensive",batch_size="warning"} 62.5
```


## How Compression Works

### Trigger Conditions

Compression triggers when **both** conditions are met:
1. **L1 size exceeds max:** More than `max_l1_messages` in L1 cache
2. **Token budget high:** Usage exceeds warning threshold

### Compression Process

1. **Calculate batch size:**
   - Budget < warning threshold → normal batch
   - Budget >= warning threshold → warning batch
   - Budget >= critical threshold → critical batch

2. **Select messages to compress:**
   - Take oldest N messages from L1 (N = batch size)
   - Ensure tool_use/tool_result pairs stay together

3. **Generate summary:**
   - Use LLM-powered compression (if configured)
   - Fall back to heuristic summarization

4. **Update memory:**
   - Append summary to L2 cache
   - Remove compressed messages from L1
   - Update token counts

5. **Record metrics:**
   - Log compression event
   - Send metrics to Hawk

### Tool Pair Preservation

**Critical:** Tool execution pairs (`tool_use` + `tool_result`) must stay together. Compression boundary adjustment ensures pairs are never split.

Example:
```
L1: [user, assistant+tool_use, tool_result, user, assistant]
                   └─────┬─────┘
                      Pair must stay together
```

If compression boundary falls between a pair, the boundary is adjusted to keep them together.


## Best Practices

### Choosing a Profile

1. **Start with balanced** for new agents
2. **Switch to data_intensive** if you see:
   - Context window exhaustion errors
   - Large tool results (> 5KB)
   - Frequent compression events (> 10/conversation)
3. **Switch to conversational** if you see:
   - Loss of conversational coherence
   - References to earlier conversation failing
   - Premature compression (< 50% budget)

### Tuning for Your Workload

**If compression happens too late:**
```yaml
memory_compression:
  warning_threshold_percent: 50  # Lower threshold
  max_l1_messages: 6             # Smaller L1 cache
```

**If compression happens too early:**
```yaml
memory_compression:
  warning_threshold_percent: 70  # Higher threshold
  max_l1_messages: 12            # Larger L1 cache
```

**If compression is too aggressive:**
```yaml
memory_compression:
  batch_sizes:
    normal: 2   # Compress fewer messages per cycle
    warning: 3
    critical: 5
```

### Monitoring in Production

1. **Track compression frequency:**
   - Goal: 3-5 compressions per 20-turn conversation
   - Too frequent: Increase thresholds or L1 size
   - Too rare: Lower thresholds

2. **Monitor token savings:**
   - Data intensive: 50-70% savings per compression
   - Balanced: 40-60% savings
   - Conversational: 30-50% savings

3. **Watch budget usage:**
   - Should stay below 80% most of the time
   - Peaks above 90% indicate profile needs adjustment


## Backwards Compatibility

**No configuration changes required.** Existing agents automatically use the balanced profile:

```yaml
# Old config (no memory_compression)
memory:
  type: memory

# Equivalent to:
memory:
  type: memory
  memory_compression:
    workload_profile: balanced
```

**Default behavior:**
- Max L1 messages: 8 (changed from 10)
- Min L1 messages: 4 (changed from 5)
- Thresholds: 60%/75% (changed from 70%/85%)

These changes provide better compression defaults while maintaining reasonable context preservation.


## Troubleshooting

### Context Window Exhaustion

**Symptom:** Agent crashes with "context window exceeded" error

**Solutions:**
1. Switch to `data_intensive` profile
2. Lower `warning_threshold_percent` to 40-50%
3. Reduce `max_l1_messages` to 5
4. Enable LLM-powered compression (better summaries)

### Lost Conversational Context

**Symptom:** Agent forgets earlier conversation details

**Solutions:**
1. Switch to `conversational` profile
2. Increase `max_l1_messages` to 12-15
3. Raise `warning_threshold_percent` to 70%
4. Check if L2 summaries are too brief (enable detailed compression)

### Frequent Compression Events

**Symptom:** Compression happening every 2-3 turns

**Solutions:**
1. Increase `warning_threshold_percent`
2. Increase `max_l1_messages`
3. Reduce batch sizes to compress less aggressively

### No Compression Happening

**Symptom:** Token budget grows but no compression

**Solutions:**
1. Verify `max_l1_messages` is being exceeded
2. Check that token budget is above warning threshold
3. Ensure compressor is configured (for LLM compression)
4. Check Hawk logs for compression errors


## Migration from Beta 4

**Breaking Changes:** None

**New Features:**
- Three workload profiles (data_intensive, conversational, balanced)
- Hawk metrics integration
- Configurable compression thresholds and batch sizes

**Recommended Actions:**
1. Review your agent's workload pattern
2. Add appropriate profile to config (optional)
3. Monitor Hawk metrics after deployment
4. Adjust thresholds based on observed behavior

**Example Migration:**

```yaml
# Before (beta 4):
memory:
  type: memory

# After (beta 5) - SQL agent:
memory:
  type: memory
  memory_compression:
    workload_profile: data_intensive

# After (beta 5) - Chat agent:
memory:
  type: memory
  memory_compression:
    workload_profile: conversational
```


## Related Documentation

- [Agent Configuration](/docs/guides/agent-configuration/)
- [Observability Integration](/docs/guides/integration/observability/)
- [Session Management](/docs/concepts/sessions/)
- [Architecture Deep Dive](/docs/concepts/architecture/)


## Feedback

If you encounter issues or have questions about memory management:
- Open an issue at https://github.com/anthropics/loom/issues
- Tag with `memory` and `compression` labels
- Include Hawk metrics and agent config
