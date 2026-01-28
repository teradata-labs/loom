
## Overview

Loom v1.0.0-beta.5 introduces **workload-specific compression profiles** to optimize memory management for different agent types. This release improves compression defaults and adds Hawk observability for production monitoring.

**Key Changes:**
- ✅ Three compression profiles: data_intensive, conversational, balanced
- ✅ Configurable compression thresholds and batch sizes
- ✅ Hawk metrics integration for compression monitoring
- ✅ Better default compression behavior
- ✅ **Zero breaking changes** - fully backwards compatible


## Breaking Changes

**None.** All changes are backwards compatible. Existing agents automatically use the balanced profile with improved defaults.


## What's New

### 1. Workload Compression Profiles

Three preset profiles optimized for different use cases:

**Data Intensive** - For SQL agents, data analysis, large results
- Max L1: 5 messages (aggressive compression)
- Thresholds: 50% warning, 70% critical (compress early)
- Best for: Teradata SQL agents, document processing

**Conversational** - For chatbots, Q&A, tutoring
- Max L1: 12 messages (preserve context)
- Thresholds: 70% warning, 85% critical (compress late)
- Best for: Customer support, educational agents

**Balanced (Default)** - For general-purpose agents
- Max L1: 8 messages
- Thresholds: 60% warning, 75% critical
- Best for: Multi-purpose agents, prototyping

See [Memory Management Guide](/docs/guides/memory-management/) for detailed profile descriptions.

### 2. Improved Default Compression

**Old Defaults (Beta 4):**
- Max L1 Messages: 10
- Min L1 Messages: 5
- Warning Threshold: 70%
- Critical Threshold: 85%
- Fixed batch sizes

**New Defaults (Beta 5):**
- Max L1 Messages: 8 (balanced profile)
- Min L1 Messages: 4
- Warning Threshold: 60%
- Critical Threshold: 75%
- Adaptive batch sizes: 3/5/7 (normal/warning/critical)

**Impact:** Better compression for data-intensive workloads while maintaining conversational coherence.

### 3. Hawk Observability

Compression events now emit metrics to Hawk:

```bash
memory.compression.events          # Compression counter
memory.compression.messages        # Messages per event
memory.compression.tokens_saved    # Token savings
memory.compression.budget_pct      # Budget at compression
memory.l1.size                     # L1 size after compression
```

Labels include profile name, batch size, and trigger threshold.

### 4. Configurable Thresholds

Fine-tune compression behavior per agent:

```yaml
memory:
  memory_compression:
    workload_profile: balanced
    max_l1_messages: 10
    warning_threshold_percent: 55
    critical_threshold_percent: 80
    batch_sizes:
      normal: 2
      warning: 4
      critical: 6
```


## Migration Steps

### Step 1: Review Your Workload

**Identify your agent's workload pattern:**

1. **Data Intensive** if you have:
   - Large tool results (> 5KB)
   - SQL queries with multi-MB results
   - Frequent context window exhaustion
   - File uploads or document processing

2. **Conversational** if you have:
   - Long multi-turn dialogues
   - Q&A requiring recent context
   - Customer support interactions
   - Educational tutoring sessions

3. **Balanced** if you have:
   - Mixed workload patterns
   - General-purpose functionality
   - Prototyping or development

### Step 2: Update Agent Configuration

**Option A: Use Default Balanced Profile (No Changes Required)**

```yaml
# Your existing config works as-is
agent:
  name: my-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
```

No action needed. Your agent automatically uses the balanced profile.

**Option B: Add Specific Profile**

For SQL agents (data intensive):

```yaml
agent:
  name: sql-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/sessions/sql-agent.db
    memory_compression:
      workload_profile: data_intensive  # Add this
```

For chat agents (conversational):

```yaml
agent:
  name: chat-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/sessions/chat-agent.db
    memory_compression:
      workload_profile: conversational  # Add this
```

**Option C: Custom Configuration**

Override specific values:

```yaml
memory:
  memory_compression:
    workload_profile: balanced
    max_l1_messages: 12              # Larger L1
    warning_threshold_percent: 55    # Compress earlier
    batch_sizes:
      normal: 3
      warning: 5
      critical: 7
```

### Step 3: Deploy and Monitor

1. **Deploy to staging** with new config
2. **Monitor Hawk metrics:**
   - Compression frequency (target: 3-5/conversation)
   - Token savings (data_intensive: 50-70%, balanced: 40-60%, conversational: 30-50%)
   - Budget usage (should stay < 80%)
3. **Adjust thresholds** based on observed behavior
4. **Deploy to production** once validated


## Example Configurations

### SQL Agent (Teradata)

**Before (Beta 4):**
```yaml
agent:
  name: teradata-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/sessions/teradata.db
```

**After (Beta 5):**
```yaml
agent:
  name: teradata-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/sessions/teradata.db
    memory_compression:
      workload_profile: data_intensive  # Add this for SQL workloads
```

### Customer Support Chatbot

**Before (Beta 4):**
```yaml
agent:
  name: support-bot
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
```

**After (Beta 5):**
```yaml
agent:
  name: support-bot
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: conversational  # Add this for chatbots
```

### General Purpose Agent

**Before (Beta 4):**
```yaml
agent:
  name: general-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
```

**After (Beta 5):**
```yaml
agent:
  name: general-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    # No changes needed - balanced profile is default
```


## Behavior Changes

### Compression Timing

**Beta 4:** Compression triggered at 70% token budget
**Beta 5:** Compression triggered at 60% token budget (balanced profile)

**Impact:** More proactive compression prevents context window exhaustion.

### L1 Cache Size

**Beta 4:** Max 10 messages in L1 (hardcoded)
**Beta 5:** Max 8 messages in L1 (balanced profile default)

**Impact:** Slightly smaller L1 reduces token usage while maintaining context quality.

### Batch Sizes

**Beta 4:** Fixed batch size (unclear from code)
**Beta 5:** Adaptive batch sizes based on budget usage

**Impact:** More intelligent compression adapts to budget pressure.


## Monitoring Compression

### Hawk Dashboard

Monitor compression behavior via Hawk metrics:

```bash
# View compression frequency
curl http://localhost:9090/metrics | grep memory.compression.events

# Example output:
memory.compression.events{profile="data_intensive",batch_size="warning"} 15
memory.compression.messages{profile="data_intensive",batch_size="warning"} 45
memory.compression.tokens_saved{profile="data_intensive",batch_size="warning"} 12450
```

### Target Metrics

**Good compression behavior:**
- **Frequency:** 3-5 compressions per 20-turn conversation
- **Token savings:** 40-70% per compression (profile-dependent)
- **Budget usage:** Stays below 80% most of the time

**Poor compression behavior:**
- **Too frequent:** > 10 compressions per conversation (increase thresholds)
- **Too rare:** < 2 compressions per 30-turn conversation (lower thresholds)
- **Budget exhaustion:** Consistently > 90% budget (switch to data_intensive)

### Troubleshooting

**Context window still exhausted:**
- Switch to data_intensive profile
- Lower warning_threshold_percent to 40-50%
- Reduce max_l1_messages to 5-6

**Lost conversational context:**
- Switch to conversational profile
- Increase max_l1_messages to 12-15
- Raise warning_threshold_percent to 70-75%

See [Memory Management Guide](/docs/guides/memory-management/#troubleshooting) for detailed troubleshooting.


## API Changes

### Proto Additions

New messages in `loom/v1/agent_config.proto`:

```protobuf
enum WorkloadProfile {
  WORKLOAD_PROFILE_UNSPECIFIED = 0;
  WORKLOAD_PROFILE_BALANCED = 1;
  WORKLOAD_PROFILE_DATA_INTENSIVE = 2;
  WORKLOAD_PROFILE_CONVERSATIONAL = 3;
}

message MemoryCompressionConfig {
  WorkloadProfile workload_profile = 1;
  int32 max_l1_messages = 2;
  int32 min_l1_messages = 3;
  int32 warning_threshold_percent = 4;
  int32 critical_threshold_percent = 5;
  MemoryCompressionBatchSizes batch_sizes = 6;
}
```

Added to MemoryConfig:
```protobuf
message MemoryConfig {
  // ... existing fields
  MemoryCompressionConfig memory_compression = 7;
}
```

### Go API Additions

New types in `pkg/agent`:

```go
type CompressionProfile struct {
    Name                     string
    MaxL1Messages            int
    MinL1Messages            int
    WarningThresholdPercent  int
    CriticalThresholdPercent int
    NormalBatchSize          int
    WarningBatchSize         int
    CriticalBatchSize        int
}

var ProfileDefaults = map[loomv1.WorkloadProfile]CompressionProfile{
    // ... profile definitions
}

func ResolveCompressionProfile(config *loomv1.MemoryCompressionConfig) (CompressionProfile, error)
func WithCompressionProfile(profile *CompressionProfile) Option
```

### Observability Additions

New metrics recorded via `observability.Tracer`:

```go
tracer.RecordMetric("memory.compression.events", 1, labels)
tracer.RecordMetric("memory.compression.messages", float64(count), labels)
tracer.RecordMetric("memory.compression.tokens_saved", float64(tokens), labels)
tracer.RecordMetric("memory.compression.budget_pct", percentage, labels)
tracer.RecordMetric("memory.l1.size", float64(size), labels)

tracer.RecordEvent(ctx, "memory.profile_configured", attributes)
```


## Rollback Plan

If you encounter issues, rolling back is simple:

### Revert to Implicit Balanced Defaults

```yaml
memory:
  type: memory
  # Remove memory_compression section
```

This reverts to balanced profile behavior (same as beta.5 default).

### Revert to Beta 4 Behavior

To match beta.4 exactly:

```yaml
memory:
  type: memory
  memory_compression:
    max_l1_messages: 10              # Old L1 size
    min_l1_messages: 5               # Old L1 minimum
    warning_threshold_percent: 70    # Old warning
    critical_threshold_percent: 85   # Old critical
    batch_sizes:
      normal: 3                      # Approximation
      warning: 5
      critical: 7
```


## Testing Checklist

Before deploying to production:

- [ ] Identify agent workload pattern (data_intensive/conversational/balanced)
- [ ] Update agent config with appropriate profile
- [ ] Deploy to staging environment
- [ ] Monitor Hawk metrics for 24 hours
- [ ] Verify compression frequency is reasonable (3-5/conversation)
- [ ] Check token budget stays below 80%
- [ ] Test long conversations (> 30 turns)
- [ ] Validate conversational coherence (no lost context)
- [ ] Check for context window exhaustion errors
- [ ] Deploy to production
- [ ] Monitor production metrics for 48 hours


## Related Documentation

- [Memory Management Guide](/docs/guides/memory-management/)
- [Observability Integration](/docs/guides/integration/observability/)
- [Agent Configuration](/docs/guides/agent-configuration/)
- [CHANGELOG.md](/CHANGELOG.md)


## Support

If you encounter issues during migration:
- Open an issue at https://github.com/teradata-labs/loom/issues
- Tag with `memory`, `compression`, and `beta5` labels
- Include your agent config and Hawk metrics
- Describe expected vs actual behavior
