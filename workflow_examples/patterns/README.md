# Workflow Pattern Examples

This directory contains practical examples of common workflow patterns using the Loom agent framework. Each pattern demonstrates when and how to structure your workflows for different use cases.

## 📁 Pattern Examples

### 1. Pipeline Pattern (`01_pipeline.yaml`)
**Sequential data processing with validation**

```
Extract → Validate → Transform → Load
```

**Use Cases:**
- ETL/ELT data pipelines
- Document processing workflows
- Multi-stage approval processes
- Sequential validation chains

**Key Characteristics:**
- Linear execution order
- Each task depends on previous task's output
- Strong data quality gates
- Clear error propagation

**Benefits:**
- Predictable execution flow
- Easy to debug and monitor
- Strong data validation at each stage
- Clear responsibility per stage

**Example Scenario:**
Customer transaction data flows through extraction, quality validation, business rule transformation, and warehouse loading. Each stage has clear input/output contracts and quality gates.

---

### 2. Parallel Pattern (`02_parallel.yaml`)
**Independent concurrent task execution**

```
Email Analysis ┐
Social Analysis ├─→ Results Collection
Paid Search     │
Content Analysis┘
```

**Use Cases:**
- Multi-channel analytics
- Independent data gathering
- Parallel risk assessments
- Concurrent testing scenarios

**Key Characteristics:**
- Tasks have no dependencies on each other
- Execute simultaneously
- 4x faster than sequential (for 4 tasks)
- Results collected when all complete

**Benefits:**
- Maximum speed through concurrency
- Independent perspectives prevent bias
- Scalable (add more parallel tasks easily)
- Efficient resource utilization

**Example Scenario:**
Marketing team analyzes campaign performance across email, social, paid search, and content channels. Each analysis runs independently and completes in parallel, delivering results 4x faster than sequential analysis.

---

### 3. Fork-Join Pattern (`03_fork_join.yaml`)
**Parallel execution with result aggregation**

```
         ┌─→ Security Review ─┐
         ├─→ Performance Review ├─→ Consolidate → Final Report
Code ──→ ├─→ Maintainability ──┤
         └─→ Architecture Review┘
```

**Use Cases:**
- Code review from multiple angles
- Risk assessment (financial, legal, technical)
- Multi-criteria decision making
- Consensus-building analyses

**Key Characteristics:**
- Fork: Multiple parallel independent analyses
- Join: Single consolidation task synthesizes results
- Comprehensive multi-perspective view
- Prioritized, actionable recommendations

**Benefits:**
- Faster than sequential reviews
- Reduces groupthink and blind spots
- Comprehensive coverage
- Consolidated actionable output

**Example Scenario:**
Code submission is reviewed simultaneously by security, performance, maintainability, and architecture experts. A lead reviewer consolidates findings, resolves conflicts, prioritizes issues, and provides a unified recommendation.

---

### 4. Hierarchical Pattern (`04_hierarchical.yaml`)
**Multi-level delegation and synthesis**

```
                    Research Director (Executive)
                            │
        ┌───────────────────┼───────────────────┐
   Market Lead         Technical Lead      Financial Lead
        │                   │                   │
   ┌────┴────┐         ┌────┴────┐         ┌────┴────┐
Competitor Customer  Arch. Security    Cost  Revenue
Analyst   Analyst    Spec. Spec.      Analyst Analyst
```

**Use Cases:**
- Research report generation
- Strategic planning
- Business case development
- Complex project proposals

**Key Characteristics:**
- Multi-level structure (3+ levels)
- Specialist expertise at leaf nodes
- Team leads consolidate domain findings
- Executive synthesizes cross-functional view

**Benefits:**
- Deep specialized expertise
- Structured delegation
- Progressive synthesis
- Executive-ready deliverables

**Example Scenario:**
Research project investigates new product opportunity. Specialists conduct deep analysis in their domains (competitors, customers, architecture, security, costs, revenue). Team leads consolidate domain reports. Director synthesizes comprehensive executive report with go/no-go recommendation.

---

## 🎯 Choosing the Right Pattern

### Decision Flow

```
Do tasks need to execute in order?
│
├─ YES → Do they share/transform the same data?
│         │
│         ├─ YES → 📊 Pipeline Pattern
│         └─ NO  → Consider if order is really needed
│
└─ NO → Do results need to be combined?
          │
          ├─ YES → Are they equal peers or hierarchical?
          │         │
          │         ├─ Peers → 🍴 Fork-Join Pattern  
          │         └─ Hierarchy → 🌳 Hierarchical Pattern
          │
          └─ NO  → ⚡ Parallel Pattern
```

### Pattern Comparison Matrix

| Pattern | Speed | Complexity | Dependencies | Synthesis | Best For |
|---------|-------|-----------|--------------|-----------|----------|
| **Pipeline** | Slowest (sequential) | Low | High (chain) | No | Data processing, ETL |
| **Parallel** | Fastest | Low | None | No | Independent analysis |
| **Fork-Join** | Fast | Medium | None (fork) | Yes | Multi-perspective review |
| **Hierarchical** | Medium | High | Structured | Yes | Research, planning |

---

## 🚀 Running the Examples

### 1. Pipeline Example
```bash
# ETL data pipeline
loom weave workflow_examples/patterns/01_pipeline.yaml \
  --set code_submission="$(cat your_data.json)"
```

### 2. Parallel Example
```bash
# Marketing analysis (runs in parallel)
loom weave workflow_examples/patterns/02_parallel.yaml
```

### 3. Fork-Join Example
```bash
# Multi-perspective code review
loom weave workflow_examples/patterns/03_fork_join.yaml \
  --set code_submission="$(cat your_code.py)"
```

### 4. Hierarchical Example
```bash
# Research report generation
loom weave workflow_examples/patterns/04_hierarchical.yaml \
  --set project_brief="New AI-powered analytics platform"
```

---

## 📝 Pattern Best Practices

### Pipeline Pattern
✅ **DO:**
- Define clear input/output contracts per stage
- Implement quality gates and validation
- Store intermediate results in shared memory
- Include row counts and timestamps in metadata

❌ **DON'T:**
- Skip validation stages to save time
- Allow data to flow unchecked
- Forget to handle partial failures
- Mix concerns within stages

### Parallel Pattern
✅ **DO:**
- Ensure tasks are truly independent
- Use consistent output formats
- Set appropriate timeouts
- Handle partial completion gracefully

❌ **DON'T:**
- Create hidden dependencies between tasks
- Assume completion order
- Share mutable state
- Overload system with too many parallel tasks

### Fork-Join Pattern
✅ **DO:**
- Give clear independent analysis mandates
- Use standardized output formats for easy aggregation
- Explicitly handle conflicts in join task
- Prioritize consolidated recommendations

❌ **DON'T:**
- Let parallel tasks coordinate during execution
- Skip conflict resolution in consolidation
- Forget to synthesize into actionable output
- Allow redundant findings without deduplication

### Hierarchical Pattern
✅ **DO:**
- Define clear levels (specialists → leads → executive)
- Ensure specialists have focused, deep scope
- Have leads synthesize domain findings
- Executive provides cross-functional integration

❌ **DON'T:**
- Create too many levels (>4 levels)
- Let specialists work on overlapping areas
- Skip intermediate consolidation
- Forget to provide executive summary

---

## 🔧 Customizing Patterns

### Adding Your Own Data Sources

Replace shared memory keys with your data:

```yaml
# In task description
description: |
  Extract data from YOUR_SOURCE.
  
  Connect to: postgresql://your-db:5432/prod
  Query: SELECT * FROM customers WHERE active = true
  
  Store in shared memory under key 'raw_data'.
```

### Adjusting Agent Expertise

Modify agent backstories for your domain:

```yaml
agents:
  - id: your_analyst
    role: Your Domain Analyst
    goal: Analyze YOUR_DOMAIN_SPECIFIC data
    backstory: |
      You are an expert in YOUR_INDUSTRY with 10 years experience
      in YOUR_SPECIALIZATION. You focus on YOUR_KEY_METRICS.
```

### Changing Output Formats

Configure output to match your needs:

```yaml
output:
  format: json          # json, markdown, yaml
  include_context: true # Include task contexts
  style: detailed       # detailed, summary, executive
```

---

## 📊 Pattern Performance

### Execution Time Comparisons

**4 Independent Tasks (5 minutes each):**
- Sequential: 20 minutes
- Parallel: 5 minutes (4x speedup)
- Fork-Join: 5 minutes + consolidation (2 min) = 7 min
- Hierarchical: Depends on tree depth

**Pipeline (4 Sequential Tasks):**
- Always: sum of all task times
- Cannot be parallelized without changing data flow

### Resource Utilization

| Pattern | CPU Usage | Memory | Network | Concurrency |
|---------|-----------|--------|---------|-------------|
| Pipeline | 25% | Low (streaming) | Sequential | 1 task at a time |
| Parallel | 100% | High (all data) | Concurrent | All tasks |
| Fork-Join | 100% → 25% | High → Low | Burst then sequential | Fork burst, join single |
| Hierarchical | Varies by level | Medium | Tiered | Per-level parallelism |

---

## 🎓 Learning Path

1. **Start Simple:** Begin with Pipeline for linear workflows
2. **Add Concurrency:** Use Parallel when tasks are independent
3. **Aggregate Results:** Apply Fork-Join when consolidation needed
4. **Scale Complexity:** Use Hierarchical for multi-team projects

### Next Steps

- Review `../use_cases/` for industry-specific examples
- Explore `../advanced/` for hybrid patterns
- Check `../best_practices.md` for production guidelines

---

## 🤝 Contributing

Have a useful pattern to share?

1. Create a new YAML file following naming convention: `05_your_pattern.yaml`
2. Include comprehensive comments and use case description
3. Add entry to this README
4. Submit PR with example execution and expected output

---

## 📚 Additional Resources

- **Workflow Design Guide:** `../docs/workflow_design.md`
- **Agent Best Practices:** `../docs/agent_design.md`
- **Performance Tuning:** `../docs/performance.md`
- **Error Handling:** `../docs/error_handling.md`

---

## 🐛 Troubleshooting

### Tasks Hanging
- Check for circular dependencies
- Verify shared memory keys match
- Ensure all required data is available

### Unexpected Execution Order
- Review task `context` dependencies
- Check workflow type matches pattern
- Verify task IDs are unique

### Memory Issues
- Use pagination for large datasets
- Stream data instead of loading all at once
- Clean up intermediate results after use

### Slow Performance
- Profile task execution times
- Consider parallelizing independent tasks
- Use async operations where possible
- Optimize database queries

---

**Questions?** Open an issue or check the documentation at `/docs/`
