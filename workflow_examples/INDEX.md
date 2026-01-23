# Workflow Examples - Complete Index

## 📚 What's Included

This repository contains **production-ready workflow examples** for the Loom agent framework, organized by pattern and use case.

### 🎯 Core Patterns (`/patterns/`)

Complete implementations of fundamental workflow patterns:

1. **[Pipeline Pattern](patterns/01_pipeline.yaml)** - Sequential data processing
   - ETL data pipeline with quality gates
   - Extract → Validate → Transform → Load
   - Best for: Data processing, document workflows

2. **[Parallel Pattern](patterns/02_parallel.yaml)** - Independent concurrent execution
   - Marketing multi-channel analysis
   - 4 analyses running simultaneously
   - Best for: Independent data gathering, concurrent testing

3. **[Fork-Join Pattern](patterns/03_fork_join.yaml)** - Parallel with aggregation
   - Multi-perspective code review
   - 4 reviewers + 1 consolidator
   - Best for: Code review, risk assessment, decision making

4. **[Hierarchical Pattern](patterns/04_hierarchical.yaml)** - Multi-level delegation
   - Research report generation (3 levels)
   - Specialists → Team Leads → Executive
   - Best for: Strategic planning, business cases

**Quick Reference:** [Pattern Selection Guide](patterns/QUICK_REFERENCE.md)
**Full Documentation:** [Pattern README](patterns/README.md)

---

## 🎓 Learning Path

### For Beginners
1. Start with **Pipeline Pattern** - simplest sequential flow
2. Understand data flow and shared memory usage
3. Run the example and inspect outputs

### For Intermediate Users
1. Explore **Parallel Pattern** for performance
2. Try **Fork-Join** to aggregate multiple perspectives
3. Customize agents for your domain

### For Advanced Users
1. Study **Hierarchical Pattern** for complex projects
2. Combine patterns for hybrid workflows
3. Optimize for production use

---

## 🚀 Quick Start

### 1. Choose Your Pattern

**Need sequential processing?** → Pipeline
```bash
loom weave patterns/01_pipeline.yaml
```

**Need speed through parallelism?** → Parallel
```bash
loom weave patterns/02_parallel.yaml
```

**Need multiple perspectives?** → Fork-Join
```bash
loom weave patterns/03_fork_join.yaml --set code_submission="$(cat code.py)"
```

**Need hierarchical synthesis?** → Hierarchical
```bash
loom weave patterns/04_hierarchical.yaml
```

### 2. Customize for Your Use Case

All examples use shared memory for data flow:

```yaml
# In your workflow YAML
tasks:
  - id: extract_data
    description: |
      Extract data from YOUR_SOURCE.
      Store in shared memory under key 'raw_data'.
  
  - id: process_data
    description: |
      Read data from shared memory key 'raw_data'.
      Process and store under key 'processed_data'.
    context:
      - extract_data  # Ensures order
```

### 3. Run and Inspect

```bash
# Run workflow
loom weave patterns/01_pipeline.yaml

# Inspect results
# Results stored in workflow output directory
# Shared memory accessible via loom CLI or API
```

---

## 📋 Pattern Selection Matrix

| Your Need | Pattern | File | Speed | Complexity |
|-----------|---------|------|-------|------------|
| **Sequential data transformation** | Pipeline | `01_pipeline.yaml` | ⭐ | ⭐ |
| **Independent concurrent tasks** | Parallel | `02_parallel.yaml` | ⭐⭐⭐⭐ | ⭐ |
| **Multi-perspective analysis** | Fork-Join | `03_fork_join.yaml` | ⭐⭐⭐ | ⭐⭐ |
| **Hierarchical research** | Hierarchical | `04_hierarchical.yaml` | ⭐⭐ | ⭐⭐⭐ |

---

## 🔧 Common Use Cases

### Data Engineering
- **ETL Pipelines**: Use Pipeline pattern (01)
- **Data Quality**: Use Pipeline with validation stages
- **Parallel Data Loads**: Use Parallel pattern (02)

### Software Development
- **Code Review**: Use Fork-Join pattern (03)
- **Multi-environment Testing**: Use Parallel pattern (02)
- **Release Validation**: Use Pipeline pattern (01)

### Business Analysis
- **Market Research**: Use Hierarchical pattern (04)
- **Competitive Analysis**: Use Fork-Join pattern (03)
- **Multi-channel Attribution**: Use Parallel pattern (02)

### Decision Making
- **Risk Assessment**: Use Fork-Join pattern (03)
- **Strategic Planning**: Use Hierarchical pattern (04)
- **Consensus Building**: Use Fork-Join pattern (03)

---

## 📖 Documentation Structure

```
workflow_examples/
├── INDEX.md                          ← You are here
├── patterns/
│   ├── README.md                     ← Full pattern documentation
│   ├── QUICK_REFERENCE.md            ← One-page cheat sheet
│   ├── 01_pipeline.yaml              ← Sequential processing
│   ├── 02_parallel.yaml              ← Concurrent execution
│   ├── 03_fork_join.yaml             ← Parallel + aggregation
│   └── 04_hierarchical.yaml          ← Multi-level delegation
```

---

## 🎯 Key Concepts

### Shared Memory
All patterns use shared memory for data exchange:

```python
# Agent writes to shared memory
shared_memory_write(key="results", value=json.dumps(data))

# Another agent reads from it
data = shared_memory_read(key="results")
```

### Task Dependencies
Control execution order via `context`:

```yaml
tasks:
  - id: task_b
    description: Process data from task_a
    context:
      - task_a  # Ensures task_a completes first
```

### Agent Specialization
Each agent has focused expertise:

```yaml
agents:
  - id: security_expert
    role: Security Analyst
    goal: Identify security vulnerabilities
    backstory: |
      You are a security expert specializing in...
```

---

## 🏆 Best Practices

### ✅ DO
- **Define clear input/output contracts** per task
- **Use shared memory keys consistently** across tasks
- **Include validation and error handling** in critical paths
- **Document agent expertise** in backstory
- **Store intermediate results** for debugging

### ❌ DON'T
- **Create circular dependencies** between tasks
- **Forget to specify task context** when order matters
- **Overload agents with multiple responsibilities**
- **Skip error handling** in production workflows
- **Hardcode values** - use configuration or inputs

---

## 🔍 Pattern Comparison

### Execution Flow

**Pipeline (Sequential):**
```
Task1 → Task2 → Task3 → Task4
Time: 20 min (5 min each)
```

**Parallel (Concurrent):**
```
Task1 ┐
Task2 ├─→ Results
Task3 ┘
Time: 5 min (all at once)
```

**Fork-Join (Parallel + Merge):**
```
     ┌─→ Task1 ─┐
Input├─→ Task2  ├─→ Consolidate → Output
     └─→ Task3 ─┘
Time: 7 min (5 min parallel + 2 min consolidate)
```

**Hierarchical (Multi-level):**
```
        Executive (Level 1)
            │
    ┌───────┼───────┐
   L2A     L2B     L2C (Level 2)
   ├─┤     ├─┤     ├─┤
  S1 S2   S3 S4   S5 S6 (Level 3 - Specialists)
Time: Depends on tree depth
```

---

## 🛠️ Customization Guide

### 1. Change Data Sources

Replace example data with your sources:

```yaml
description: |
  Extract data from YOUR_DATABASE.
  
  Connection: postgresql://host:5432/db
  Query: SELECT * FROM your_table
  
  Store in shared memory under key 'extracted_data'.
```

### 2. Adjust Agent Expertise

Modify agents for your domain:

```yaml
agents:
  - id: domain_expert
    role: Your Industry Analyst
    goal: Analyze YOUR_DOMAIN data
    backstory: |
      You are an expert in YOUR_INDUSTRY with
      specialization in YOUR_AREA.
```

### 3. Configure Output Format

Set output preferences:

```yaml
output:
  format: json          # or markdown, yaml
  include_context: true # include task contexts
  primary_result: final_task_id
```

---

## 📊 Performance Characteristics

| Pattern | Concurrency | Throughput | Latency | Memory |
|---------|-------------|------------|---------|---------|
| Pipeline | None | Low | High | Low |
| Parallel | High | High | Low | High |
| Fork-Join | Medium | Medium | Medium | Medium |
| Hierarchical | Varies | Medium | Medium | Medium |

**Optimization Tips:**
- Use Parallel for independent tasks (max speed)
- Use Pipeline when order matters (data integrity)
- Use Fork-Join when synthesis needed (comprehensive)
- Use Hierarchical for complex projects (structure)

---

## 🧪 Testing Your Workflows

### 1. Start Small
Test with minimal data first:
```bash
loom weave patterns/01_pipeline.yaml --dry-run
```

### 2. Validate Dependencies
Ensure task execution order is correct:
```bash
loom weave patterns/03_fork_join.yaml --validate
```

### 3. Monitor Execution
Watch progress in real-time:
```bash
loom weave patterns/02_parallel.yaml --verbose
```

### 4. Inspect Results
Check shared memory and outputs:
```bash
# View shared memory
loom memory list

# Read specific key
loom memory get results_key
```

---

## 🐛 Troubleshooting

### Common Issues

**Tasks Hanging:**
- Check for circular dependencies in `context`
- Verify all required shared memory keys exist
- Ensure agents have necessary tool access

**Wrong Execution Order:**
- Review task `context` dependencies
- Verify workflow `type` matches pattern
- Check for missing task prerequisites

**Memory Issues:**
- Use pagination for large datasets
- Stream data instead of loading all
- Clean up intermediate results

**Slow Performance:**
- Profile task execution times
- Consider parallelizing independent tasks
- Optimize database queries
- Use caching where appropriate

---

## 📚 Additional Resources

### Documentation
- **Workflow Design Guide**: Best practices for workflow creation
- **Agent Design Guide**: Creating effective agent configurations
- **Shared Memory Guide**: Data exchange patterns
- **Performance Tuning**: Optimization techniques

### Examples by Industry
- **Finance**: Fraud detection, risk assessment
- **Healthcare**: Clinical decision support, patient triage  
- **Retail**: Demand forecasting, inventory optimization
- **Technology**: Code review, incident response

### Community
- **GitHub Discussions**: Ask questions and share examples
- **Slack Community**: Real-time help and collaboration
- **Blog**: Deep dives and case studies

---

## 🤝 Contributing

### Adding New Patterns

1. Create workflow YAML following conventions:
   - Clear naming: `05_your_pattern.yaml`
   - Comprehensive comments and documentation
   - Real-world use case example

2. Add documentation:
   - Update `patterns/README.md`
   - Add entry to this INDEX.md
   - Include decision criteria

3. Test thoroughly:
   - Verify execution with sample data
   - Document expected output
   - Include error cases

4. Submit PR with:
   - Pattern file
   - Documentation updates
   - Example execution
   - Performance characteristics

### Improving Examples

- Suggest better use cases
- Add domain-specific customizations
- Improve agent backstories
- Optimize performance

---

## 🎓 Next Steps

1. **Explore Patterns**: Review the 4 core patterns in `/patterns/`
2. **Run Examples**: Execute workflows with sample data
3. **Customize**: Adapt patterns to your use case
4. **Create New**: Build your own workflows using patterns as templates
5. **Share**: Contribute your workflows back to the community

---

## 📞 Support

- **Documentation**: `/docs/` directory
- **Examples**: This repository
- **Issues**: GitHub Issues
- **Community**: Slack channel
- **Email**: support@loom.ai

---

**Ready to get started?** Head to [patterns/QUICK_REFERENCE.md](patterns/QUICK_REFERENCE.md) for a one-page overview, or dive into [patterns/README.md](patterns/README.md) for comprehensive documentation.

**Questions?** Check the troubleshooting section or reach out to the community!
