
# Pattern System Architecture

Architecture of Loom's pattern system - hot-reloadable domain knowledge library with keyword-based search (plus optional LLM re-ranking), A/B testing, and pattern effectiveness tracking for continuous improvement.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.2.0


## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
  - [Pattern Library](#pattern-library)
  - [Hot-Reload System](#hot-reload-system)
  - [Pattern Orchestrator](#pattern-orchestrator)
  - [Search and Matching](#search-and-matching)
  - [A/B Testing](#ab-testing)
  - [Pattern Effectiveness Tracker](#pattern-effectiveness-tracker)
- [Key Interactions](#key-interactions)
  - [Pattern Loading](#pattern-loading)
  - [Hot-Reload Flow](#hot-reload-flow)
  - [Pattern Matching](#pattern-matching)
- [Data Structures](#data-structures)
- [Algorithms](#algorithms)
  - [Pattern Search](#pattern-search)
  - [Debounce Algorithm](#debounce-algorithm)
  - [A/B Test Selection](#ab-test-selection)
- [Design Trade-offs](#design-trade-offs)
- [Constraints and Limitations](#constraints-and-limitations)
- [Performance Characteristics](#performance-characteristics)
- [Concurrency Model](#concurrency-model)
- [Error Handling](#error-handling)
- [Security Considerations](#security-considerations)
- [Related Work](#related-work)
- [References](#references)
- [Further Reading](#further-reading)


## Overview

The Pattern System encodes **domain knowledge as reusable YAML patterns** that guide agent behavior without hardcoded prompts. It combines:

1. **Pattern Library**: Cache-backed pattern storage with embedded + filesystem sources
2. **Hot-Reload**: Zero-downtime pattern updates via fsnotify (89-143ms latency)
3. **Hybrid Search**: Keyword-based pattern matching with intent classification, plus optional LLM-based re-ranking for ambiguous cases
4. **A/B Testing**: Compare pattern variants for continuous improvement
5. **Effectiveness Tracking**: SQLite-backed metrics for learning agent feedback

The system supports **104 patterns across 26 search paths** (13 top-level domains + 13 vendor-specific subdirectories including Teradata, Postgres, SQL, and Weaver patterns).


## Design Goals

1. **Zero Downtime**: Pattern updates without server restart (hot-reload)
2. **Backend Agnostic**: Patterns for SQL, REST APIs, documents, MCP tools
3. **Continuous Improvement**: A/B testing + effectiveness tracking enable learning
4. **Fast Lookup**: <10ms pattern matching via in-memory cache
5. **Composable**: Patterns reference other patterns for modularity
6. **Observable**: Every pattern load, match, and update traced to Hawk

**Non-goals**:
- Real-time pattern generation (patterns are pre-authored YAML, not LLM-generated)
- Complex dependency resolution (patterns are independent, minimal cross-references)
- Version control integration (file system changes detected, but no git hooks)


## System Context

```mermaid
graph TB
    subgraph External["External Environment"]
        FS[Filesystem<br/>YAML patterns]
        Agent[Agent<br/>consumer]
        Learning[Learning Agent<br/>effectiveness]
        Hawk[Hawk<br/>tracing]
    end

    subgraph PatternSystem["Pattern System"]
        subgraph Library["Pattern Library (Cache + Load)"]
            Cache[Cache: map string Pattern<br/>in-memory]
            Sources[Sources: Embedded FS + Filesystem]
            Search[Search: Keyword matching + intent scoring]
        end

        subgraph HotReload["Hot-Reload System (fsnotify)"]
            FileWatch[File Watcher fsnotify]
            Debounce[Debounce 500ms]
            Reload[Reload atomic]
            Broadcast[Broadcast callback]
            FileWatch --> Debounce --> Reload --> Broadcast
        end

        subgraph Orchestrator["Pattern Orchestrator"]
            IntentClass[Intent Classification]
            ExecPlan[Execution Planning]
            Recommend[Pattern Recommendation]
            Routing[Routing Advice]
        end

        subgraph ABTest["A/B Testing + Effectiveness Tracking"]
            Variant[Variant Selection]
            Execution[Execution]
            Metrics[Metrics]
            Learn[Learn]
            Variant --> Execution --> Metrics --> Learn
        end
    end

    FS --> Library
    Agent --> Library
    Learning --> ABTest
    Hawk --> ABTest
```

**External Dependencies**:
- **Filesystem**: YAML pattern files (hot-reloadable)
- **Embedded FS**: Compiled-in patterns (fallback, immutable)
- **Agent Runtime**: Pattern consumer (ROM layer)
- **Learning Agent**: Pattern effectiveness tracking (SQLite)
- **Hawk**: Observability tracing


## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                         Pattern System                                       │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  Pattern Library                             │         │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         Pattern Cache (RWMutex protected)          │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  patternCache: map[string]*Pattern                │     │           │  │
│  │  │    Key: pattern_name                              │     │           │  │
│  │  │    Value: Pattern struct                          │     │           │  │
│  │  │                                                    │     │          │  │
│  │  │  patternIndex: []PatternSummary                   │     │           │  │
│  │  │    Lightweight metadata for search                │     │           │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         Pattern Sources (dual-mode)                │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  1. Embedded FS (embed.FS)                         │     │          │  │
│  │  │     - Compiled into binary                         │     │          │  │
│  │  │     - Immutable, always available                  │     │          │  │
│  │  │     - Fallback if filesystem unavailable           │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  2. Filesystem (patternsDir)                       │     │          │  │
│  │  │     - Hot-reloadable YAML files                    │     │          │  │
│  │  │     - Checked first (overrides embedded)           │     │          │  │
│  │  │     - Supports local development                   │     │          │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  │                                                              │         │  │
│  │  Search Paths (26 total: 13 top-level + 13 vendor):         │          │  │
│  │    Top-level: analytics, ml, timeseries, text,              │          │  │
│  │    data_quality, rest_api, document, etl,                   │          │  │
│  │    prompt_engineering, code, debugging, vision, evaluation  │          │  │
│  │    Vendor: teradata/*, postgres/analytics, sql/*, weaver    │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  Hot-Reload System                           │         │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         HotReloader (fsnotify wrapper)             │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  watcher: *fsnotify.Watcher                        │     │          │  │
│  │  │  debounceTimers: map[string]*time.Timer            │     │          │  │
│  │  │  config: HotReloadConfig                           │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  Debounce Logic:                                   │     │          │  │
│  │  │    - Default: 500ms                                │     │          │  │
│  │  │    - Handles rapid-fire edits                      │     │          │  │
│  │  │    - Per-file timer tracking                       │     │          │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  │                                                              │         │  │
│  │  Event Flow:                                                 │         │  │
│  │    fsnotify Event → Debounce → Validate → Reload → Callback │          │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  Pattern Orchestrator                        │         │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         Intent Classification                      │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  IntentCategories:                                 │     │          │  │
│  │  │    - schema_discovery                              │     │          │  │
│  │  │    - data_quality                                  │     │          │  │
│  │  │    - data_transform                                │     │          │  │
│  │  │    - analytics                                     │     │          │  │
│  │  │    - relationship_query                            │     │          │  │
│  │  │    - query_generation                              │     │          │  │
│  │  │    - document_search                               │     │          │  │
│  │  │    - api_call                                      │     │          │  │
│  │  │    - unknown                                       │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  Classifier: Pluggable IntentClassifierFunc        │     │          │  │
│  │  │    Default: Keyword-based heuristics               │     │          │  │
│  │  │    Custom: Backend-specific (e.g., Teradata ML)   │     │           │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         Execution Planning                         │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  ExecutionPlan:                                    │     │          │  │
│  │  │    - Intent: IntentCategory                        │     │          │  │
│  │  │    - Steps: []PlannedStep                          │     │          │  │
│  │  │    - Reasoning: string                             │     │          │  │
│  │  │    - PatternName: string (recommendation)          │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  Planner: Pluggable ExecutionPlannerFunc           │     │          │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │                  A/B Testing + Learning                      │         │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         A/B Testing (VariantSelector)               │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  Variant Selection Strategies:                     │     │          │  │
│  │  │    - Explicit (forced variant)                     │     │          │  │
│  │  │    - Hash-based (deterministic per session)        │     │          │  │
│  │  │    - Random (uniform distribution)                 │     │          │  │
│  │  │    - Weighted (importance-based)                   │     │          │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  │                                                              │         │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │  │
│  │  │         Pattern Effectiveness Tracker              │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  SQLite DB:                                        │     │          │  │
│  │  │    - pattern_id                                    │     │          │  │
│  │  │    - success/failure counts                        │     │          │  │
│  │  │    - judge_scores (multi-dimensional)              │     │          │  │
│  │  │    - token_usage, latency                          │     │          │  │
│  │  │                                                    │     │          │  │
│  │  │  Metrics → Learning Agent → Pattern Tuning         │     │          │  │
│  │  └──────────────────────────────────────────────────────────────────┘  │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```


## Components

### Pattern Library

**Responsibility**: Cache-backed pattern storage with dual-source loading (embedded + filesystem).

**Core Structure** (`pkg/patterns/library.go`):
```go
type Library struct {
    mu               sync.RWMutex
    patternCache     map[string]*Pattern      // In-memory cache
    patternIndex     []PatternSummary         // Lightweight metadata
    indexInitialized bool                     // Whether index has been built
    embeddedFS       *embed.FS                // Compiled-in patterns
    patternsDir      string                   // Filesystem patterns
    searchPaths      []string                 // Domain directories
    pathCache        map[string]string        // Pattern name -> relative path
    tracer           observability.Tracer     // Hawk tracing
}
```

**Load Priority**:
```
1. Check cache (in-memory map)
   ├─ Hit → Return cached (< 1ms)                                               
   └─ Miss → Continue                                                           

2. Try embedded FS (compiled-in patterns)
   ├─ Found → Cache + Return                                                    
   └─ Not found → Continue                                                      

3. Try filesystem (hot-reloadable patterns)
   ├─ Found → Cache + Return                                                    
   └─ Not found → Error                                                         
```

**Rationale**:
- **In-memory cache**: Avoids repeated file I/O (99%+ cache hit rate after warm-up)
- **Embedded FS first**: Ensures patterns always available (no filesystem dependency); compiled into binary
- **Filesystem fallback**: Allows hot-reload during development; checked after embedded FS
- **RWMutex**: Optimizes for read-heavy workload (concurrent pattern lookups)

**Search Paths** (26 paths: 13 top-level + 13 vendor-specific):
```
patterns/
├── analytics/              # Business intelligence, aggregations
├── ml/                     # Machine learning patterns
├── timeseries/             # Time series analysis
├── text/                   # NLP patterns (sentiment, summarization)
├── data_quality/           # Validation, profiling, duplicates
├── rest_api/               # REST API interaction patterns
├── document/               # Document processing
├── etl/                    # Extract, Transform, Load
├── prompt_engineering/     # Universal meta-patterns (CoT, few-shot)
├── code/                   # Code generation, testing
├── debugging/              # Root cause analysis
├── vision/                 # Multimodal patterns (chart interpretation)
├── evaluation/             # Quality assurance patterns
├── teradata/analytics/     # Teradata-specific analytics (nPath, sessionize)
├── teradata/ml/            # Teradata ML functions
├── teradata/timeseries/    # Teradata time series
├── teradata/data_quality/  # Teradata data quality
├── teradata/data_loading/  # Teradata FastLoad, etc.
├── teradata/data_modeling/ # Teradata temporal tables, etc.
├── teradata/code_migration/# Macro to procedure migration
├── teradata/data_discovery/# Schema graph, column similarity
├── teradata/text/          # Teradata text analytics
├── teradata/performance/   # PI skew, spool space analysis
├── postgres/analytics/     # Postgres query optimization
├── sql/timeseries/         # Generic SQL time series
├── sql/data_quality/       # Generic SQL data quality
├── sql/text/               # Generic SQL text analytics
└── weaver/                 # Workflow patterns (debate, fork-join, etc.)
```

**Note**: Additional directories exist on the filesystem (fun, documents, libraries, nasa, loom) that are not in the default search paths but are discoverable via full filesystem indexing.

**Pattern Struct** (`pkg/patterns/types.go`):
```go
type Pattern struct {
    // Metadata
    Name            string   // Unique identifier
    Title           string   // Human-readable title
    Description     string   // What this pattern does
    Category        string   // Domain (analytics, ml, etc.)
    Difficulty      string   // beginner, intermediate, advanced
    BackendType     string   // sql, rest, document, etc.
    UseCases        []string // Example use cases
    RelatedPatterns []string // Cross-references

    // Definition
    Parameters      []Parameter         // Template variables
    Templates       map[string]Template // Named templates (basic, advanced)
    Examples        []Example           // Worked examples
    CommonErrors    []CommonError       // Known failure modes
    BestPractices   string              // Usage recommendations
    Syntax          *Syntax             // Backend-specific syntax docs
    BackendFunction string              // Backend-specific function name (e.g., nPath)
}
```


### Hot-Reload System

**Responsibility**: Detect filesystem changes and reload patterns without server restart.

**Core Architecture**:
```
Filesystem Change → fsnotify Event → Debounce → Validate → Reload → Broadcast
```

**HotReloader Struct** (`pkg/patterns/hotreload.go`):
```go
type HotReloader struct {
    library        *Library
    watcher        *fsnotify.Watcher          // File system watcher
    config         HotReloadConfig
    logger         *zap.Logger
    tracer         observability.Tracer
    debounceTimers map[string]*time.Timer     // Per-file timers
    debounceMu     sync.Mutex                 // Protects timer map
    stopCh         chan struct{}              // Shutdown signal
    doneCh         chan struct{}              // Completion signal
    stopped        bool                       // Shutdown flag
    stopMu         sync.Mutex                 // Protects stopped flag
}
```

**Debounce Logic**:
```
File Edit 1 ───▶ Start Timer (500ms)                                            
File Edit 2 ───▶ Reset Timer (500ms)                                            
File Edit 3 ───▶ Reset Timer (500ms)                                            
...
(No edits for 500ms) ───▶ Timer Fires ───▶ Reload Pattern                       
```

**Rationale**:
- **Debounce prevents reload storms**: IDEs trigger multiple events per save (create, write, chmod)
- **Per-file timers**: Independent debounce for each pattern file
- **Configurable delay**: Default 500ms, tunable for different environments

**Reload Flow**:
```
1. Detect Change (fsnotify)
   ├─ CREATE: New pattern added                                                 
   ├─ WRITE:  Pattern modified                                                  
   └─ REMOVE: Pattern deleted                                                   

2. Debounce (500ms default)
   ├─ Cancel existing timer for this file                                       
   └─ Start new timer                                                           

3. Validate (YAML parse + schema check)
   ├─ Valid → Continue                                                          
   └─ Invalid → Log error, call callback, skip reload                           

4. Reload (atomic cache update)
   ├─ Parse YAML                                                                
   ├─ Load into Pattern struct                                                  
   ├─ Write lock patternCache                                                   
   ├─ Update map[name]*Pattern                                                  
   └─ Release write lock                                                        

5. Broadcast (callback notification)
   ├─ Notify server (pattern update event)                                      
   └─ Trace to Hawk (reload metrics)                                            
```

**Performance**:
- Debounce latency: 500ms default
- Validation: 5-15ms per pattern (YAML parse)
- Cache update: <1ms (atomic map write)
- **Total P50/P99**: 89ms / 143ms (measured)

**Atomic Updates**:
```go
// Pattern cache updates are atomic via RWMutex
lib.mu.Lock()
lib.patternCache[name] = newPattern
lib.mu.Unlock()

// Readers always see consistent state (no partial updates)
```


### Pattern Orchestrator

**Responsibility**: Intent classification, execution planning, and pattern recommendation.

**Intent Classification**:
```
User Message ───▶ Intent Classifier ───▶ IntentCategory + Confidence            
                      │                                                         
                      ├─ Keyword matching (default)                             
                      ├─ Backend-specific classifier (pluggable)                
                      └─ LLM-based classification (optional)                    
```

**Intent Categories** (9 types):
- `schema_discovery`: "Show me the table structure"
- `data_quality`: "Check for duplicates"
- `data_transform`: "Convert this data format"
- `analytics`: "Calculate sales by region"
- `relationship_query`: "How are these tables related?"
- `query_generation`: "Generate a SQL query for..."
- `document_search`: "Find documents matching..."
- `api_call`: "Call the REST API to..."
- `unknown`: Fallback category

**Pattern Recommendation Algorithm**:
```
1. Search Library (keyword matching)
   ├─ Extract keywords from user message                                        
   ├─ Search pattern index                                                      
   └─ Return top N candidates                                                   

2. Score Candidates (multi-factor, from orchestrator.go)
   ├─ Category match (intent alignment):    +0.5
   ├─ Keyword match rate (% of keywords):   up to +0.5
   ├─ Exact name match:                     +0.2
   ├─ Title keyword match:                  +0.1
   └─ Sum scores

3. Rank by Score
   ├─ Sort descending                                                           
   └─ Return top-1 pattern + confidence                                         

4. (Optional) Learning Agent Integration
   ├─ Boost patterns with high effectiveness scores                             
   └─ Penalize patterns with recent failures                                    
```

**Pluggable Classifiers**:
```go
// Backends can provide custom intent classifiers
type IntentClassifierFunc func(
    userMessage string,
    context map[string]interface{}
) (IntentCategory, float64)

// Example: Teradata-specific classifier
func TeradataIntentClassifier(msg string, ctx map[string]interface{}) (IntentCategory, float64) {
    if strings.Contains(msg, "nPath") || strings.Contains(msg, "sessionize") {
        return IntentAnalytics, 0.9
    }
    // ... backend-specific logic
    return defaultIntentClassifier(msg, ctx)
}

orchestrator.SetIntentClassifier(TeradataIntentClassifier)
```

**Routing Recommendations**:
- Orchestrator provides guidance for tool/pattern selection
- Injected into agent system prompt
- Helps LLM choose efficient execution path


### Search and Matching

**Responsibility**: Find relevant patterns based on user query.

**Search Algorithm** (keyword-based, with optional LLM re-ranking via `pkg/patterns/llm_reranker.go`):
```
1. Build Pattern Index (startup)
   ├─ Load all patterns                                                         
   ├─ Extract: name, title, description, use cases, category                    
   └─ Store in []PatternSummary                                                 

2. Query Matching (per request)
   ├─ Tokenize query and filter stop words
   ├─ For each pattern in index:
   │   ├─ Build searchable text (name + title + description + use cases)
   │   ├─ Count keyword matches
   │   ├─ Base score = matchCount / totalKeywords
   │   ├─ Boost: name contains keyword → +0.5
   │   └─ Boost: title contains keyword → +0.3
   ├─ Sort by score descending, then by match count
   └─ Return ALL matching patterns (no top-K truncation)

3. Intent Boosting (in Orchestrator.RecommendPattern, not in Library.Search)
   ├─ If pattern category matches intent → +0.5 score
   ├─ Keyword match rate → up to +0.5 score
   ├─ Exact name match → +0.2 score
   └─ Title keyword match → +0.1 score
```

**Complexity**:
- Index build: O(N) where N = pattern count
- Query: O(N × M) where M = avg keywords per pattern
- Space: O(N) for pattern summaries

**Performance**:
- Index build: <100ms for 104 patterns
- Query: <10ms for 104 patterns
- Memory: ~500KB for pattern index

**Rationale for Keyword-Based (not TF-IDF/embeddings)**:
- ✅ Fast (no model inference)
- ✅ Deterministic (reproducible ranking)
- ✅ Explainable (why this pattern was chosen)
- ✅ No external dependencies (no embedding models)
- ❌ Limited semantic understanding (synonyms not handled)
- ❌ Keyword-based only (no semantic similarity)

**Hybrid LLM Re-Ranking** (✅ Implemented in `pkg/patterns/llm_reranker.go`):

When an LLM provider is configured on the orchestrator (via `SetLLMProvider()`), the system uses a hybrid approach: fast keyword matching followed by LLM-based re-ranking for ambiguous cases. LLM re-ranking is triggered when:
- Intent is unknown
- Top keyword score is below 0.70
- Top two candidates are within 0.20 of each other
- Three or more candidates score above 0.60

This provides semantic understanding beyond keyword matching without requiring embedding models or vector databases.


### A/B Testing

**Responsibility**: Compare pattern variants to identify improvements.

**Variant Selection Strategies** (`pkg/prompts/ab_testing.go` for selectors, `pkg/patterns/ab_testing.go` for pattern-specific A/B wrapper, 4 strategies):

The A/B testing system is implemented via the `VariantSelector` interface in `pkg/prompts/ab_testing.go`, with four concrete strategies. Pattern-specific A/B testing wraps this via `PatternABTestingLibrary` in `pkg/patterns/ab_testing.go`:

1. **Explicit** (`ExplicitSelector`):
   - Always returns the specified variant (no selection logic)
   - Use for: Manual override, debugging, forced rollout

2. **Hash-Based** (`HashSelector`):
   - Deterministic based on FNV-64a hash of session ID + key
   - Same session always gets the same variant (consistent experience)
   - Use for: Stable A/B testing where user experience must not change mid-session

3. **Random** (`RandomSelector`):
   - Uniform random selection across variants
   - Use for: True A/B testing with equal distribution

4. **Weighted** (`WeightedSelector`):
   - Weighted random selection based on variant weights
   - Weights are relative (don't need to sum to 100)
   - Use for: Gradual rollout (e.g., 80% default, 20% experimental)

**A/B Test Configuration** (code-based):
```go
// Hash-based: deterministic per session
selector := prompts.NewHashSelector()

// Weighted: 80% default, 20% experimental
selector := prompts.NewWeightedSelector(map[string]int{
    "default":      80,
    "experimental": 20,
}, 0)

// Wrap a PromptRegistry with A/B testing
abRegistry := prompts.NewABTestingRegistry(fileRegistry, selector)
prompt, _ := abRegistry.GetForSession(ctx, "agent.system", "sess-123", vars)
```

**Integration with Learning Agent**:
```
Execute Variant ───▶ Collect Metrics ───▶ Pattern Effectiveness Tracker
                                              │
                                              ▼
                                        Learning Analysis
                                              │
                                              ├─ Statistical significance test
                                              ├─ Winner identification
                                              └─ Auto-apply (if configured)
```


### Pattern Effectiveness Tracker

**Responsibility**: Track pattern usage metrics for learning agent feedback.

**Metrics Collected** (aggregated per time window, per pattern/variant/agent):
- Pattern name, variant, domain, agent ID
- Success/failure count and success rate
- Judge pass rate, average score, per-criterion scores (multi-dimensional)
- Average cost in USD
- Average latency in milliseconds
- Error type breakdown (JSON map)
- LLM provider and model used

**Storage**: SQLite database (schema in `pkg/metaagent/learning/schema.go`, logic in `pkg/metaagent/learning/pattern_tracker.go`)

**Integration Points**:
1. **Agent Execution**: Records metrics after pattern use (via `Orchestrator.RecordPatternUsage()`)
2. **Judge System**: Exports scores to tracker (multi-dimensional: pass rate, criterion scores)
3. **Learning Agent**: Queries tracker for analysis
4. **MessageBus**: Publishes aggregated metrics to `meta.pattern.effectiveness` topic

**See**: [Learning Agent Architecture](learning-agent.md)


## Key Interactions

### Pattern Loading

```
Agent Request     Library       Cache        Embedded FS    Filesystem
  │                 │              │              │             │               
  ├─ Load(name) ───▶│              │              │             │               
  │                 ├─ Check Cache ▶│              │             │              
  │                 │◀─ Not Found ─┤              │             │               
  │                 │              │              │             │               
  │                 ├─ Try Embedded ─────────────▶│             │               
  │                 │◀─ Not Found ────────────────┤             │               
  │                 │              │              │             │               
  │                 ├─ Try Filesystem ────────────┼────────────▶│               
  │                 │◀─ Pattern YAML ─────────────┼─────────────┤               
  │                 │              │              │             │               
  │                 ├─ Parse YAML  │              │             │               
  │                 ├─ Validate    │              │             │               
  │                 ├─ Cache Update ─────────────▶│             │               
  │                 │              │              │             │               
  │◀─ Pattern ──────┤              │              │             │               
  │                 │              │              │             │               
```

**Duration**:
- Cache hit: <1ms
- Embedded FS: 5-15ms (YAML parse)
- Filesystem: 8-20ms (file I/O + parse)


### Hot-Reload Flow

```
Text Editor    Filesystem    fsnotify      Debouncer     Library      Agent
  │                │             │              │            │           │      
  ├─ Save File ───▶│             │              │            │           │      
  │                ├─ WRITE ─────▶│              │            │           │     
  │                │             ├─ Event ──────▶│            │           │     
  │                │             │              ├─ Start     │           │      
  │                │             │              │  Timer     │           │      
  │                │             │              │  (500ms)   │           │      
  │                │             │              │            │           │      
  (500ms passes, no more edits)                │            │           │       
  │                │             │              ├─ Fire ─────▶│           │     
  │                │             │              │            ├─ Parse    │      
  │                │             │              │            ├─ Validate │      
  │                │             │              │            ├─ Update   │      
  │                │             │              │            │  Cache    │      
  │                │             │              │            │           │      
  │                │             │              │            ├─ Broadcast▶│     
  │                │             │              │            │           ├─ Relo
  │                │             │              │            │           │  (nex
  │                │             │              │            │           │      
```

**Total Latency**: 500ms (debounce) + 5-15ms (parse) + <1ms (cache update) = **505-516ms typical**

**Measured Performance** (from benchmarks):
- P50: 89ms (fast path, no debounce)
- P99: 143ms (slow path, file I/O contention)


### Pattern Matching

```
User Query      Orchestrator    Library       Pattern Index
  │                 │               │                │                          
  ├─ "Analyze ─────▶│               │                │                          
  │  customer       ├─ Classify ───┤                │                           
  │  journey"       │  Intent       │                │                          
  │                 │◀─ analytics ──┤                │                          
  │                 │  (0.85)       │                │                          
  │                 │               │                │                          
  │                 ├─ Search ──────┼───────────────▶│                          
  │                 │  "customer    │                ├─ Match Keywords          
  │                 │   journey"    │                ├─ Score Patterns          
  │                 │               │                ├─ Rank by Score           
  │                 │◀─ Top-5 ──────┼────────────────┤                          
  │                 │               │                │                          
  │                 ├─ Recommend ───┤                │                          
  │                 │  (intent      │                │                          
  │                 │   boost)      │                │                          
  │                 │               │                │                          
  │◀─ npath_analysis│               │                │                          
  │   (0.92 conf)   │               │                │                          
  │                 │               │                │                          
```

**Scoring Example** (based on actual weights from `pkg/patterns/orchestrator.go`):
```
User: "Analyze customer purchase journey with nPath"

Pattern: npath_analysis
├─ Category = "analytics" matches IntentAnalytics → +0.5
├─ Keyword match rate (4/5 keywords matched)      → +0.4
├─ Name contains "npath"                          → +0.2
├─ Title contains "nPath"                         → +0.1
└─ Total Score: 1.2 (high confidence match)

Pattern: sessionize
├─ Category = "analytics" matches IntentAnalytics → +0.5
├─ Keyword match rate (2/5 keywords matched)      → +0.2
└─ Total Score: 0.7 (medium confidence, alternative)
```


## Data Structures

### Pattern Struct

**Definition** (`pkg/patterns/types.go`):
```go
type Pattern struct {
    Name            string               // npath_analysis
    Title           string               // "nPath Sequential Analysis"
    Description     string               // What this pattern does
    Category        string               // analytics
    Difficulty      string               // advanced
    BackendType     string               // sql
    UseCases        []string             // ["customer journey", "churn"]
    RelatedPatterns []string             // ["sessionize", "funnel"]

    Parameters      []Parameter          // Template variables
    Templates       map[string]Template  // basic, advanced, discovery
    Examples        []Example            // Worked examples
    CommonErrors    []CommonError        // Known failures + solutions
    BestPractices   string               // Usage guidance
    Syntax          *Syntax              // Backend-specific syntax
    BackendFunction string               // Backend-specific function name
}
```

**YAML Example** (based on `patterns/teradata/analytics/npath.yaml`):
```yaml
name: npath
title: "nPath Sequence Analysis"
description: "Analyze sequences of events to find patterns over ordered data partitions"
category: analytics
difficulty: advanced
backend_type: sql

parameters:
  - name: table
    type: string
    required: true
    description: "Source table name"
  - name: pattern
    type: string
    required: true
    description: "Sequential pattern (e.g., 'A*.B*.C')"

templates:
  basic: |
    SELECT * FROM nPath(
      ON {{.table}} PARTITION BY {{.partition_key}} ORDER BY {{.order_key}}
      PATTERN '{{.pattern}}'
      SYMBOLS ({{.symbols}})
      RESULT ({{.result_columns}})
    )

examples:
  - name: "Ad to purchase journey"
    parameters:
      table: customer_events
      pattern: "AdView*.ProductView*.Purchase"
    expected_result: "Customer paths from ad to purchase"
```


## Algorithms

### Pattern Search

**Problem**: Find relevant patterns from 104 candidates in <10ms.

**Solution**: In-memory keyword matching with intent boosting and optional LLM re-ranking.

**Algorithm** (simplified from `pkg/patterns/library.go`):
```go
func (lib *Library) Search(query string) []PatternSummary {
    queryLower := strings.ToLower(query)
    keywords := tokenizeAndFilterStopWords(queryLower)

    type scoredResult struct {
        pattern    PatternSummary
        matchCount int
        score      float64
    }
    scoredResults := []scoredResult{}

    for _, p := range lib.ListAll() {
        searchText := strings.ToLower(p.Name + " " + p.Title + " " + p.Description)
        matchCount := 0
        for _, kw := range keywords {
            if strings.Contains(searchText, kw) {
                matchCount++
            }
        }

        if matchCount > 0 {
            score := float64(matchCount) / float64(len(keywords))
            // Boost for name/title matches (stronger signal)
            for _, kw := range keywords {
                if strings.Contains(strings.ToLower(p.Name), kw) {
                    score += 0.5
                }
                if strings.Contains(strings.ToLower(p.Title), kw) {
                    score += 0.3
                }
            }
            scoredResults = append(scoredResults, scoredResult{p, matchCount, score})
        }
    }

    // Sort by score descending, then by match count
    sort.Slice(scoredResults, func(i, j int) bool { ... })

    // Return ALL matching patterns (no top-K truncation)
    return extractPatterns(scoredResults)
}
```

**Complexity**:
- Time: O(N × K) where N = patterns, K = keywords
- Space: O(N) for results array

**Performance**: <10ms for 104 patterns, 5 keywords


### Debounce Algorithm

**Problem**: Rapid-fire filesystem events during save (create, write, chmod).

**Solution**: Per-file timer with reset logic.

**Algorithm**:
```go
func (hr *HotReloader) handleFileChange(path string) {
    hr.debounceMu.Lock()
    defer hr.debounceMu.Unlock()

    // Cancel existing timer for this file
    if timer, exists := hr.debounceTimers[path]; exists {
        timer.Stop()
    }

    // Start new timer
    hr.debounceTimers[path] = time.AfterFunc(
        time.Duration(hr.config.DebounceMs) * time.Millisecond,
        func() {
            hr.reloadPattern(path)  // Actual reload after delay

            hr.debounceMu.Lock()
            delete(hr.debounceTimers, path)  // Cleanup
            hr.debounceMu.Unlock()
        },
    )
}
```

**Timing**:
```
Edit 1 (t=0ms)    → Start Timer (fires at t=500ms)
Edit 2 (t=100ms)  → Reset Timer (fires at t=600ms)
Edit 3 (t=200ms)  → Reset Timer (fires at t=700ms)
No edits after t=200ms
Timer fires at t=700ms → Reload Pattern
```


### A/B Test Selection

**Variant Selection** (4 strategies, no UCB -- stateless selection models):

The A/B testing system uses the `VariantSelector` interface (`pkg/prompts/ab_testing.go`) with four concrete strategies. Selection is stateless -- there is no bandit algorithm or adaptive selection based on past results.

```
1. ExplicitSelector:  Always returns forced variant (debugging, manual override)
2. HashSelector:      FNV-64a hash of (sessionID + key) → deterministic variant
3. RandomSelector:    Uniform random (math/rand with configurable seed)
4. WeightedSelector:  Weighted random based on relative integer weights
```

**Rationale**:
- Hash-based provides consistent user experience within a session
- Weighted enables gradual rollout (e.g., 90/10 canary testing)
- No bandit/UCB algorithm -- effectiveness tracking feeds into the Learning Agent for offline analysis, not real-time variant selection

**Example** (WeightedSelector):
```go
// 80% default, 20% experimental
selector := prompts.NewWeightedSelector(map[string]int{
    "default":      80,
    "experimental": 20,
}, 0)

// For canary testing (convenience function in pkg/patterns/ab_testing.go):
selector := patterns.NewCanarySelector("control", "treatment", 0.10) // 90/10 split
```


## Design Trade-offs

### Decision 1: Hybrid Keyword + LLM Re-Ranking

**Chosen**: Keyword-based search with optional LLM re-ranking for ambiguous cases

**Rationale**:
- **Fast path**: <10ms query time for clear keyword matches (no model inference)
- **Accurate path**: LLM re-ranking for ambiguous cases (triggered automatically)
- **Deterministic default**: Same query → same results when LLM not triggered
- **Graceful degradation**: Works without LLM provider configured

**How it works** (`pkg/patterns/llm_reranker.go`):
1. Keyword-based scoring runs first (fast path)
2. If results are ambiguous (low confidence, close scores, unknown intent), LLM re-ranks top candidates
3. If LLM fails, falls back to keyword-based scores

**Alternatives considered**:
1. **Embedding similarity (vector DB)**:
   - ✅ Best semantic understanding
   - ❌ Requires external vector DB or embedding model
   - ❌ Latency for embedding generation
   - ❌ Cost per embedding

2. **Keywords only (no LLM fallback)**:
   - ✅ Fastest, simplest
   - ❌ Limited semantic understanding (synonyms missed)

**Consequences**:
- ✅ Fast for clear matches, accurate for ambiguous cases
- ✅ Zero external dependencies in default mode
- ✅ Graceful degradation without LLM
- ❌ LLM re-ranking adds latency when triggered
- ❌ Requires good keyword coverage for fast-path accuracy


### Decision 2: Hot-Reload vs. Server Restart

**Chosen**: Hot-reload with fsnotify + debounce

**Rationale**:
- **Zero downtime**: Patterns update without interrupting agents
- **Fast iteration**: Edit pattern → test immediately (no restart)
- **Atomic updates**: Cache updates are atomic (no partial state)

**Alternatives**:
1. **Server restart required**:
   - ✅ Simplest implementation
   - ❌ Downtime during restart
   - ❌ All sessions lost
   - ❌ Slow iteration cycle

2. **Polling-based reload (check every N seconds)**:
   - ✅ No fsnotify dependency
   - ❌ Latency (up to N seconds delay)
   - ❌ Wasted CPU (constant polling)

3. **API-based reload (HTTP endpoint triggers reload)**:
   - ✅ Explicit control
   - ❌ Manual trigger required
   - ❌ Extra API surface

**Consequences**:
- ✅ Zero downtime, fast iteration
- ✅ Atomic cache updates (thread-safe)
- ❌ fsnotify dependency (platform-specific quirks)
- ❌ Debounce complexity (rapid-fire events)


### Decision 3: Dual-Source (Embedded + Filesystem) vs. Single Source

**Chosen**: Dual-source with embedded FS fallback

**Rationale**:
- **Embedded FS**: Always available (no filesystem dependency), immutable
- **Filesystem**: Hot-reloadable, development-friendly
- **Priority**: Filesystem overrides embedded (allows local customization)

**Alternatives**:
1. **Filesystem only**:
   - ✅ Simpler implementation
   - ❌ Requires patterns directory (deployment complexity)
   - ❌ No fallback if filesystem unavailable

2. **Embedded only**:
   - ✅ Self-contained binary
   - ❌ No hot-reload (binary recompile required)
   - ❌ No local development workflow

**Consequences**:
- ✅ Best of both worlds (fallback + hot-reload)
- ✅ Production deployment flexibility
- ❌ Added complexity (two load paths)
- ❌ Pattern precedence rules to remember


## Constraints and Limitations

### Constraint 1: Pattern Count Scales Linearly

**Description**: Search algorithm is O(N) in pattern count

**Current**: 104 patterns, <10ms search time

**Impact**: 700 patterns → ~100ms search time (may become bottleneck)

**Workaround**: Implement category pre-filtering or indexing (e.g., inverted index)


### Constraint 2: Keyword-Based Matching Limitations

**Description**: No semantic understanding of synonyms or paraphrases

**Example**:
- Query: "Find duplicates" → Matches pattern "duplicate_detection"
- Query: "Find identical rows" → Might not match (no "duplicate" keyword)

**Workaround**: Expand pattern descriptions with synonym keywords, or implement embedding-based search


### Constraint 3: Hot-Reload Race Condition During Edit

**Description**: Pattern may be in inconsistent state during multi-step file edit

**Scenario**:
```
1. Editor deletes old file
2. Editor creates new file
3. fsnotify triggers on DELETE → pattern removed from cache
4. fsnotify triggers on CREATE → pattern reloaded
   (but if agent queries during step 3-4, pattern not found)
```

**Mitigation**: Debounce logic delays reload until edits settle (500ms)

**Impact**: Minimal (race window < 500ms, agent retries on cache miss)


## Performance Characteristics

### Latency (P50/P99)

| Operation | P50 | P99 | Notes |
|-----------|-----|-----|-------|
| Pattern load (cache hit) | <1ms | <1ms | In-memory map lookup |
| Pattern load (embedded) | 8ms | 15ms | YAML parse |
| Pattern load (filesystem) | 12ms | 25ms | File I/O + YAML parse |
| Pattern search | 5ms | 10ms | 104 patterns, keyword matching |
| Hot-reload (debounce) | 505ms | 520ms | 500ms debounce + 5-20ms reload |
| Hot-reload (measured) | 89ms | 143ms | Optimized path, no debounce wait |
| Intent classification | 2ms | 5ms | Keyword-based heuristics |
| Pattern recommendation | 8ms | 15ms | Search + scoring |

### Memory Usage

| Component | Size |
|-----------|------|
| Pattern cache (104 patterns) | ~1.5MB (full structs) |
| Pattern index (summaries) | ~500KB (metadata only) |
| Hot-reloader | ~100KB (timers, watcher) |
| **Total** | **~2MB** |

### Throughput

- **Pattern loads**: 10,000+ req/s (cache hit)
- **Pattern searches**: 1,000+ req/s (104 patterns)
- **Hot-reloads**: N/A (filesystem-bound, rare operation)


## Concurrency Model

### Library Cache

**Model**: Read-write lock (`sync.RWMutex`)

**Readers**: Concurrent pattern loads (GetPattern, Search)
**Writers**: Exclusive hot-reload (UpdatePattern)

**Rationale**: Read-heavy workload (99%+ reads after warm-up), RWMutex optimizes for concurrent reads


### Hot-Reload System

**Model**: Single goroutine per file watcher, debounce timers per file

**Synchronization**:
- Debounce timer map: Mutex-protected (`debounceMu`)
- Pattern cache updates: RWMutex (shared with library)

**Race Prevention**:
- All tests run with `-race` detector
- Zero race conditions in v1.2.0


### Pattern Orchestrator

**Model**: Stateless (no shared state), concurrent intent classification

**Thread Safety**: All operations read-only after initialization (no locking needed)


## Error Handling

### Strategy

1. **Graceful Degradation**: If pattern load fails, agent continues without pattern guidance
2. **Validation on Reload**: Hot-reload validates YAML before applying (invalid patterns logged, not cached)
3. **Fallback Sources**: If filesystem unavailable, fall back to embedded patterns
4. **Cache Consistency**: Pattern cache always in consistent state (atomic updates)

### Error Propagation

```
YAML Parse Error ───▶ Log Error ───▶ Skip Reload ───▶ Call Callback(error)      
                         │                                                      
                         ▼
                     Trace to Hawk (error recorded)


Pattern Not Found ───▶ Try Embedded FS ───▶ Try Filesystem ───▶ Return Error    
                            │                     │                             
                            ▼                     ▼
                         Cached?              Cached?


Hot-Reload Validation Failure ───▶ Log + Trace ───▶ Keep Old Pattern            
                                         │                                      
                                         ▼
                                    Notify Callback(error)
```


## Security Considerations

### Threat Model

1. **Malicious Pattern Injection**: Attacker modifies pattern files to inject commands
2. **Path Traversal**: Attacker uses path traversal to load patterns from arbitrary directories
3. **YAML Bomb**: Malicious YAML with recursive structures causing DoS

### Mitigations

**Pattern Injection**:
- Patterns are templates, not executable code (safe variable interpolation)
- Agent validates backend operations (SQL validation, API request validation)
- No shell execution from patterns

**Path Traversal**:
- Pattern names validated (no "..", "/", etc.)
- Search paths hardcoded (cannot load from arbitrary directories)
- Filesystem access restricted to patternsDir

**YAML Bomb**:
- YAML parser has depth limits (default: 64 levels)
- Pattern validation checks for reasonable size (< 1MB per pattern)
- Hot-reload timeout prevents infinite parsing


## Related Work

### Pattern Libraries

1. **LangChain Prompts**: Template-based prompt management
   - **Similar**: YAML-based pattern definitions
   - **Loom differs**: Hot-reload, A/B testing, effectiveness tracking, backend-agnostic

2. **Semantic Kernel Skills**: C#-based skill library
   - **Similar**: Reusable domain knowledge
   - **Loom differs**: YAML (not code), multi-language support, hot-reload

3. **Dust Apps**: API-first workflow patterns
   - **Similar**: Composable execution patterns
   - **Loom differs**: Pattern-guided (not workflow-driven), LLM context optimization

### Hot-Reload Systems

1. **Kubernetes ConfigMap**: Hot-reload configuration
   - **Similar**: File watch + reload
   - **Loom differs**: In-process (not sidecar), debounce logic, atomic updates

2. **Prometheus Config Reload**: SIGHUP-triggered reload
   - **Similar**: Zero-downtime configuration updates
   - **Loom differs**: Automatic filesystem watch (not signal-based)


## References

1. fsnotify (fsnotify/fsnotify). Cross-platform file system notifications for Go. https://github.com/fsnotify/fsnotify

2. Fowler, G. & Vo, P. (1991). *FNV Hash*. Used for deterministic A/B testing variant selection via FNV-64a hash.


## Further Reading

### Architecture Deep Dives

- [Agent System Architecture](agent-system-design.md) - Pattern integration with ROM layer
- [Memory System Architecture](memory-systems.md) - ROM layer immutability for hot-reload
- [Learning Agent Architecture](learning-agent.md) - Pattern effectiveness tracking
- [Loom System Architecture](loom-system-architecture.md) - Overall system design

### Reference Documentation

- [Pattern Configuration Reference](/docs/reference/patterns.md) - Pattern YAML format
- [CLI Reference](/docs/reference/cli.md) - Pattern management commands

### Guides

- [Getting Started](/docs/guides/quickstart.md) - Quick start guide
- [Pattern Library Guide](/docs/guides/pattern-library-guide.md) - Pattern library usage
