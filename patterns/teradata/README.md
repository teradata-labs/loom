# Teradata Pattern Library

This directory contains Teradata-specific patterns for analytics, machine learning, data preparation, text analytics, AI functions, vector search, geospatial, and more.

**Pattern count**: 83 patterns across 15 categories

## Directory Structure

### Foundational Knowledge
- **rom/** - Read-Only Memory (foundational Teradata knowledge)
  - Core database concepts (join indexes, TableKind types)
  - System behaviors and limitations
  - Prerequisites for Teradata development
  - **Start here** for foundational knowledge

- **core/** - Core SQL patterns
  - `data_types_casting.yaml` - Teradata types (VECTOR, Vector32, JSON), CAST patterns
  - `sql_basics.yaml` - Teradata dialect differences (QUALIFY, REPLACE VIEW, SAMPLE, MINUS)

### Domain-Specific Patterns

- **analytics/** (7 patterns) - Aggregations, metrics, reporting, path analysis
  - nPath, Sessionize, Attribution, Funnel, Churn, Customer Health, Resource Utilization

- **data_discovery/** (8 patterns) - Schema exploration, key detection, profiling
  - Signature Generation, Key/FK Detection, Column Similarity, Domain Discovery

- **data_quality/** (5 patterns) - Validation, quality checks, profiling
  - Data Profiling, Validation, Duplicate Detection, Missing Values, Outliers

- **data_prep/** (6 patterns) - Feature engineering with Fit/Transform architecture
  - Scale/Transform, One-Hot Encoding, BinCode, Column Transformer, SMOTE, Fit/Transform Pattern

- **ml/** (12 patterns) - Machine learning with Teradata ML Engine
  - XGBoost, Decision Forest, GLM, SVM, Naive Bayes, KNN, Linear/Logistic Regression,
    Decision Tree, K-Means, Anomaly Detection (OneClassSVM), ML Pipeline Patterns

- **model_evaluation/** (5 patterns) - Model assessment and explainability
  - Train/Test Split, Classification Evaluator, Regression Evaluator, ROC, SHAP

- **text/** (5 patterns) - Native text analysis and NLP
  - N-Gram, Text Classifier, Sentiment, NER, TF-IDF

- **ai_functions/** (6 patterns) - LLM-powered in-database analytics
  - AI Sentiment, AI AskLLM, AI Text Classifier, AI PII Masking, AI Summarize, Authorization Objects

- **vector_search/** (4 patterns) - Embeddings and similarity search
  - Vector Distance (exact NN), HNSW (approximate NN), Embeddings, Vector Normalize

- **geospatial/** (3 patterns) - Spatial analytics
  - Geometry Basics, Spatial Relationships, Spatial Operations

- **hypothesis_testing/** (2 patterns) - Statistical tests
  - ANOVA, Statistical Tests (Chi-Square, F-Test, Z-Test)

- **association/** (2 patterns) - Market basket and recommendations
  - Apriori (frequent itemsets), Collaborative Filtering

- **byom/** (2 patterns) - Bring Your Own Models
  - Model Loading (PMML, H2O, ONNX), Model Scoring

- **timeseries/** (5 patterns) - Time series, forecasting, and signal processing
  - ARIMA (with UAF pipeline), Moving Average (with UAF), Holt-Winters, UAF Concepts, Signal Processing

- **performance/** (6 patterns) - Query optimization and metadata
  - EXPLAIN Analysis, Query Tuning, Statistics Collection, PI Skew Detection, Spool Space, Catalog Views

## Getting Started

### For New Teradata Developers
1. **Read ROM first**: `rom/README.md` - Understand foundational concepts
2. **Review core patterns**: `core/` - SQL basics and data types
3. **Explore domain patterns**: Choose patterns for your use case

### For Agents
ROM patterns should be loaded into system prompts for:
- Table discovery workflows (Stage 2 agents)
- Connectivity validation (Stage 3 agents)
- Query generation (Stage 4-7 agents)
- Any agent working with DBC system tables

### Common Workflows

| Workflow | Pattern Sequence |
|----------|-----------------|
| **Classification** | data_prep → model_evaluation/train_test_split → ml/xgboost → model_evaluation/classification_evaluator |
| **Regression** | data_prep → model_evaluation/train_test_split → ml/glm → model_evaluation/regression_evaluator |
| **Clustering** | data_prep/scale_transform → ml/kmeans → model_evaluation (Silhouette) |
| **Text Analytics** | text/ngram → text/tfidf → text/text_classifier |
| **LLM Analytics** | ai_functions/authorization_objects → ai_functions/ai_sentiment |
| **RAG / Semantic Search** | ai_functions/authorization_objects → vector_search/embeddings → vector_search/hnsw |
| **Time Series** | timeseries/uaf_concepts → timeseries/arima → timeseries/holt_winters |
| **Path Analysis** | analytics/sessionize → analytics/npath → analytics/attribution |

## Pattern Libraries

Aggregated pattern libraries are available in `patterns/libraries/`:
- `teradata-ml.yaml` - 12 ML patterns
- `teradata-data-prep.yaml` - 6 data preparation patterns
- `teradata-model-eval.yaml` - 5 model evaluation patterns
- `teradata-analytics.yaml` - 10 analytics patterns
- `teradata-data-quality.yaml` - 5 data quality patterns
- `teradata-text-analytics.yaml` - 11 text + AI patterns
- `teradata-vector-search.yaml` - 4 vector/embedding patterns

## Skills

Teradata-specific skills are available in `skills/`:
- `teradata-sql-analytics.yaml` - Master skill with native function guide (`/td-analytics`)
- `teradata-ml-pipeline.yaml` - ML workflow guidance (`/td-ml`)
- `teradata-data-prep.yaml` - Data preparation workflow (`/td-prep`)
- `teradata-vector-rag.yaml` - RAG/vector search workflow (`/td-rag`)

## ROM Philosophy

ROM (Read-Only Memory) contains **foundational facts** that:
- Don't change frequently (core database architecture)
- Apply broadly across all Teradata work
- Prevent common mistakes (like querying join indexes)
- Serve as prerequisites for advanced patterns

Think of ROM as the "manual" that comes before the "cookbook."

## Contributing

When adding patterns:
1. **Foundational knowledge** -> `rom/` (markdown docs)
2. **Reusable SQL patterns** -> Domain directories (YAML files)
3. **Use existing pattern format** from `patterns/README.md`
4. **Test patterns** before committing

See [/patterns/README.md](../README.md) for pattern format details.

---

**Principle**: Understand the foundation (ROM) before applying patterns.
