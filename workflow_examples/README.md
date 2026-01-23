# Workflow Examples Repository

**Production-ready workflow patterns for the Loom agent framework**

[![Loom Version](https://img.shields.io/badge/loom-v1.0-orange.svg)](https://loom.ai)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Examples](https://img.shields.io/badge/examples-4%20patterns-green.svg)](patterns/)

---

## 🚀 Quick Start

**Start here:** [INDEX.md](INDEX.md) - Complete navigation and learning path

**Just want the essentials?** [patterns/QUICK_REFERENCE.md](patterns/QUICK_REFERENCE.md) - One-page cheat sheet

**Ready to dive deep?** [patterns/README.md](patterns/README.md) - Comprehensive documentation

---

## 📚 What You'll Find

### Core Workflow Patterns

1. **[Pipeline](patterns/01_pipeline.yaml)** - Sequential data processing
   ```bash
   loom weave patterns/01_pipeline.yaml
   ```

2. **[Parallel](patterns/02_parallel.yaml)** - Independent concurrent execution
   ```bash
   loom weave patterns/02_parallel.yaml
   ```

3. **[Fork-Join](patterns/03_fork_join.yaml)** - Parallel with aggregation
   ```bash
   loom weave patterns/03_fork_join.yaml
   ```

4. **[Hierarchical](patterns/04_hierarchical.yaml)** - Multi-level delegation
   ```bash
   loom weave patterns/04_hierarchical.yaml
   ```

---

## 🎯 Pattern Selection (30 Second Guide)

**Choose your pattern based on your needs:**

| I need to... | Use Pattern | Why |
|-------------|-------------|-----|
| Process data step-by-step | **Pipeline** | Ensures correct order and dependencies |
| Run multiple tasks at once | **Parallel** | Maximum speed, no dependencies |
| Get multiple perspectives | **Fork-Join** | Parallel analysis + consolidation |
| Organize complex research | **Hierarchical** | Multi-level synthesis and delegation |

---

## 📖 Documentation Structure

```
workflow_examples/
├── README.md                    ← Start here
├── INDEX.md                     ← Complete navigation
├── patterns/
│   ├── README.md               ← Full pattern documentation
│   ├── QUICK_REFERENCE.md      ← One-page cheat sheet
│   ├── 01_pipeline.yaml        ← Sequential processing example
│   ├── 02_parallel.yaml        ← Concurrent execution example
│   ├── 03_fork_join.yaml       ← Parallel + aggregation example
│   └── 04_hierarchical.yaml    ← Multi-level delegation example
```

---

## 🎓 Learning Path

### Beginners (30 minutes)
1. Read [QUICK_REFERENCE.md](patterns/QUICK_REFERENCE.md)
2. Run [Pipeline pattern](patterns/01_pipeline.yaml)
3. Inspect the output and shared memory

### Intermediate (1 hour)
1. Read full [Pattern README](patterns/README.md)
2. Run all 4 patterns with sample data
3. Customize one pattern for your use case

### Advanced (2+ hours)
1. Study pattern implementations in detail
2. Combine patterns for hybrid workflows
3. Build production workflows for your domain

---

## 🔧 Installation

### Prerequisites
```bash
# Install Loom CLI
curl -sSL https://install.loom.ai | bash

# Verify installation
loom --version
```

### Clone Examples
```bash
git clone https://github.com/loom-ai/workflow-examples.git
cd workflow-examples
```

### Run Your First Workflow
```bash
# Try the simplest pattern
loom weave patterns/01_pipeline.yaml

# Check execution
loom status

# View results
loom results
```

---

## 💡 Key Concepts

### 1. Agents
Specialized AI workers with expertise:
```yaml
agents:
  - id: data_analyst
    role: Senior Data Analyst
    goal: Extract insights from data
    backstory: Expert in statistical analysis...
```

### 2. Tasks
Work units executed by agents:
```yaml
tasks:
  - id: analyze_data
    description: Analyze the dataset and identify trends
    context:
      - extract_data  # Depends on this task
```

### 3. Shared Memory
Data exchange between agents:
```python
# Write data
shared_memory_write(key="results", value=data)

# Read data
data = shared_memory_read(key="results")
```

---

## 🏆 Best Practices

### ✅ DO
- **Start with existing patterns** - Don't reinvent the wheel
- **Define clear contracts** - Document inputs/outputs per task
- **Use descriptive keys** - Shared memory naming matters
- **Test incrementally** - Validate each task separately
- **Include error handling** - Expect and handle failures

### ❌ DON'T
- **Create circular dependencies** - Tasks can't depend on themselves
- **Hardcode values** - Use configuration and inputs
- **Skip validation** - Always verify data quality
- **Overload agents** - Keep responsibilities focused
- **Ignore performance** - Monitor and optimize

---

## 🎯 Real-World Examples

### Data Engineering
```bash
# ETL Pipeline
loom weave patterns/01_pipeline.yaml \
  --set source="database://prod" \
  --set target="warehouse://analytics"
```

### Code Review
```bash
# Multi-perspective review
loom weave patterns/03_fork_join.yaml \
  --set code_submission="$(cat code.py)"
```

### Market Research
```bash
# Hierarchical research report
loom weave patterns/04_hierarchical.yaml \
  --set topic="AI in Healthcare" \
  --set depth="comprehensive"
```

### Performance Testing
```bash
# Parallel test execution
loom weave patterns/02_parallel.yaml \
  --set test_suite="integration_tests"
```

---

## 📊 Pattern Comparison

| Pattern | Concurrency | Best For | Complexity |
|---------|-------------|----------|------------|
| **Pipeline** | Sequential | Data transformation | ⭐ Simple |
| **Parallel** | Maximum | Independent tasks | ⭐ Simple |
| **Fork-Join** | High | Multi-perspective | ⭐⭐ Medium |
| **Hierarchical** | Varies | Complex research | ⭐⭐⭐ Advanced |

---

## 🛠️ Customization

All patterns are templates - adapt them to your needs:

### 1. Change Data Sources
```yaml
description: |
  Extract data from YOUR_SOURCE.
  Connection: YOUR_CONNECTION_STRING
  Store in shared memory under key 'raw_data'.
```

### 2. Modify Agent Expertise
```yaml
agents:
  - id: domain_expert
    role: YOUR_DOMAIN Expert
    backstory: |
      Specialized knowledge in YOUR_AREA...
```

### 3. Adjust Workflow Logic
```yaml
tasks:
  - id: custom_task
    description: YOUR_CUSTOM_LOGIC
    context:
      - prerequisite_task
```

---

## 🐛 Troubleshooting

### Tasks Not Running
```bash
# Check workflow validation
loom weave patterns/01_pipeline.yaml --validate

# Verify dependencies
loom graph patterns/01_pipeline.yaml
```

### Wrong Execution Order
- Review `context` dependencies in tasks
- Ensure no circular dependencies
- Verify workflow `type` is correct

### Performance Issues
- Profile task execution times
- Parallelize independent tasks
- Optimize data transfer between agents
- Use pagination for large datasets

---

## 📚 Additional Resources

### Documentation
- **[INDEX.md](INDEX.md)** - Complete navigation
- **[QUICK_REFERENCE.md](patterns/QUICK_REFERENCE.md)** - One-page overview
- **[Pattern README](patterns/README.md)** - Detailed documentation

### Community
- **GitHub Discussions** - Ask questions
- **Slack Channel** - Real-time help
- **Blog** - Deep dives and tutorials

### Support
- **Issues**: Report bugs or request features
- **Email**: support@loom.ai
- **Docs**: https://docs.loom.ai

---

## 🤝 Contributing

We welcome contributions!

### Adding Patterns
1. Create workflow YAML with clear documentation
2. Add to `patterns/` directory
3. Update documentation files
4. Submit PR with examples and tests

### Improving Examples
- Better use cases
- Domain-specific customizations
- Performance optimizations
- Documentation improvements

---

## 📄 License

MIT License - See [LICENSE](LICENSE) for details

---

## 🎉 Get Started Now!

1. **Browse**: Check out [INDEX.md](INDEX.md) for navigation
2. **Learn**: Read [patterns/README.md](patterns/README.md) for details
3. **Run**: Execute `loom weave patterns/01_pipeline.yaml`
4. **Customize**: Adapt patterns to your use case
5. **Build**: Create amazing workflows!

**Questions?** Check [INDEX.md](INDEX.md) or reach out to the community.

**Ready to build?** Start with the [Pipeline pattern](patterns/01_pipeline.yaml)!
