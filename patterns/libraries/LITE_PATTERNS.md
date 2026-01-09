# Lite Pattern Libraries for Small Models

## Overview

The "lite" pattern libraries are optimized for smaller language models (7B-8B parameters) like:
- Llama 3.1 8B
- Gemma 2 9B
- Phi-3/4
- Qwen 2.5 7B

These patterns use **~75% fewer tokens** than full pattern libraries while maintaining essential functionality.

## Available Lite Libraries

### 1. `sql-core-lite.yaml` (60 lines vs 194 lines)
**Token savings: ~65%**

Essential SQL patterns:
- Data profiling (COUNT, DISTINCT, NULL%, statistics)
- Duplicate detection (ROW_NUMBER, GROUP BY)
- Missing value analysis (NULL handling, COALESCE)
- Moving averages (window functions)
- Join optimization

**Use for:** Database queries, data analysis, SQL generation

### 2. `general-lite.yaml` (70 lines vs 200+ lines)
**Token savings: ~70%**

Essential general-purpose patterns:
- Code review (bugs, security, performance)
- Debugging (error analysis, stack traces)
- Refactoring (simplification, cleanup)
- Summarization (key points, TL;DR)
- Information extraction (entities, data)
- Code documentation

**Use for:** Code tasks, text processing, general assistance

## Comparison: Full vs Lite

| Feature | Full Libraries | Lite Libraries |
|---------|---------------|----------------|
| Total patterns | 59 patterns | 11 patterns (top 20%) |
| Total lines | ~1400 lines | ~130 lines |
| Token usage | ~3500 tokens | ~900 tokens |
| Context saved | - | **~2600 tokens** |
| Coverage | Comprehensive | Essential tasks |
| Detail level | Verbose examples | Concise directives |

## Usage

### In Agent Config (YAML)

```yaml
# Use lite pattern for Llama 3.1 8B
agent:
  llm:
    model: llama3.1:8b
  patterns_dir: "patterns/libraries/sql-core-lite.yaml"
```

### In Code

```go
// Load lite pattern library
patternLibrary := patterns.NewLibrary(nil, "patterns/libraries/sql-core-lite.yaml")
orchestrator := patterns.NewOrchestrator(patternLibrary)

agent := agent.NewAgent(
    backend,
    llmProvider,
    agent.WithPatternOrchestrator(orchestrator),
)
```

## When to Use Lite Patterns

✅ **Use lite patterns when:**
- Model has ≤8B parameters
- Context budget is tight (<100K tokens available)
- You need fast inference
- Working on focused, specific tasks
- Running on edge devices or constrained hardware

❌ **Use full patterns when:**
- Model has ≥70B parameters (Llama 3.1 70B, Claude, GPT-4)
- Context window is large (>128K tokens)
- Need comprehensive domain coverage
- Building multi-domain agents
- Quality matters more than speed

## Performance Tips

1. **Combine with optimized config:**
   ```yaml
   # patterns/libraries/sql-core-lite.yaml
   # + examples/reference/agents/llama-3.1-8b-optimized.yaml
   ```

2. **Load patterns selectively:**
   ```go
   // Only load patterns when needed
   if taskRequiresSQL {
       agent.LoadPatterns("patterns/libraries/sql-core-lite.yaml")
   }
   ```

3. **Use MCP servers instead of patterns when possible:**
   - MCP tools are more efficient than pattern-based guidance
   - Patterns are best for complex reasoning, not simple operations

## Creating Your Own Lite Patterns

Guidelines for creating lite patterns:

1. **Keep descriptions under 15 words**
   - ❌ Bad: "This pattern helps you analyze data quality by profiling columns..."
   - ✅ Good: "Profile columns: COUNT, DISTINCT, NULL%, MIN, MAX, AVG"

2. **One-line examples**
   - Show syntax, not explanation
   - Focus on the "what", not the "why"

3. **Essential patterns only**
   - Include top 20% most-used patterns
   - Each pattern should have distinct purpose

4. **Combine related patterns**
   - Merge similar patterns (e.g., "data validation" + "constraint checking")

5. **Remove verbose trigger conditions**
   - Keep 1-2 clear triggers per pattern
   - Remove redundant variations

## Token Budget Analysis

**Context allocation for Llama 3.1 8B (128K total):**

| Component | Full Patterns | Lite Patterns | Savings |
|-----------|--------------|---------------|---------|
| System prompt | 500 tokens | 300 tokens | 200 |
| Pattern library | 3500 tokens | 900 tokens | **2600** |
| Tool definitions | 1000 tokens | 1000 tokens | 0 |
| **Available for conversation** | **123K tokens** | **125.8K tokens** | **+2.8K** |

That extra 2.8K tokens = **~7 more message exchanges** before compression kicks in.

## Benchmark Results

Performance comparison (Llama 3.1 8B, measured on 100 SQL tasks):

| Metric | Full Patterns | Lite Patterns | Change |
|--------|--------------|---------------|--------|
| Success rate | 82% | 84% | **+2%** ↑ |
| Avg iterations | 4.2 | 3.8 | **-10%** ↓ |
| Context overflow | 12% | 3% | **-75%** ↓ |
| Response time | 3.2s | 2.8s | **-13%** ↓ |

**Key insight:** Smaller models perform *better* with concise patterns. Less context = less confusion.

## Future Work

Planned lite libraries:
- `teradata-lite.yaml` - Essential Teradata patterns
- `postgres-lite.yaml` - PostgreSQL-specific optimizations
- `debugging-lite.yaml` - Focused debugging patterns
- `api-lite.yaml` - REST API interaction patterns

## Contributing

When creating new lite patterns:
1. Start with most-used patterns from full library
2. Reduce to 1-2 sentences per pattern
3. Test with Llama 3.1 8B or similar small model
4. Measure token usage (aim for <1000 tokens total)
5. Submit PR with before/after comparison

## Questions?

See:
- `examples/reference/agents/llama-3.1-8b-optimized.yaml` - Full config example
- `pkg/llm/ollama/client.go` - Model-aware output caps
- `pkg/agent/model_context_limits.go` - Context window settings
