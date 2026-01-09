# Teradata Pattern Library

This directory contains Teradata-specific patterns for analytics, machine learning, and data quality workflows.

## Directory Structure

### Foundational Knowledge
- **rom/** - Read-Only Memory (foundational Teradata knowledge)
  - Core database concepts (join indexes, TableKind types)
  - System behaviors and limitations
  - Prerequisites for Teradata development
  - **Start here** for foundational knowledge

### Domain-Specific Patterns
- **analytics/** - Aggregations, metrics, reporting patterns
- **data_quality/** - Validation, quality checks, profiling
- **ml/** - Machine learning workflows (regression, classification)
- **text/** - Text analysis and NLP patterns
- **timeseries/** - Time-series analysis patterns (ARIMA, moving averages)

## Getting Started

### For New Teradata Developers
1. **Read ROM first**: `rom/README.md` - Understand foundational concepts
2. **Review join indexes**: `rom/join-indexes.md` - Critical for table discovery
3. **Study TableKind types**: `rom/tablekind-reference.md` - All database object types
4. **Explore patterns**: Choose domain-specific patterns for your use case

### For Agents
ROM patterns should be loaded into system prompts for:
- Table discovery workflows (Stage 2 agents)
- Connectivity validation (Stage 3 agents)
- Query generation (Stage 4-7 agents)
- Any agent working with DBC system tables

## Pattern Categories

### Analytics Patterns
Advanced aggregation, windowing, and reporting patterns specific to Teradata SQL.

### Data Quality Patterns
Validation rules, profiling queries, outlier detection, and quality checks.

### Machine Learning Patterns
ML workflows using Teradata's built-in functions and external integrations.

### Text Patterns
Natural language processing and text analysis patterns.

### Time Series Patterns
Temporal analysis including ARIMA, moving averages, and forecasting.

## ROM Philosophy

ROM (Read-Only Memory) contains **foundational facts** that:
- Don't change frequently (core database architecture)
- Apply broadly across all Teradata work
- Prevent common mistakes (like querying join indexes)
- Serve as prerequisites for advanced patterns

Think of ROM as the "manual" that comes before the "cookbook."

## Contributing

When adding patterns:
1. **Foundational knowledge** → `rom/` (markdown docs)
2. **Reusable SQL patterns** → Domain directories (YAML files)
3. **Use existing pattern format** from `patterns/README.md`
4. **Test patterns** before committing

See [/patterns/README.md](../../README.md) for pattern format details.

---

**Principle**: Understand the foundation (ROM) before applying patterns.
