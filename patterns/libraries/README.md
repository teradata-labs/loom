# Pattern Libraries

Pattern libraries bundle individual patterns into loadable collections for agent configuration.

## Available Libraries

### Teradata Analytics (`teradata-analytics.yaml`)
**Domain:** teradata
**Patterns:** 7

Advanced analytics patterns for Teradata Vantage:
- **npath** - Sequence analysis for user journeys and clickstream patterns
- **sessionize** - Group events into sessions with timeout-based segmentation
- **funnel_analysis** - Multi-step conversion funnel tracking with drop-offs
- **attribution** - Multi-touch attribution for marketing and customer journey analysis
- **churn_analysis** - Customer churn prediction and retention analysis
- **customer_health_scoring** - Engagement and satisfaction scoring
- **resource_utilization** - System resource usage and capacity planning

**Use cases:** E-commerce analytics, user journey mapping, conversion optimization, customer retention

---

### Teradata ML (`teradata-ml.yaml`)
**Domain:** teradata
**Patterns:** 4

Machine learning patterns using Teradata Vantage ML Engine:
- **linear_regression** - Predict continuous numeric values (sales, prices, demand)
- **logistic_regression** - Binary classification (churn, conversion, fraud)
- **kmeans** - Clustering for customer segmentation and pattern discovery
- **decision_tree** - Interpretable classification with rule extraction

**Use cases:** Predictive analytics, customer segmentation, fraud detection, forecasting

---

### Teradata Data Quality (`teradata-data-quality.yaml`)
**Domain:** teradata
**Patterns:** 5

Data quality and cleansing patterns:
- **data_profiling** - Column statistics, distributions, completeness metrics
- **data_validation** - Business rule validation and constraint checking
- **duplicate_detection** - Exact, key-based, and fuzzy duplicate identification
- **missing_value_analysis** - NULL analysis and imputation strategies
- **outlier_detection** - Statistical anomaly detection (Z-score, IQR, percentiles)

**Use cases:** Data quality assessment, ETL validation, master data management

---

### SQL Core (`sql-core.yaml`)
**Domain:** sql
**Patterns:** 8

Database-agnostic SQL patterns using standard SQL:

**Data Quality (5 patterns):**
- data_profiling, data_validation, duplicate_detection, missing_value_analysis, outlier_detection

**Timeseries (2 patterns):**
- **moving_average** - Rolling statistics and smoothing
- **arima** - Time series forecasting setup

**Text (1 pattern):**
- **ngram** - Text tokenization and n-gram generation

**Use cases:** Portable SQL patterns for any database, data quality checks, timeseries analysis

---

### Postgres Optimization (`postgres-optimization.yaml`)
**Domain:** postgres
**Patterns:** 12

Query optimization and performance tuning for PostgreSQL:

**Index Optimization:**
- **sequential_scan_detection** - Find slow table scans
- **missing_index_analysis** - Recommend missing indexes

**Query Optimization:**
- **join_optimization** - Optimize JOIN operations
- **query_rewrite** - Rewrite inefficient queries
- **subquery_to_join** - Convert correlated subqueries to JOINs
- **like_pattern_optimization** - Optimize text search patterns
- **count_optimization** - Fast COUNT(*) alternatives
- **distinct_elimination** - Remove unnecessary DISTINCT

**Maintenance:**
- **vacuum_recommendation** - VACUUM and ANALYZE recommendations
- **partition_recommendation** - Table partitioning strategies

**Data Quality:**
- **foreign_key_validation** - Check referential integrity
- **data_type_optimization** - Optimize column data types for storage

**Use cases:** PostgreSQL performance tuning, query optimization, database maintenance

---

## Usage in Agent Configuration

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-teradata-agent
spec:
  backend:
    config_file: examples/backends/teradata.yaml

  # Load pattern libraries
  pattern_libraries:
    - patterns/libraries/teradata-analytics.yaml
    - patterns/libraries/teradata-ml.yaml
    - patterns/libraries/teradata-data-quality.yaml

  system_prompt: |
    You have access to Teradata pattern libraries covering analytics,
    machine learning, and data quality. Use patterns to generate
    accurate SQL based on user requests.
```

## Loading Patterns Programmatically

```go
import "github.com/teradata-labs/loom/pkg/patterns"

// Load a pattern library
lib, err := patterns.LoadPatternLibrary("patterns/libraries/teradata-analytics.yaml")
if err != nil {
    log.Fatal(err)
}

// Access patterns
for _, pattern := range lib.Spec.Entries {
    fmt.Printf("Pattern: %s (priority: %d)\n", pattern.Name, pattern.Priority)
    fmt.Printf("Description: %s\n", pattern.Description)
    fmt.Printf("Tags: %v\n", pattern.Tags)
}
```

## Pattern Metadata

Each pattern entry includes:
- **name** - Unique pattern identifier
- **description** - What the pattern does
- **trigger_conditions** - When to apply this pattern
- **example** - Reference to detailed pattern file with SQL templates
- **priority** - Pattern importance (0-100, higher = more important)
- **tags** - Categorization for filtering

## Pattern Priority Levels

- **90-100**: Critical patterns (e.g., npath, duplicate_detection, sequential_scan_detection)
- **80-89**: High-value patterns (e.g., sessionize, logistic_regression, join_optimization)
- **70-79**: Common patterns (e.g., attribution, missing_value_analysis, partition_recommendation)
- **60-69**: Specialized patterns (e.g., customer_health_scoring, arima)
- **50-59**: Advanced/niche patterns (e.g., ngram, data_type_optimization)

## Pattern Selection Strategy

Agents should select patterns based on:
1. **Trigger condition matching** - Does user query match pattern triggers?
2. **Priority** - Higher priority patterns applied first
3. **Domain match** - Use domain-specific patterns when available
4. **Tag filtering** - Filter by category (analytics, ml, data-quality, etc.)

## Detailed Pattern Documentation

Each pattern references a detailed YAML file in `patterns/<domain>/<category>/` with:
- Multiple SQL templates (discovery, validation, basic, advanced)
- Parameters with LLM hints
- Comprehensive examples
- Common errors and solutions
- Best practices
- Related patterns

Example: `patterns/teradata/analytics/npath.yaml` (602 lines)

## Pattern Library Statistics

| Library | Domain | Patterns | Priority Range | Categories |
|---------|--------|----------|----------------|------------|
| teradata-analytics | teradata | 7 | 60-90 | analytics |
| teradata-ml | teradata | 4 | 70-85 | ml |
| teradata-data-quality | teradata | 5 | 70-90 | data-quality |
| sql-core | sql | 8 | 55-85 | data-quality, timeseries, text |
| postgres-optimization | postgres | 12 | 55-95 | performance, optimization |
| **Total** | | **36** | | |

## Contributing New Patterns

1. Create detailed pattern file in `patterns/<domain>/<category>/pattern-name.yaml`
2. Add pattern entry to appropriate library file
3. Test with `pkg/patterns/loader_test.go`
4. Update this README with pattern description

## Pattern Library Design

Pattern libraries use the `loom/v1` API:
- **apiVersion**: `loom/v1`
- **kind**: `PatternLibrary`
- **metadata**: Library name, version, domain, description
- **spec.entries**: Array of pattern entries

See `pkg/patterns/loader.go` for implementation details.
