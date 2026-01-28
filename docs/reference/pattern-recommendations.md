
# Pattern Recommendations Reference

**Version**: v1.0.0-beta.1

Complete reference for Loom's 65 built-in patterns across 11 categories - pattern selection by use case, backend type, difficulty level, and comprehensive pattern catalog.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Pattern Categories](#pattern-categories)
- [Pattern Selection Guide](#pattern-selection-guide)
- [Pattern by Use Case](#pattern-by-use-case)
- [Pattern by Backend Type](#pattern-by-backend-type)
- [Pattern by Difficulty](#pattern-by-difficulty)
- [Complete Pattern Catalog](#complete-pattern-catalog)
- [Pattern Library API](#pattern-library-api)
- [Best Practices](#best-practices)
- [Error Codes](#error-codes)
- [See Also](#see-also)


## Quick Reference

### Pattern Categories Summary

| Category | Pattern Count | Backend Type | Difficulty Range | Use Cases |
|----------|---------------|--------------|------------------|-----------|
| **postgres/analytics** | 12 | PostgreSQL | Beginner - Advanced | Query optimization, performance analysis, index tuning |
| **teradata/analytics** | 6 | Teradata | Intermediate - Advanced | Customer analytics, funnel analysis, churn prediction |
| **teradata/ml** | 4 | Teradata | Intermediate - Advanced | Machine learning, regression, clustering, classification |
| **sql/data_quality** | 5 | SQL (Generic) | Beginner - Intermediate | Data profiling, duplicate detection, outlier analysis |
| **sql/timeseries** | 2 | SQL (Generic) | Intermediate | Time series forecasting, trend analysis |
| **sql/text** | 1 | SQL (Generic) | Intermediate | Text processing, n-gram analysis |
| **prompt_engineering** | 4 | LLM | Beginner - Intermediate | Reasoning, structured output, hallucination prevention |
| **text** | 2 | LLM | Beginner | Sentiment analysis, summarization |
| **code** | 2 | LLM | Beginner - Intermediate | Test generation, documentation |
| **debugging** | 1 | LLM | Intermediate | Root cause analysis |
| **vision** | 2 | LLM (Vision) | Intermediate | Chart interpretation, form extraction |
| **evaluation** | 1 | LLM | Intermediate | Prompt quality evaluation |
| **documents** | 4 | LLM | Beginner - Intermediate | CSV/PDF/Excel processing, document Q&A |
| **libraries** | 11 | N/A | N/A | Pattern library definitions |

**Total Patterns**: 65 (54 executable patterns + 11 library definitions)


### Pattern Selection Decision Tree

```
START: What do you need to accomplish?

├─ Query Performance Issue?
│  ├─ PostgreSQL → postgres/analytics patterns
│  │  ├─ Slow queries → sequential_scan_detection, missing_index_analysis
│  │  ├─ Join issues → join_optimization, join_elimination
│  │  └─ Table design → normalization_denormalization_analysis
│  └─ Teradata → teradata/analytics patterns
│     └─ Customer behavior → sessionize, funnel_analysis, churn_analysis
│
├─ Data Quality Issue?
│  └─ sql/data_quality patterns
│     ├─ Duplicates → duplicate_detection
│     ├─ Outliers → outlier_detection
│     ├─ Missing data → missing_value_analysis
│     └─ Data validation → data_validation, data_profiling
│
├─ Machine Learning?
│  └─ teradata/ml patterns
│     ├─ Regression → linear_regression, logistic_regression
│     ├─ Clustering → kmeans
│     └─ Classification → decision_tree
│
├─ Time Series Analysis?
│  └─ sql/timeseries patterns
│     ├─ Trends → moving_average
│     └─ Forecasting → arima
│
├─ Text Processing?
│  ├─ SQL-based → sql/text patterns (ngram)
│  └─ LLM-based → text patterns
│     ├─ Sentiment → sentiment_analysis
│     └─ Summarization → summarization
│
├─ Code Tasks?
│  └─ code patterns
│     ├─ Testing → test_generation
│     └─ Documentation → doc_generation
│
├─ LLM Reasoning?
│  └─ prompt_engineering patterns
│     ├─ Multi-step reasoning → chain_of_thought
│     ├─ Few examples → few_shot_learning
│     ├─ Structured output → structured_output
│     └─ Reduce hallucinations → hallucination_prevention
│
├─ Visual Data?
│  └─ vision patterns
│     ├─ Charts/graphs → chart_interpretation
│     └─ Forms/PDFs → form_extraction
│
├─ Document Processing?
│  └─ documents patterns
│     ├─ CSV → csv_import
│     ├─ Excel → excel_analysis
│     ├─ PDF → pdf_extraction
│     └─ Q&A → document_qa
│
└─ Debugging/Evaluation?
   ├─ Root cause → debugging patterns (root_cause_analysis)
   └─ Prompt quality → evaluation patterns (prompt_evaluation)
```


## Overview

Loom includes **65 built-in patterns** across **11 categories** to guide agents in solving domain-specific problems. Patterns provide:

- **Structured problem-solving**: Templates for common tasks
- **Domain expertise**: Best practices for SQL, ML, text processing
- **Error prevention**: Common pitfalls and solutions
- **Performance optimization**: Query tuning, cost reduction
- **Cross-referencing**: Related patterns for complex workflows

**Implementation**: `pkg/patterns/library.go`
**Storage**: `patterns/` directory (YAML files)
**Available Since**: v0.6.0


## Pattern Categories

### 1. postgres/analytics (12 patterns)

**Purpose**: PostgreSQL query optimization and performance analysis

**Backend**: PostgreSQL database
**Difficulty**: Beginner - Advanced
**Use Cases**: Slow query diagnosis, index optimization, join tuning, partition management

**Patterns**:
1. **sequential_scan_detection** - Identify full table scans hurting performance
2. **missing_index_analysis** - Find missing indexes causing slow queries
3. **join_optimization** - Optimize JOIN operations (type, order, statistics)
4. **join_elimination** - Remove unnecessary JOINs
5. **subquery_optimization** - Convert subqueries to JOINs or CTEs
6. **cte_optimization** - Optimize Common Table Expressions
7. **partition_pruning_analysis** - Verify partition pruning works
8. **materialized_view_recommendation** - Suggest materialized views for expensive queries
9. **vacuum_analyze_recommendations** - Optimize VACUUM and ANALYZE schedules
10. **query_plan_analysis** - Deep EXPLAIN ANALYZE interpretation
11. **index_bloat_detection** - Identify bloated indexes
12. **normalization_denormalization_analysis** - Table design optimization

**Example Usage**:
```yaml
# Find missing indexes causing slow queries
pattern: postgres/analytics/missing_index_analysis
parameters:
  database: production
  min_query_time_ms: 1000
  analyze_last_n_days: 7
```


### 2. teradata/analytics (6 patterns)

**Purpose**: Advanced analytics using Teradata-specific functions

**Backend**: Teradata database
**Difficulty**: Intermediate - Advanced
**Use Cases**: Customer journey analysis, marketing attribution, churn prediction

**Patterns**:
1. **sessionize** - Group events into user sessions with SESSIONIZE()
2. **funnel_analysis** - Multi-step conversion funnel with nPath
3. **attribution** - Marketing attribution modeling (first-touch, last-touch, multi-touch)
4. **churn_analysis** - Customer churn prediction and analysis
5. **customer_health_scoring** - Calculate customer health scores
6. **resource_utilization** - Teradata resource usage analysis (CPU, I/O, spool)

**Example Usage**:
```yaml
# Track user journeys from landing to purchase
pattern: teradata/analytics/sessionize
parameters:
  events_table: web_events
  user_id_column: user_id
  timestamp_column: event_time
  timeout_minutes: 30
```


### 3. teradata/ml (4 patterns)

**Purpose**: Machine learning using Teradata SQL-MapReduce functions

**Backend**: Teradata database
**Difficulty**: Intermediate - Advanced
**Use Cases**: Predictive modeling, clustering, classification

**Patterns**:
1. **linear_regression** - Linear regression with GLML1L2()
2. **logistic_regression** - Logistic regression for binary classification
3. **kmeans** - K-means clustering with KMeans()
4. **decision_tree** - Decision tree classifier with DecisionTree()

**Example Usage**:
```yaml
# Predict sales based on marketing spend
pattern: teradata/ml/linear_regression
parameters:
  training_table: historical_sales
  target_column: revenue
  feature_columns: [marketing_spend, season, region]
  test_split: 0.2
```


### 4. sql/data_quality (5 patterns)

**Purpose**: Generic SQL data quality checks

**Backend**: SQL (PostgreSQL, Teradata, any ANSI SQL)
**Difficulty**: Beginner - Intermediate
**Use Cases**: Data validation, anomaly detection, data profiling

**Patterns**:
1. **data_profiling** - Generate comprehensive data profile (nulls, cardinality, distribution)
2. **duplicate_detection** - Find duplicate records
3. **outlier_detection** - Identify statistical outliers
4. **missing_value_analysis** - Analyze missing data patterns
5. **data_validation** - Validate data against business rules

**Example Usage**:
```yaml
# Profile a new data source
pattern: sql/data_quality/data_profiling
parameters:
  table_name: new_customer_data
  columns: [age, income, credit_score]
  sample_size: 10000
```


### 5. sql/timeseries (2 patterns)

**Purpose**: Time series analysis with SQL

**Backend**: SQL (any with window functions)
**Difficulty**: Intermediate
**Use Cases**: Forecasting, trend analysis, moving averages

**Patterns**:
1. **moving_average** - Calculate moving averages (SMA, EMA)
2. **arima** - ARIMA forecasting (where supported)

**Example Usage**:
```yaml
# Forecast next month's sales
pattern: sql/timeseries/moving_average
parameters:
  table_name: daily_sales
  time_column: sale_date
  value_column: total_sales
  window_size: 7
  forecast_periods: 30
```


### 6. sql/text (1 pattern)

**Purpose**: SQL-based text processing

**Backend**: SQL
**Difficulty**: Intermediate
**Use Cases**: Text tokenization, n-gram analysis

**Patterns**:
1. **ngram** - Generate n-grams from text columns

**Example Usage**:
```yaml
# Extract bigrams from product reviews
pattern: sql/text/ngram
parameters:
  table_name: product_reviews
  text_column: review_text
  n: 2
```


### 7. prompt_engineering (4 patterns)

**Purpose**: LLM reasoning and output structuring patterns

**Backend**: LLM (any provider)
**Difficulty**: Beginner - Intermediate
**Use Cases**: Complex reasoning, structured output, hallucination reduction

**Patterns**:
1. **chain_of_thought** - Step-by-step reasoning for complex problems
2. **few_shot_learning** - Learn from examples
3. **structured_output** - Generate JSON, YAML, or other structured formats
4. **hallucination_prevention** - Techniques to reduce LLM hallucinations

**Example Usage**:
```yaml
# Use chain-of-thought for multi-step reasoning
pattern: prompt_engineering/chain_of_thought
parameters:
  problem_type: mathematical
  require_explicit_steps: true
  verify_answer: true
```

**Chain-of-Thought Templates**:
- **basic**: "Let's think step by step..."
- **explicit_thinking**: "Before answering, I'll think through this..."
- **guided_steps**: Structured steps (1. Understand, 2. Plan, 3. Execute, 4. Verify)
- **hypothesis_testing**: Scientific method approach
- **mathematical**: Math-specific reasoning (given/find/plan/solve/check)


### 8. text (2 patterns)

**Purpose**: LLM-based text processing

**Backend**: LLM
**Difficulty**: Beginner
**Use Cases**: Sentiment analysis, summarization

**Patterns**:
1. **sentiment_analysis** - Analyze text sentiment (positive/negative/neutral)
2. **summarization** - Generate text summaries

**Example Usage**:
```yaml
# Analyze customer feedback sentiment
pattern: text/sentiment_analysis
parameters:
  text: "The product is great but shipping was slow."
  include_score: true
  include_aspects: true
```


### 9. code (2 patterns)

**Purpose**: Code generation and documentation

**Backend**: LLM
**Difficulty**: Beginner - Intermediate
**Use Cases**: Test generation, documentation, code explanation

**Patterns**:
1. **test_generation** - Generate unit tests for code
2. **doc_generation** - Generate code documentation

**Example Usage**:
```yaml
# Generate tests for a function
pattern: code/test_generation
parameters:
  language: python
  code: |
    def calculate_total(items, tax_rate):
        subtotal = sum(item.price for item in items)
        return subtotal * (1 + tax_rate)
  framework: pytest
```


### 10. debugging (1 pattern)

**Purpose**: Error analysis and debugging assistance

**Backend**: LLM
**Difficulty**: Intermediate
**Use Cases**: Root cause analysis, error explanation

**Patterns**:
1. **root_cause_analysis** - Analyze errors and suggest fixes

**Example Usage**:
```yaml
# Analyze a database error
pattern: debugging/root_cause_analysis
parameters:
  error_message: "ERROR: relation 'users' does not exist"
  context: "Occurred after schema migration"
  system: "PostgreSQL 14"
```


### 11. vision (2 patterns)

**Purpose**: Visual data interpretation with vision-enabled LLMs

**Backend**: LLM (with vision capabilities)
**Difficulty**: Intermediate
**Use Cases**: Chart analysis, form extraction, OCR

**Patterns**:
1. **chart_interpretation** - Extract data and insights from charts/graphs
2. **form_extraction** - Extract structured data from forms/documents

**Example Usage**:
```yaml
# Extract data from a chart image
pattern: vision/chart_interpretation
parameters:
  image_path: "/path/to/sales_chart.png"
  extract_data: true
  generate_insights: true
```


### 12. evaluation (1 pattern)

**Purpose**: LLM output quality evaluation

**Backend**: LLM
**Difficulty**: Intermediate
**Use Cases**: Prompt testing, output validation

**Patterns**:
1. **prompt_evaluation** - Evaluate prompt effectiveness

**Example Usage**:
```yaml
# Evaluate a prompt's quality
pattern: evaluation/prompt_evaluation
parameters:
  prompt: "Summarize the following text..."
  test_inputs: [...]
  evaluation_criteria: [accuracy, conciseness, clarity]
```


### 13. documents (4 patterns)

**Purpose**: Document processing and Q&A

**Backend**: LLM
**Difficulty**: Beginner - Intermediate
**Use Cases**: CSV/Excel/PDF import, document question answering

**Patterns**:
1. **csv_import** - Import and analyze CSV files
2. **excel_analysis** - Analyze Excel spreadsheets
3. **pdf_extraction** - Extract text and data from PDFs
4. **document_qa** - Question answering over documents

**Example Usage**:
```yaml
# Import CSV and generate SQL
pattern: documents/csv_import
parameters:
  csv_path: "/path/to/sales.csv"
  table_name: "sales_data"
  generate_schema: true
```


## Pattern Selection Guide

### By Task Type

#### Performance Optimization
**Symptoms**: Slow queries, high CPU, long response times

**Pattern Selection**:
1. **Identify backend type**:
   - PostgreSQL → Start with `postgres/analytics/sequential_scan_detection`
   - Teradata → Start with `teradata/analytics/resource_utilization`

2. **Run diagnostic patterns**:
   ```yaml
   # PostgreSQL: Find root cause
   - postgres/analytics/missing_index_analysis
   - postgres/analytics/join_optimization
   - postgres/analytics/query_plan_analysis

   # Fix issues
   - postgres/analytics/index_bloat_detection (if indexes exist)
   - postgres/analytics/vacuum_analyze_recommendations (if statistics stale)
   - postgres/analytics/partition_pruning_analysis (if using partitions)
   ```

3. **Optimize further**:
   - Consider materialized views: `postgres/analytics/materialized_view_recommendation`
   - Consider denormalization: `postgres/analytics/normalization_denormalization_analysis`


#### Data Quality Checks
**Symptoms**: Unexpected nulls, duplicates, data anomalies

**Pattern Workflow**:
```yaml
# Step 1: Profile data
pattern: sql/data_quality/data_profiling
# Generates: null %, cardinality, min/max/avg, distribution

# Step 2: Targeted checks based on profile
- sql/data_quality/duplicate_detection (if duplicates suspected)
- sql/data_quality/outlier_detection (if distribution shows extremes)
- sql/data_quality/missing_value_analysis (if high null %)
- sql/data_quality/data_validation (validate business rules)
```


#### Customer Analytics
**Symptoms**: Need to understand user behavior, conversion, churn

**Pattern Workflow**:
```yaml
# Step 1: Sessionize events
pattern: teradata/analytics/sessionize
# Groups events into user sessions

# Step 2: Analyze conversions
pattern: teradata/analytics/funnel_analysis
# Tracks multi-step conversion funnels

# Step 3: Predict churn
pattern: teradata/analytics/churn_analysis
# Identifies at-risk customers

# Step 4: Attribute conversions
pattern: teradata/analytics/attribution
# Marketing attribution modeling
```


#### Machine Learning
**Symptoms**: Need predictive models

**Pattern Selection by ML Task**:
- **Regression** (predict continuous value):
  - `teradata/ml/linear_regression` - Linear relationships

- **Classification** (predict category):
  - `teradata/ml/logistic_regression` - Binary classification
  - `teradata/ml/decision_tree` - Multi-class classification

- **Clustering** (group similar items):
  - `teradata/ml/kmeans` - K-means clustering

**Workflow**:
```yaml
# 1. Prepare data (clean, feature engineering)
pattern: sql/data_quality/data_profiling
pattern: sql/data_quality/missing_value_analysis

# 2. Train model
pattern: teradata/ml/linear_regression  # or logistic_regression, etc.

# 3. Evaluate
pattern: evaluation/prompt_evaluation
```


#### Text Processing
**Symptoms**: Need to analyze text data

**Pattern Selection**:
- **Sentiment analysis**: `text/sentiment_analysis` (LLM-based)
- **Summarization**: `text/summarization` (LLM-based)
- **N-gram analysis**: `sql/text/ngram` (SQL-based)

**When to use SQL vs LLM**:
- **SQL text patterns**: Fast, structured output, limited to tokenization
- **LLM text patterns**: Slow, semantic understanding, flexible


#### LLM Reasoning
**Symptoms**: LLM gives wrong answers, lacks reasoning, hallucinates

**Pattern Selection**:
1. **Complex reasoning needed** → `prompt_engineering/chain_of_thought`
   - Mathematical problems
   - Multi-step analysis
   - Causal reasoning

2. **Need structured output** → `prompt_engineering/structured_output`
   - Generate JSON, YAML, SQL
   - Consistent format required

3. **Few examples available** → `prompt_engineering/few_shot_learning`
   - Show 2-5 examples
   - Learn from demonstrations

4. **Hallucination issues** → `prompt_engineering/hallucination_prevention`
   - Ask for citations
   - Verify against facts
   - Use uncertainty expressions


### By Difficulty Level

#### Beginner Patterns (Easy to Use)
- **sql/data_quality/data_profiling** - Simple table profiling
- **sql/data_quality/duplicate_detection** - Find duplicates
- **text/sentiment_analysis** - Sentiment from text
- **text/summarization** - Summarize text
- **code/test_generation** - Generate tests
- **documents/csv_import** - Import CSV

**Characteristics**:
- Few parameters required
- Clear expected output
- Fast execution
- Minimal domain knowledge needed


#### Intermediate Patterns (Moderate Complexity)
- **postgres/analytics/missing_index_analysis** - Requires SQL knowledge
- **teradata/analytics/sessionize** - Requires event schema understanding
- **teradata/ml/linear_regression** - Requires ML basics
- **sql/timeseries/moving_average** - Requires time series knowledge
- **prompt_engineering/chain_of_thought** - Requires prompt engineering
- **vision/chart_interpretation** - Requires vision model

**Characteristics**:
- Multiple parameters with dependencies
- Requires domain knowledge
- May need iteration
- Moderate execution time


#### Advanced Patterns (Expert Level)
- **postgres/analytics/query_plan_analysis** - Deep EXPLAIN knowledge
- **teradata/analytics/funnel_analysis** - Complex nPath queries
- **teradata/analytics/attribution** - Multi-touch attribution modeling
- **teradata/ml/decision_tree** - ML hyperparameter tuning
- **postgres/analytics/normalization_denormalization_analysis** - Database design

**Characteristics**:
- Many parameters with complex interactions
- Expert domain knowledge required
- Often requires multiple pattern combinations
- Long execution time possible


## Pattern by Use Case

### E-Commerce

**Product Performance**:
```yaml
# Analyze top-selling products
- sql/data_quality/data_profiling (product_sales)
- sql/timeseries/moving_average (sales trends)
- teradata/analytics/funnel_analysis (add-to-cart → purchase)
```

**Customer Behavior**:
```yaml
# Track customer journeys
- teradata/analytics/sessionize (website events)
- teradata/analytics/churn_analysis (predict churn)
- teradata/analytics/customer_health_scoring (health scores)
```

**Marketing Attribution**:
```yaml
# Attribute conversions to marketing channels
- teradata/analytics/attribution (multi-touch attribution)
- teradata/analytics/funnel_analysis (conversion funnels)
```


### Database Administration

**Query Optimization**:
```yaml
# PostgreSQL
- postgres/analytics/sequential_scan_detection
- postgres/analytics/missing_index_analysis
- postgres/analytics/join_optimization
- postgres/analytics/query_plan_analysis

# Teradata
- teradata/analytics/resource_utilization
```

**Maintenance**:
```yaml
# PostgreSQL
- postgres/analytics/vacuum_analyze_recommendations
- postgres/analytics/index_bloat_detection
- postgres/analytics/partition_pruning_analysis
```


### Data Science

**Exploratory Data Analysis**:
```yaml
- sql/data_quality/data_profiling
- sql/data_quality/outlier_detection
- sql/data_quality/missing_value_analysis
```

**Predictive Modeling**:
```yaml
- teradata/ml/linear_regression (regression)
- teradata/ml/logistic_regression (classification)
- teradata/ml/kmeans (clustering)
- teradata/ml/decision_tree (classification)
```

**Feature Engineering**:
```yaml
- sql/text/ngram (text features)
- sql/timeseries/moving_average (time series features)
```


### Customer Support

**Ticket Analysis**:
```yaml
- text/sentiment_analysis (customer sentiment)
- text/summarization (ticket summaries)
- documents/document_qa (knowledge base Q&A)
```

**Root Cause Analysis**:
```yaml
- debugging/root_cause_analysis (error analysis)
- sql/data_quality/outlier_detection (anomaly detection)
```


### Document Processing

**Data Import**:
```yaml
- documents/csv_import (CSV files)
- documents/excel_analysis (Excel files)
- documents/pdf_extraction (PDF files)
```

**Document Q&A**:
```yaml
- documents/document_qa (question answering)
- text/summarization (document summaries)
```

**Visual Data**:
```yaml
- vision/chart_interpretation (charts/graphs)
- vision/form_extraction (forms/documents)
```


### Software Development

**Code Quality**:
```yaml
- code/test_generation (unit tests)
- code/doc_generation (documentation)
- debugging/root_cause_analysis (bug analysis)
```

**Prompt Engineering**:
```yaml
- prompt_engineering/chain_of_thought (reasoning)
- prompt_engineering/few_shot_learning (examples)
- prompt_engineering/structured_output (JSON/YAML)
- evaluation/prompt_evaluation (quality checks)
```


## Pattern by Backend Type

### PostgreSQL Patterns (12)
All patterns in `postgres/analytics/`:
- sequential_scan_detection
- missing_index_analysis
- join_optimization
- join_elimination
- subquery_optimization
- cte_optimization
- partition_pruning_analysis
- materialized_view_recommendation
- vacuum_analyze_recommendations
- query_plan_analysis
- index_bloat_detection
- normalization_denormalization_analysis

**Requirements**:
- PostgreSQL 12+ (most patterns)
- PostgreSQL 14+ (some advanced features)
- `pg_stat_statements` extension (for query analysis)
- Sufficient privileges to read system catalogs


### Teradata Patterns (10)
**Analytics** (6 patterns):
- sessionize (SESSIONIZE function)
- funnel_analysis (nPath function)
- attribution (nPath + analytics)
- churn_analysis (ML + analytics)
- customer_health_scoring (aggregate analytics)
- resource_utilization (DBC system tables)

**Machine Learning** (4 patterns):
- linear_regression (GLML1L2 function)
- logistic_regression (GLML1L2 function)
- kmeans (KMeans function)
- decision_tree (DecisionTree function)

**Requirements**:
- Teradata 16.20+ (analytics functions)
- Teradata 17.00+ (ML functions)
- SQL-MapReduce license (ML patterns)
- TD_SYSFNLIB database access (ML functions)


### Generic SQL Patterns (8)
Patterns in `sql/data_quality/`, `sql/timeseries/`, `sql/text/`:
- data_profiling (ANSI SQL)
- duplicate_detection (ANSI SQL)
- outlier_detection (window functions)
- missing_value_analysis (ANSI SQL)
- data_validation (ANSI SQL)
- moving_average (window functions)
- arima (ANSI SQL + statistics)
- ngram (string functions)

**Requirements**:
- Any SQL database with window function support
- Some patterns require PostgreSQL, Teradata, or other specific features


### LLM Patterns (19)
Patterns in `prompt_engineering/`, `text/`, `code/`, `debugging/`, `vision/`, `evaluation/`, `documents/`:
- chain_of_thought
- few_shot_learning
- structured_output
- hallucination_prevention
- sentiment_analysis
- summarization
- test_generation
- doc_generation
- root_cause_analysis
- chart_interpretation (vision)
- form_extraction (vision)
- prompt_evaluation
- csv_import
- excel_analysis
- pdf_extraction
- document_qa

**Requirements**:
- LLM provider (Anthropic, Bedrock, Ollama, etc.)
- Vision-enabled LLM for vision patterns (Claude 4.5 Sonnet, GPT-4 Vision)
- Sufficient token limits (varies by pattern)


## Pattern by Difficulty

### Beginner (15 patterns)
**Characteristics**: Simple parameters, clear output, fast execution

**SQL**:
- sql/data_quality/data_profiling
- sql/data_quality/duplicate_detection
- postgres/analytics/sequential_scan_detection

**LLM**:
- text/sentiment_analysis
- text/summarization
- code/test_generation
- code/doc_generation
- documents/csv_import
- documents/excel_analysis
- documents/pdf_extraction
- documents/document_qa
- prompt_engineering/few_shot_learning
- prompt_engineering/structured_output


### Intermediate (30 patterns)
**Characteristics**: Moderate complexity, some domain knowledge needed

**SQL**:
- sql/data_quality/outlier_detection
- sql/data_quality/missing_value_analysis
- sql/data_quality/data_validation
- sql/timeseries/moving_average
- sql/timeseries/arima
- sql/text/ngram
- postgres/analytics/missing_index_analysis
- postgres/analytics/join_optimization
- postgres/analytics/subquery_optimization
- postgres/analytics/cte_optimization
- postgres/analytics/partition_pruning_analysis
- postgres/analytics/index_bloat_detection

**Teradata**:
- teradata/analytics/sessionize
- teradata/analytics/churn_analysis
- teradata/analytics/customer_health_scoring
- teradata/analytics/resource_utilization
- teradata/ml/linear_regression
- teradata/ml/logistic_regression
- teradata/ml/kmeans

**LLM**:
- prompt_engineering/chain_of_thought
- prompt_engineering/hallucination_prevention
- debugging/root_cause_analysis
- vision/chart_interpretation
- vision/form_extraction
- evaluation/prompt_evaluation


### Advanced (10 patterns)
**Characteristics**: Expert-level knowledge, complex parameters, long execution

**SQL**:
- postgres/analytics/join_elimination
- postgres/analytics/materialized_view_recommendation
- postgres/analytics/vacuum_analyze_recommendations
- postgres/analytics/query_plan_analysis
- postgres/analytics/normalization_denormalization_analysis

**Teradata**:
- teradata/analytics/funnel_analysis (nPath complexity)
- teradata/analytics/attribution (multi-touch modeling)
- teradata/ml/decision_tree (hyperparameter tuning)


## Complete Pattern Catalog

### postgres/analytics/sequential_scan_detection

**Purpose**: Identify full table scans causing performance issues

**Difficulty**: Beginner
**Backend**: PostgreSQL
**Execution Time**: <1 second

**When to Use**:
- Queries are slow
- High I/O load
- Need to identify missing indexes

**Parameters**:
- `database` (string, required): Database to analyze
- `min_seq_scan_count` (int, default: 100): Minimum sequential scans to report
- `min_table_size_mb` (int, default: 10): Minimum table size to consider

**Output**:
- Table name
- Sequential scan count
- Table size
- Index suggestions

**Related Patterns**: missing_index_analysis, query_plan_analysis


### postgres/analytics/missing_index_analysis

**Purpose**: Find missing indexes causing slow queries

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 1-5 seconds

**When to Use**:
- Queries are slow
- `pg_stat_statements` shows high execution time
- WHERE clauses not using indexes

**Parameters**:
- `database` (string, required): Database to analyze
- `min_query_time_ms` (int, default: 1000): Minimum query time to analyze
- `analyze_last_n_days` (int, default: 7): Days of query history to analyze

**Output**:
- Missing index recommendations
- Table/column combinations
- Expected performance improvement

**Related Patterns**: sequential_scan_detection, join_optimization


### postgres/analytics/join_optimization

**Purpose**: Optimize JOIN operations (type, order, statistics)

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 5-30 seconds

**When to Use**:
- Queries with multiple JOINs are slow
- JOIN order matters for performance
- Need to choose JOIN type (NESTED LOOP, HASH, MERGE)

**Parameters**:
- `query` (string, required): Query to optimize
- `explain_analyze` (bool, default: true): Run EXPLAIN ANALYZE
- `suggest_rewrite` (bool, default: true): Suggest query rewrites

**Output**:
- JOIN type recommendations
- JOIN order suggestions
- Statistics freshness check

**Related Patterns**: join_elimination, query_plan_analysis


### postgres/analytics/join_elimination

**Purpose**: Remove unnecessary JOINs from queries

**Difficulty**: Advanced
**Backend**: PostgreSQL
**Execution Time**: 5-10 seconds

**When to Use**:
- Queries have JOINs but don't use joined columns
- Foreign key relationships allow JOIN removal
- Performance optimization needed

**Parameters**:
- `query` (string, required): Query to analyze
- `check_foreign_keys` (bool, default: true): Use foreign keys for elimination

**Output**:
- Simplified query
- Performance improvement estimate

**Related Patterns**: join_optimization, subquery_optimization


### postgres/analytics/subquery_optimization

**Purpose**: Convert subqueries to JOINs or CTEs

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 5-10 seconds

**When to Use**:
- Queries have correlated subqueries
- Subqueries execute multiple times
- Need to materialize subquery results

**Parameters**:
- `query` (string, required): Query with subqueries
- `prefer_cte` (bool, default: false): Prefer CTEs over JOINs

**Output**:
- Rewritten query (JOIN or CTE)
- Performance comparison

**Related Patterns**: cte_optimization, join_optimization


### postgres/analytics/cte_optimization

**Purpose**: Optimize Common Table Expressions

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 5-10 seconds

**When to Use**:
- Queries use CTEs
- CTEs are not materialized correctly
- Need to inline or materialize CTEs

**Parameters**:
- `query` (string, required): Query with CTEs
- `materialize` (string, default: "auto"): Materialization strategy (auto/always/never)

**Output**:
- Optimized CTE usage
- Materialization recommendations

**Related Patterns**: subquery_optimization, materialized_view_recommendation


### postgres/analytics/partition_pruning_analysis

**Purpose**: Verify partition pruning works correctly

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 1-5 seconds

**When to Use**:
- Using table partitioning
- Queries scan more partitions than expected
- Need to verify pruning logic

**Parameters**:
- `table_name` (string, required): Partitioned table
- `query` (string, required): Query to analyze

**Output**:
- Partitions scanned
- Pruning effectiveness
- Constraint suggestions

**Related Patterns**: query_plan_analysis


### postgres/analytics/materialized_view_recommendation

**Purpose**: Suggest materialized views for expensive queries

**Difficulty**: Advanced
**Backend**: PostgreSQL
**Execution Time**: 10-30 seconds

**When to Use**:
- Queries are expensive and run frequently
- Data doesn't change frequently
- Can tolerate stale data

**Parameters**:
- `min_query_time_ms` (int, default: 5000): Minimum query time
- `min_execution_count` (int, default: 100): Minimum query frequency
- `max_staleness_hours` (int, default: 24): Maximum acceptable staleness

**Output**:
- Materialized view recommendations
- Refresh strategy
- Estimated storage cost

**Related Patterns**: cte_optimization, vacuum_analyze_recommendations


### postgres/analytics/vacuum_analyze_recommendations

**Purpose**: Optimize VACUUM and ANALYZE schedules

**Difficulty**: Advanced
**Backend**: PostgreSQL
**Execution Time**: 5-10 seconds

**When to Use**:
- Query performance degraded over time
- Statistics are stale
- Table bloat suspected

**Parameters**:
- `database` (string, required): Database to analyze
- `check_bloat` (bool, default: true): Check for bloat

**Output**:
- VACUUM recommendations
- ANALYZE recommendations
- Bloat report

**Related Patterns**: index_bloat_detection


### postgres/analytics/query_plan_analysis

**Purpose**: Deep EXPLAIN ANALYZE interpretation

**Difficulty**: Advanced
**Backend**: PostgreSQL
**Execution Time**: Varies by query

**When to Use**:
- Need detailed query execution analysis
- Query performance issues
- Understand query plan choices

**Parameters**:
- `query` (string, required): Query to analyze
- `buffers` (bool, default: true): Include buffer usage
- `timing` (bool, default: true): Include timing information

**Output**:
- Detailed plan breakdown
- Bottleneck identification
- Optimization suggestions

**Related Patterns**: join_optimization, sequential_scan_detection


### postgres/analytics/index_bloat_detection

**Purpose**: Identify bloated indexes needing rebuild

**Difficulty**: Intermediate
**Backend**: PostgreSQL
**Execution Time**: 5-10 seconds

**When to Use**:
- Index size larger than expected
- Query performance degraded
- After many UPDATEs/DELETEs

**Parameters**:
- `database` (string, required): Database to analyze
- `min_bloat_pct` (int, default: 30): Minimum bloat percentage to report

**Output**:
- Bloated indexes
- Bloat percentage
- REINDEX recommendations

**Related Patterns**: vacuum_analyze_recommendations


### postgres/analytics/normalization_denormalization_analysis

**Purpose**: Analyze table design (normalization vs denormalization)

**Difficulty**: Advanced
**Backend**: PostgreSQL
**Execution Time**: 10-30 seconds

**When to Use**:
- Schema design review
- Frequent JOINs hurting performance
- Considering denormalization

**Parameters**:
- `tables` (array, required): Tables to analyze
- `analyze_joins` (bool, default: true): Analyze JOIN patterns

**Output**:
- Normalization level
- Denormalization recommendations
- Trade-off analysis

**Related Patterns**: join_optimization, materialized_view_recommendation


### teradata/analytics/sessionize

**Purpose**: Group events into user sessions with SESSIONIZE()

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 10-60 seconds

**When to Use**:
- Analyze user behavior
- Track session duration
- Session-based analytics

**Parameters**:
- `events_table` (string, required): Table with event data
- `user_id_column` (string, required): User identifier column
- `timestamp_column` (string, required): Event timestamp column
- `timeout_minutes` (int, default: 30): Session timeout

**Output**:
- Session IDs
- Session duration
- Events per session

**Related Patterns**: funnel_analysis, churn_analysis


### teradata/analytics/funnel_analysis

**Purpose**: Multi-step conversion funnel analysis with nPath

**Difficulty**: Advanced
**Backend**: Teradata
**Execution Time**: 30-300 seconds

**When to Use**:
- Track conversion funnels
- Identify drop-off points
- Optimize user flows

**Parameters**:
- `events_table` (string, required): Events table
- `user_id_column` (string, required): User ID
- `event_column` (string, required): Event type column
- `timestamp_column` (string, required): Timestamp
- `funnel_steps` (array, required): Sequence of events

**Output**:
- Conversion rates
- Drop-off analysis
- Path patterns

**Related Patterns**: sessionize, attribution


### teradata/analytics/attribution

**Purpose**: Marketing attribution modeling

**Difficulty**: Advanced
**Backend**: Teradata
**Execution Time**: 60-300 seconds

**When to Use**:
- Measure marketing channel effectiveness
- Multi-touch attribution
- ROI analysis

**Parameters**:
- `touchpoints_table` (string, required): Marketing touchpoints
- `conversions_table` (string, required): Conversions
- `attribution_model` (string, required): Model (first_touch, last_touch, linear, time_decay)

**Output**:
- Channel attribution
- Conversion credit
- Model comparison

**Related Patterns**: funnel_analysis, sessionize


### teradata/analytics/churn_analysis

**Purpose**: Customer churn prediction and analysis

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 60-300 seconds

**When to Use**:
- Identify at-risk customers
- Predict churn probability
- Retention strategies

**Parameters**:
- `customer_table` (string, required): Customer data
- `activity_table` (string, required): Activity data
- `churn_definition_days` (int, default: 90): Days of inactivity = churn

**Output**:
- Churn predictions
- Risk scores
- Feature importance

**Related Patterns**: customer_health_scoring, sessionize


### teradata/analytics/customer_health_scoring

**Purpose**: Calculate customer health scores

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 30-120 seconds

**When to Use**:
- Track customer engagement
- Identify expansion opportunities
- Predict churn

**Parameters**:
- `customer_table` (string, required): Customer data
- `usage_table` (string, required): Usage metrics
- `score_components` (array, required): Score components (usage, satisfaction, growth)

**Output**:
- Health scores (0-100)
- Component breakdown
- Segment classification

**Related Patterns**: churn_analysis


### teradata/analytics/resource_utilization

**Purpose**: Teradata resource usage analysis (CPU, I/O, spool)

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 5-30 seconds

**When to Use**:
- Performance troubleshooting
- Capacity planning
- Query optimization

**Parameters**:
- `time_range_hours` (int, default: 24): Analysis time range
- `top_n_queries` (int, default: 20): Top resource consumers

**Output**:
- CPU usage
- I/O usage
- Spool usage
- Top queries

**Related Patterns**: postgres/analytics/query_plan_analysis


### teradata/ml/linear_regression

**Purpose**: Linear regression with GLML1L2()

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 60-600 seconds

**When to Use**:
- Predict continuous values
- Linear relationships
- Simple regression models

**Parameters**:
- `training_table` (string, required): Training data
- `target_column` (string, required): Target variable
- `feature_columns` (array, required): Feature columns
- `test_split` (float, default: 0.2): Train/test split ratio

**Output**:
- Model coefficients
- R-squared score
- Predictions

**Related Patterns**: logistic_regression, kmeans


### teradata/ml/logistic_regression

**Purpose**: Logistic regression for binary classification

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 60-600 seconds

**When to Use**:
- Binary classification
- Probability predictions
- Churn prediction

**Parameters**:
- `training_table` (string, required): Training data
- `target_column` (string, required): Binary target (0/1)
- `feature_columns` (array, required): Features

**Output**:
- Model coefficients
- Accuracy, precision, recall
- Predictions

**Related Patterns**: linear_regression, decision_tree


### teradata/ml/kmeans

**Purpose**: K-means clustering with KMeans()

**Difficulty**: Intermediate
**Backend**: Teradata
**Execution Time**: 60-600 seconds

**When to Use**:
- Customer segmentation
- Anomaly detection
- Grouping similar items

**Parameters**:
- `data_table` (string, required): Data to cluster
- `feature_columns` (array, required): Features for clustering
- `n_clusters` (int, required): Number of clusters

**Output**:
- Cluster assignments
- Cluster centers
- Within-cluster sum of squares

**Related Patterns**: linear_regression, decision_tree


### teradata/ml/decision_tree

**Purpose**: Decision tree classifier with DecisionTree()

**Difficulty**: Advanced
**Backend**: Teradata
**Execution Time**: 60-600 seconds

**When to Use**:
- Multi-class classification
- Non-linear relationships
- Interpretable models

**Parameters**:
- `training_table` (string, required): Training data
- `target_column` (string, required): Target variable
- `feature_columns` (array, required): Features
- `max_depth` (int, default: 5): Maximum tree depth

**Output**:
- Tree structure
- Feature importance
- Accuracy metrics

**Related Patterns**: logistic_regression, kmeans


### sql/data_quality/data_profiling

**Purpose**: Generate comprehensive data profile

**Difficulty**: Beginner
**Backend**: SQL (generic)
**Execution Time**: 5-60 seconds

**When to Use**:
- New data source exploration
- Data quality baseline
- Before analysis

**Parameters**:
- `table_name` (string, required): Table to profile
- `columns` (array, optional): Columns to profile (default: all)
- `sample_size` (int, optional): Sample size (default: full table)

**Output**:
- Null percentages
- Cardinality
- Min/max/avg values
- Distribution summaries

**Related Patterns**: missing_value_analysis, outlier_detection


### sql/data_quality/duplicate_detection

**Purpose**: Find duplicate records

**Difficulty**: Beginner
**Backend**: SQL (generic)
**Execution Time**: 5-30 seconds

**When to Use**:
- Data validation
- Before deduplication
- Data quality checks

**Parameters**:
- `table_name` (string, required): Table to check
- `key_columns` (array, required): Columns defining uniqueness

**Output**:
- Duplicate count
- Duplicate records
- Deduplication SQL

**Related Patterns**: data_profiling, data_validation


### sql/data_quality/outlier_detection

**Purpose**: Identify statistical outliers

**Difficulty**: Intermediate
**Backend**: SQL (with window functions)
**Execution Time**: 10-60 seconds

**When to Use**:
- Anomaly detection
- Data quality checks
- Before ML training

**Parameters**:
- `table_name` (string, required): Table to analyze
- `column` (string, required): Numeric column
- `method` (string, default: "iqr"): Detection method (iqr, zscore)
- `threshold` (float, default: 1.5): Outlier threshold

**Output**:
- Outlier records
- Outlier percentage
- Distribution statistics

**Related Patterns**: data_profiling, missing_value_analysis


### sql/data_quality/missing_value_analysis

**Purpose**: Analyze missing data patterns

**Difficulty**: Intermediate
**Backend**: SQL (generic)
**Execution Time**: 5-30 seconds

**When to Use**:
- Data quality assessment
- Before imputation
- Data completeness report

**Parameters**:
- `table_name` (string, required): Table to analyze
- `columns` (array, optional): Columns to check

**Output**:
- Missing value counts
- Missing percentage
- Patterns (MCAR, MAR, MNAR)

**Related Patterns**: data_profiling, data_validation


### sql/data_quality/data_validation

**Purpose**: Validate data against business rules

**Difficulty**: Intermediate
**Backend**: SQL (generic)
**Execution Time**: 5-60 seconds

**When to Use**:
- Enforce business rules
- Data quality gates
- Before data processing

**Parameters**:
- `table_name` (string, required): Table to validate
- `rules` (array, required): Validation rules

**Output**:
- Validation results
- Rule violations
- Pass/fail summary

**Related Patterns**: data_profiling, duplicate_detection


### sql/timeseries/moving_average

**Purpose**: Calculate moving averages (SMA, EMA)

**Difficulty**: Intermediate
**Backend**: SQL (with window functions)
**Execution Time**: 5-30 seconds

**When to Use**:
- Trend analysis
- Smoothing noisy data
- Feature engineering

**Parameters**:
- `table_name` (string, required): Time series data
- `time_column` (string, required): Time column
- `value_column` (string, required): Value column
- `window_size` (int, required): Window size (days/rows)
- `ma_type` (string, default: "sma"): Moving average type (sma, ema)

**Output**:
- Moving averages
- Trend direction

**Related Patterns**: arima


### sql/timeseries/arima

**Purpose**: ARIMA forecasting

**Difficulty**: Intermediate
**Backend**: SQL (where supported)
**Execution Time**: 30-300 seconds

**When to Use**:
- Time series forecasting
- Seasonal data
- Trend prediction

**Parameters**:
- `table_name` (string, required): Historical data
- `time_column` (string, required): Time column
- `value_column` (string, required): Value to forecast
- `forecast_periods` (int, required): Periods to forecast
- `p` (int, default: 1): AR order
- `d` (int, default: 1): Differencing order
- `q` (int, default: 1): MA order

**Output**:
- Forecasted values
- Confidence intervals

**Related Patterns**: moving_average


### sql/text/ngram

**Purpose**: Generate n-grams from text columns

**Difficulty**: Intermediate
**Backend**: SQL
**Execution Time**: 10-60 seconds

**When to Use**:
- Text tokenization
- Feature engineering
- Text analysis

**Parameters**:
- `table_name` (string, required): Table with text
- `text_column` (string, required): Text column
- `n` (int, required): N-gram size (1=unigram, 2=bigram, etc.)

**Output**:
- N-grams
- Frequency counts

**Related Patterns**: text/sentiment_analysis


### prompt_engineering/chain_of_thought

**Purpose**: Step-by-step reasoning for complex problems

**Difficulty**: Intermediate
**Backend**: LLM
**Execution Time**: 5-30 seconds

**When to Use**:
- Complex reasoning needed
- Multi-step problems
- Mathematical problems

**Parameters**:
- `problem_type` (string, optional): Problem type (mathematical, logical, analytical)
- `require_explicit_steps` (bool, default: false): Require step enumeration
- `verify_answer` (bool, default: false): Self-verify answer

**Templates**:
- `basic`: "Let's think step by step..."
- `explicit_thinking`: "Before answering, I'll think through this..."
- `guided_steps`: Structured 4-step process
- `hypothesis_testing`: Scientific method
- `mathematical`: Math-specific (given/find/plan/solve/check)

**Output**:
- Reasoning steps
- Final answer

**Related Patterns**: few_shot_learning, structured_output


### prompt_engineering/few_shot_learning

**Purpose**: Learn from examples

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 3-15 seconds

**When to Use**:
- Consistent output format needed
- Examples available
- Pattern recognition

**Parameters**:
- `examples` (array, required): 2-5 examples (input/output pairs)
- `task_description` (string, optional): Task description

**Output**:
- Prediction following example pattern

**Related Patterns**: structured_output


### prompt_engineering/structured_output

**Purpose**: Generate JSON, YAML, or other structured formats

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 3-15 seconds

**When to Use**:
- Need JSON/YAML output
- Consistent format required
- Programmatic processing

**Parameters**:
- `output_format` (string, required): Format (json, yaml, sql, markdown)
- `schema` (object, optional): JSON schema for validation

**Output**:
- Structured data in requested format

**Related Patterns**: few_shot_learning, chain_of_thought


### prompt_engineering/hallucination_prevention

**Purpose**: Reduce LLM hallucinations

**Difficulty**: Intermediate
**Backend**: LLM
**Execution Time**: 5-20 seconds

**When to Use**:
- Factual accuracy critical
- Hallucination issues observed
- Need citations

**Techniques**:
- Ask for citations
- Verify against facts
- Use uncertainty expressions
- Request sources
- Chain-of-thought verification

**Output**:
- Factual response with citations

**Related Patterns**: chain_of_thought, evaluation/prompt_evaluation


### text/sentiment_analysis

**Purpose**: Analyze text sentiment

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 2-10 seconds

**When to Use**:
- Customer feedback analysis
- Review analysis
- Social media monitoring

**Parameters**:
- `text` (string, required): Text to analyze
- `include_score` (bool, default: false): Include numerical score (-1 to 1)
- `include_aspects` (bool, default: false): Aspect-based sentiment

**Output**:
- Sentiment (positive/negative/neutral)
- Score (if requested)
- Aspect sentiment (if requested)

**Related Patterns**: summarization, documents/document_qa


### text/summarization

**Purpose**: Generate text summaries

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 3-20 seconds

**When to Use**:
- Long documents
- Quick overview needed
- Content condensation

**Parameters**:
- `text` (string, required): Text to summarize
- `max_length` (int, optional): Maximum summary length (words)
- `style` (string, default: "concise"): Summary style (concise, detailed, bullet_points)

**Output**:
- Summary

**Related Patterns**: sentiment_analysis, documents/document_qa


### code/test_generation

**Purpose**: Generate unit tests for code

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 5-20 seconds

**When to Use**:
- Need test coverage
- New code written
- Regression testing

**Parameters**:
- `language` (string, required): Programming language
- `code` (string, required): Code to test
- `framework` (string, optional): Test framework (pytest, jest, junit)

**Output**:
- Unit tests

**Related Patterns**: doc_generation, debugging/root_cause_analysis


### code/doc_generation

**Purpose**: Generate code documentation

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 3-15 seconds

**When to Use**:
- Undocumented code
- Need docstrings
- API documentation

**Parameters**:
- `language` (string, required): Programming language
- `code` (string, required): Code to document
- `style` (string, optional): Doc style (google, numpy, jsdoc)

**Output**:
- Documentation

**Related Patterns**: test_generation


### debugging/root_cause_analysis

**Purpose**: Analyze errors and suggest fixes

**Difficulty**: Intermediate
**Backend**: LLM
**Execution Time**: 5-20 seconds

**When to Use**:
- Error occurred
- Need root cause
- Fix suggestions

**Parameters**:
- `error_message` (string, required): Error message
- `context` (string, optional): Additional context
- `system` (string, optional): System/language

**Output**:
- Root cause analysis
- Fix suggestions
- Prevention strategies

**Related Patterns**: code/test_generation


### vision/chart_interpretation

**Purpose**: Extract data and insights from charts/graphs

**Difficulty**: Intermediate
**Backend**: LLM (vision)
**Execution Time**: 5-20 seconds

**When to Use**:
- Charts/graphs in images
- Data extraction from visuals
- Chart insights

**Parameters**:
- `image_path` (string, required): Path to image
- `extract_data` (bool, default: true): Extract data points
- `generate_insights` (bool, default: true): Generate insights

**Output**:
- Data points
- Chart insights
- Trend analysis

**Related Patterns**: form_extraction


### vision/form_extraction

**Purpose**: Extract structured data from forms/documents

**Difficulty**: Intermediate
**Backend**: LLM (vision)
**Execution Time**: 5-30 seconds

**When to Use**:
- Forms in images
- OCR + structure extraction
- Data entry automation

**Parameters**:
- `image_path` (string, required): Path to form image
- `fields` (array, optional): Expected fields
- `output_format` (string, default: "json"): Output format

**Output**:
- Extracted fields
- Structured data

**Related Patterns**: chart_interpretation, documents/pdf_extraction


### evaluation/prompt_evaluation

**Purpose**: Evaluate prompt effectiveness

**Difficulty**: Intermediate
**Backend**: LLM
**Execution Time**: 10-60 seconds

**When to Use**:
- Test prompt quality
- Compare prompts
- Optimize prompts

**Parameters**:
- `prompt` (string, required): Prompt to evaluate
- `test_inputs` (array, required): Test inputs
- `evaluation_criteria` (array, required): Criteria (accuracy, conciseness, clarity)

**Output**:
- Evaluation scores
- Improvement suggestions

**Related Patterns**: prompt_engineering/hallucination_prevention


### documents/csv_import

**Purpose**: Import and analyze CSV files

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 3-20 seconds

**When to Use**:
- CSV data import
- Schema generation
- Data type inference

**Parameters**:
- `csv_path` (string, required): Path to CSV file
- `table_name` (string, optional): Target table name
- `generate_schema` (bool, default: true): Generate SQL schema

**Output**:
- Schema definition
- Import SQL
- Data summary

**Related Patterns**: excel_analysis, pdf_extraction


### documents/excel_analysis

**Purpose**: Analyze Excel spreadsheets

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 5-30 seconds

**When to Use**:
- Excel data import
- Multi-sheet analysis
- Formula extraction

**Parameters**:
- `excel_path` (string, required): Path to Excel file
- `sheets` (array, optional): Sheets to analyze (default: all)

**Output**:
- Sheet summaries
- Data schemas
- Relationships

**Related Patterns**: csv_import, pdf_extraction


### documents/pdf_extraction

**Purpose**: Extract text and data from PDFs

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 5-30 seconds

**When to Use**:
- PDF data extraction
- Table extraction
- Document parsing

**Parameters**:
- `pdf_path` (string, required): Path to PDF file
- `extract_tables` (bool, default: true): Extract tables
- `extract_images` (bool, default: false): Extract images

**Output**:
- Extracted text
- Tables (if requested)
- Images (if requested)

**Related Patterns**: form_extraction, document_qa


### documents/document_qa

**Purpose**: Question answering over documents

**Difficulty**: Beginner
**Backend**: LLM
**Execution Time**: 5-20 seconds

**When to Use**:
- Document Q&A
- Knowledge base queries
- Document search

**Parameters**:
- `document` (string, required): Document text
- `question` (string, required): Question to answer
- `include_citations` (bool, default: false): Include citations

**Output**:
- Answer
- Citations (if requested)

**Related Patterns**: text/summarization, pdf_extraction


## Pattern Library API

### Loading Patterns

```go
import "github.com/teradata-labs/loom/pkg/patterns"

// Create library
lib, err := patterns.NewLibrary(
    patterns.WithEmbeddedPatterns(),
    patterns.WithFilesystemPatterns("/path/to/patterns"),
    patterns.WithTracer(tracer),
)

// Load specific pattern
pattern, err := lib.Load("postgres/analytics/missing_index_analysis")
if err != nil {
    log.Fatalf("Pattern not found: %v", err)
}

// Pattern contains:
// - name, title, description
// - category, difficulty, backend_type
// - use_cases, parameters, templates, examples
// - common_errors, best_practices, related_patterns
```


### Listing Patterns

```go
// List all patterns
patterns := lib.ListAll()

// Each PatternSummary contains:
// - Name, Title, Category
// - Difficulty, BackendType
// - Use case count
```

**Expected Output**:
```
Name: postgres/analytics/missing_index_analysis
Title: Missing Index Analysis
Category: analytics
Difficulty: intermediate
Backend: PostgreSQL
Use Cases: 3
```


### Filtering Patterns

```go
// Filter by category
analyticsPatterns := lib.FilterByCategory("analytics")

// Filter by backend
postgresPatterns := lib.FilterByBackendType("PostgreSQL")

// Filter by difficulty
beginnerPatterns := lib.FilterByDifficulty("beginner")
```


### Searching Patterns

```go
// Free-text search across pattern metadata
results := lib.Search("index optimization")

// Searches:
// - Pattern name
// - Title
// - Description
// - Use cases
// - Best practices
```


## Best Practices

### 1. Start with Diagnostic Patterns

Before optimizing, understand the problem:

```yaml
# PostgreSQL: Start with diagnostics
- postgres/analytics/sequential_scan_detection
- postgres/analytics/missing_index_analysis

# Then optimize based on findings
- postgres/analytics/join_optimization
- postgres/analytics/query_plan_analysis
```

**Why**: Avoid premature optimization.


### 2. Use Related Patterns

Patterns reference related patterns for complex workflows:

```yaml
# Customer analytics workflow
1. teradata/analytics/sessionize (group events)
2. teradata/analytics/funnel_analysis (track conversions)
3. teradata/analytics/attribution (assign credit)
4. teradata/analytics/churn_analysis (predict churn)
```

**Why**: Patterns are designed to work together.


### 3. Match Difficulty to Skill Level

| Your Skill Level | Start With | Avoid |
|-----------------|------------|-------|
| SQL Beginner | sql/data_quality/data_profiling | postgres/analytics/query_plan_analysis |
| SQL Intermediate | postgres/analytics/missing_index_analysis | teradata/analytics/funnel_analysis |
| SQL Expert | Any postgres/analytics pattern | N/A |
| ML Beginner | teradata/ml/linear_regression | teradata/ml/decision_tree |

**Why**: Advanced patterns require deep domain knowledge.


### 4. Verify Pattern Applicability

Before using a pattern, check:
- **Backend compatibility**: Does your database support required functions?
- **Data requirements**: Do you have the required tables/columns?
- **Prerequisites**: Are required extensions installed?

**Example**:
```yaml
# teradata/analytics/sessionize requires:
- Teradata 16.20+
- SESSIONIZE() function
- Event data with user_id, timestamp
```


### 5. Use Pattern Templates Progressively

Many patterns have multiple templates for progressive refinement:

```yaml
# teradata/analytics/funnel_analysis templates:
1. discovery - Explore event values
2. simple_funnel - Basic funnel
3. funnel_with_timing - Add timing analysis
4. funnel_with_paths - Full path analysis
```

**Why**: Start simple, add complexity as needed.


### 6. Combine SQL and LLM Patterns

Use SQL for structured data, LLM for unstructured:

```yaml
# Workflow: Analyze customer feedback
1. sql/data_quality/data_profiling (profile feedback data)
2. text/sentiment_analysis (analyze sentiment)
3. text/summarization (summarize common themes)
4. sql/data_quality/outlier_detection (find unusual feedback)
```


### 7. Test Patterns in Development First

Before production:
1. **Test with sample data**: Verify pattern works
2. **Check performance**: Measure execution time
3. **Validate output**: Ensure output meets expectations
4. **Review generated SQL**: Verify SQL is correct


### 8. Use Pattern Caching

The pattern library caches loaded patterns:

```go
// First load: Reads from filesystem
pattern1, _ := lib.Load("postgres/analytics/missing_index_analysis")

// Second load: Returns cached copy (fast)
pattern2, _ := lib.Load("postgres/analytics/missing_index_analysis")
```

**Why**: Avoid repeated filesystem reads.


### 9. Leverage Pattern Examples

Every pattern includes examples:

```yaml
examples:
  - name: "E-commerce Purchase Funnel"
    description: "Track users who view, add to cart, purchase"
    parameters:
      funnel_steps: ["product_view", "add_to_cart", "purchase"]
    expected_result: |
      Returns conversion rates for each step...
```

**Why**: Examples show real-world usage.


### 10. Read Common Errors Before Using

Patterns document common errors:

```yaml
common_errors:
  - error: "Symbol 'X' is not found in the data"
    cause: "Event values don't match actual data"
    solution: "Run discovery template first to see actual values"
```

**Why**: Avoid known pitfalls.


## Error Codes

### ERR_PATTERN_NOT_FOUND

**Code**: `pattern_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Pattern does not exist in library.

**Example**:
```
Error: pattern not found: postgres/analytics/non_existent_pattern
```

**Resolution**:
1. List available patterns: `lib.ListAll()`
2. Check pattern name spelling
3. Verify pattern exists in `patterns/` directory

**Retry behavior**: Not retryable (pattern doesn't exist)


### ERR_PATTERN_INVALID_YAML

**Code**: `pattern_invalid_yaml`
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `INTERNAL`

**Cause**: Pattern YAML file has syntax errors.

**Example**:
```
Error: failed to parse pattern YAML: yaml: line 42: mapping values are not allowed in this context
```

**Resolution**:
1. Validate YAML syntax
2. Check for indentation issues
3. Verify all required fields present

**Retry behavior**: Not retryable (fix YAML)


### ERR_PATTERN_MISSING_REQUIRED_FIELD

**Code**: `pattern_missing_required_field`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Pattern YAML missing required field.

**Example**:
```
Error: pattern missing required field: name
```

**Required Fields**:
- `name`
- `title`
- `description`
- `category`
- `difficulty`

**Resolution**: Add missing field to pattern YAML

**Retry behavior**: Not retryable (fix YAML)


### ERR_PATTERN_INVALID_CATEGORY

**Code**: `pattern_invalid_category`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Pattern category not recognized.

**Example**:
```
Error: invalid category: unknown_category
```

**Valid Categories**:
- analytics
- ml
- data_quality
- timeseries
- text
- prompt_engineering
- code
- debugging
- vision
- evaluation
- documents

**Resolution**: Use valid category

**Retry behavior**: Not retryable (fix category)


### ERR_PATTERN_INVALID_DIFFICULTY

**Code**: `pattern_invalid_difficulty`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Pattern difficulty not recognized.

**Example**:
```
Error: invalid difficulty: expert
```

**Valid Difficulties**:
- beginner
- intermediate
- advanced

**Resolution**: Use valid difficulty level

**Retry behavior**: Not retryable (fix difficulty)


### ERR_PATTERN_BACKEND_NOT_SUPPORTED

**Code**: `pattern_backend_not_supported`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Pattern requires backend not available.

**Example**:
```
Error: pattern requires Teradata backend but only PostgreSQL available
```

**Resolution**:
1. Check pattern `backend_type` field
2. Use pattern compatible with your backend
3. Or configure required backend

**Retry behavior**: Retryable after backend configuration


## See Also

### Reference Documentation
- [Backend Reference](./backend.md) - ExecutionBackend interface for pattern execution
- [LLM Provider Reference](./llm-providers.md) - LLM providers for LLM patterns
- [CLI Reference](./cli.md) - `looms pattern` commands

### Guides
- [Pattern Library Guide](../guides/pattern-library-guide.md) - Using patterns in agents
- [Custom Pattern Guide](../guides/custom-patterns.md) - Creating custom patterns
- [Agent Configuration Guide](../guides/agent-configuration.md) - Configure pattern-guided agents

### Architecture Documentation
- [Pattern System Architecture](../architecture/pattern-system.md) - Pattern system design

### External Resources
- [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/current/sql-explain.html) - Query plan analysis
- [Teradata nPath](https://docs.teradata.com/r/Teradata-VantageTM-SQL-Functions-Operators-Expressions-and-Predicates/March-2019/Ordered-Analytical-Functions/NPATH) - Sequence analysis
- [Teradata ML Functions](https://docs.teradata.com/r/Teradata-Machine-Learning-Engine-SQL-Functions/July-2021) - ML function reference
