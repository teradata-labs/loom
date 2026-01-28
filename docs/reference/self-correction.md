
# Self-Correction and Error Recovery Reference

**Version**: v1.0.0-beta.1

Complete technical reference for Loom's self-correction system - error analysis, retry strategies, circuit breakers, and automatic recovery patterns.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Architecture](#architecture)
- [Guardrails Engine](#guardrails-engine)
- [Circuit Breakers](#circuit-breakers)
- [Error Classification](#error-classification)
- [Correction Strategies](#correction-strategies)
- [Retry Policies](#retry-policies)
- [Configuration](#configuration)
- [Protocol Integration](#protocol-integration)
- [Best Practices](#best-practices)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)
- [Error Codes](#error-codes)
- [See Also](#see-also)


## Quick Reference

### Self-Correction Components

| Component | Purpose | Location | State |
|-----------|---------|----------|-------|
| **GuardrailEngine** | Pre-flight validation, error analysis, correction suggestions | `pkg/fabric/guardrails.go` | Thread-safe |
| **CircuitBreaker** | Prevent cascading failures, exponential backoff | `pkg/fabric/circuit_breaker.go` | Per-tool isolation |
| **Error Classifier** | Categorize errors for targeted corrections | `pkg/fabric/guardrails.go` | Stateless |
| **Correction Generator** | Generate SQL fixes, retry strategies | `pkg/fabric/guardrails.go` | Context-aware |


### Error Types and Correction Strategies

| Error Type | Detection | Correction Strategy | Confidence | Auto-Retry |
|------------|-----------|---------------------|------------|------------|
| `syntax_error` | "syntax" in message | Check parentheses, reserved keywords, commas | Medium | Yes (with rewrite) |
| `table_not_found` | "table"/"object" + "not found" | Verify table name, schema qualification | High | Yes (after schema discovery) |
| `column_not_found` | "column" + "not found" | Call GetTableSchema for actual column names | High | Yes (after schema) |
| `permission_denied` | "permission"/"access denied" | Check grants, database access, ownership | High | No (requires manual fix) |
| `timeout` | "timeout"/"exceeded" | Add WHERE clause, indexes, pagination | Medium | Yes (with query optimization) |
| `unknown` | None of above | Generic retry with error context | Low | Limited (3 attempts max) |


### Circuit Breaker States

| State | Behavior | Transitions | Purpose |
|-------|----------|-------------|---------|
| **Closed** | Allow all requests | → Open (after 5 consecutive failures) | Normal operation |
| **Open** | Reject immediately | → Half-Open (after exponential timeout) | Fail fast during cascading failures |
| **Half-Open** | Allow limited requests | → Closed (2 successes) or → Open (any failure) | Test recovery |

**Exponential Backoff**: Base timeout × 2^(consecutive_opens - 1), capped at 60s
- 1st open: 30s
- 2nd open: 60s (capped)
- Subsequent: 60s (capped)


### Configuration Defaults

```yaml
# Agent configuration (self-correction enabled by default)
agent:
  self_correction:
    enabled: true  # Default: true
    circuit_breaker:
      failure_threshold: 5        # Failures before opening circuit
      success_threshold: 2        # Successes to close from half-open
      timeout: 30s                # Base timeout for exponential backoff
    guardrails:
      max_retry_attempts: 3       # Maximum correction attempts per error
      preflight_validation: true  # Run validators before execution
```

**Go API**:
```go
// Self-correction enabled by default
agent := agent.NewAgent(backend, llm)

// Explicit configuration
agent := agent.NewAgent(backend, llm,
    agent.WithGuardrails(customGuardrails),
    agent.WithCircuitBreakers(customBreakers),
)

// Disable self-correction
agent := agent.NewAgent(backend, llm,
    agent.WithoutSelfCorrection(),
)
```


## Overview

Loom's self-correction system provides **automatic error recovery** through three integrated components:

1. **Guardrails Engine**: Pre-flight validation and error analysis
2. **Circuit Breakers**: Cascading failure prevention with exponential backoff
3. **Correction Generator**: Intelligent retry strategies based on error type

**Key Features**:
- **Automatic retry** for recoverable errors (syntax, schema mismatches)
- **Fail-fast** for unrecoverable errors (permissions, rate limits)
- **Per-tool isolation** - one failing tool doesn't block others
- **Exponential backoff** - prevent overwhelming failing systems
- **Error history tracking** - learn from repeated failures
- **Confidence scoring** - prioritize high-confidence corrections

**Implementation**: `pkg/fabric/` (guardrails.go, circuit_breaker.go)
**Available Since**: v0.7.0
**Thread Safety**: All components are thread-safe


## Architecture

### Self-Correction Flow

```
┌──────────────────────────────────────────────────────────────────┐
│                      User Query                                   │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│                    Agent Conversation Loop                        │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  1. LLM generates SQL/action                             │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  2. Guardrails: Pre-flight Validation                    │   │
│  │     - Run registered validators                           │   │
│  │     - Check syntax, reserved words, etc.                 │   │
│  │     → Issues found? Return warnings to LLM               │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  3. Circuit Breaker: Check State                         │   │
│  │     - Closed? → Allow request                            │   │
│  │     - Open? → Reject with timeout info                   │   │
│  │     - Half-Open? → Allow limited test request           │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  4. Tool Execution                                        │   │
│  │     → Success? Clear error history                       │   │
│  │     → Failure? → Go to Error Analysis                    │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  5. Error Analysis (on failure)                          │   │
│  │     - Classify error type                                │   │
│  │     - Retrieve error history                             │   │
│  │     - Generate correction suggestions                    │   │
│  │     - Update circuit breaker state                       │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  6. Correction Strategy                                   │   │
│  │     ┌──────────────────────────────────────┐             │   │
│  │     │ High Confidence?                     │             │   │
│  │     │ (table_not_found, column_not_found)  │             │   │
│  │     └──────────────────────────────────────┘             │   │
│  │              │                       │                    │   │
│  │         Yes  │                  No   │                    │   │
│  │              ▼                       ▼                    │   │
│  │     Auto-retry with          Ask LLM to revise           │   │
│  │     schema discovery         with suggestions            │   │
│  └──────────────────────────────────────────────────────────┘   │
│                              │                                    │
│                              ▼                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  7. Retry/Continue                                        │   │
│  │     - Retry attempt < 3? → Loop back to step 1          │   │
│  │     - Max attempts reached? → Return error to user       │   │
│  └──────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```


### Component Interactions

```
Agent
  │
  ├─ GuardrailEngine (session-scoped error tracking)
  │   ├─ Validators[] (pre-flight checks)
  │   ├─ ErrorCache[sessionID] → ErrorRecord
  │   └─ CorrectionGenerator
  │
  └─ CircuitBreakerManager (per-tool isolation)
      ├─ CircuitBreaker[tool1] → State, FailureCount, Timeout
      ├─ CircuitBreaker[tool2] → State, FailureCount, Timeout
      └─ CircuitBreaker[toolN] → State, FailureCount, Timeout
```

**Key Design Decisions**:
- **Per-tool circuit breakers**: One failing tool doesn't block others
- **Session-scoped error tracking**: Learn from repeated errors in same session
- **Pluggable validators**: Backends can register custom validation rules
- **Confidence-based strategies**: High-confidence errors auto-retry, low-confidence involve LLM


## Guardrails Engine

### Overview

The **GuardrailEngine** performs two critical functions:
1. **Pre-flight validation**: Check queries before execution
2. **Error analysis**: Analyze failures and suggest corrections

**Thread Safety**: All methods are thread-safe (uses sync.RWMutex)
**Scope**: Session-based error tracking (errors tracked per session ID)


### Creating Guardrail Engine

```go
import "github.com/teradata-labs/loom/pkg/fabric"

// Create engine
engine := fabric.NewGuardrailEngine()

// Register backend-specific validators
engine.RegisterValidator(myValidator)
```


### Pre-flight Validation

**Purpose**: Catch common errors before execution

```go
type Validator interface {
    Name() string
    Validate(ctx context.Context, sql string) []Issue
}

type Issue struct {
    Severity    string // "error", "warning", "info"
    Message     string
    Suggestion  string
    LineNumber  int
    ColumnRange string
}
```

**Example Validator**:
```go
type TeradataReservedWordValidator struct{}

func (v *TeradataReservedWordValidator) Name() string {
    return "teradata_reserved_words"
}

func (v *TeradataReservedWordValidator) Validate(ctx context.Context, sql string) []fabric.Issue {
    issues := []fabric.Issue{}

    // Check for unquoted reserved words
    if strings.Contains(strings.ToUpper(sql), " USER ") {
        issues = append(issues, fabric.Issue{
            Severity:   "error",
            Message:    "Reserved word 'USER' used without quotes",
            Suggestion: "Quote reserved words: \"USER\"",
        })
    }

    return issues
}

// Register validator
engine.RegisterValidator(&TeradataReservedWordValidator{})
```

**Usage in Agent**:
```go
// Run pre-flight check
issues := engine.PreflightCheck(ctx, generatedSQL)

if len(issues) > 0 {
    // Format issues for LLM
    issueText := formatIssues(issues)

    // Provide to LLM for correction
    // (Agent automatically includes in conversation context)
}
```


### Error Analysis

**Purpose**: Analyze execution errors and suggest corrections

#### HandleError

```go
// Basic error handling (error code + message)
correction := engine.HandleError(
    ctx,
    sessionID,
    failedSQL,
    errorCode,
    errorMessage,
)
```

#### HandleErrorWithAnalysis

```go
// Enhanced error handling (with detailed analysis)
analysis := &fabric.ErrorAnalysisInfo{
    ErrorType:   "table_not_found",
    Summary:     "Table 'sales' does not exist",
    Suggestions: []string{
        "Check table name spelling",
        "Verify schema: database.table",
        "Call GetTableSchema to discover tables",
    },
}

correction := engine.HandleErrorWithAnalysis(
    ctx,
    sessionID,
    failedSQL,
    analysis,
)
```

**Returns**: `fabric.Correction`
```go
type Correction struct {
    OriginalSQL     string
    CorrectedSQL    string  // May be empty (LLM generates)
    Explanation     string  // Human-readable correction guidance
    ErrorCode       string
    ConfidenceLevel string  // "high", "medium", "low"
}
```


### Error Record Tracking

**Purpose**: Track error history per session to avoid repeated failures

```go
type ErrorRecord struct {
    SQL              string
    ErrorCode        string
    ErrorMessage     string
    Timestamp        string
    AttemptCount     int      // How many times this session has failed
    PreviousAttempts []string // History of failed SQL
    ErrorAnalysis    *ErrorAnalysisInfo
}
```

**Operations**:
```go
// Get error history for a session
record := engine.GetErrorRecord(sessionID)

if record != nil && record.AttemptCount >= 3 {
    // Too many failures - abort or escalate
}

// Clear error history on success
engine.ClearErrorRecord(sessionID)
```


### Correction Generation

**Process**:
1. Classify error type
2. Retrieve error history
3. Generate correction based on error type and attempt count
4. Assign confidence level

**Error Types and Corrections**:

#### syntax_error

**Confidence**: Medium

**Explanation**:
```
SQL syntax error detected. Check for:
- Missing or extra parentheses
- Reserved keyword usage (quote with double quotes)
- Comma placement in SELECT/WHERE clauses

Suggestions:
- [context-specific suggestions from backend]
```

#### table_not_found / object_not_found

**Confidence**: High

**Explanation**:
```
Table/object does not exist. Verify:
- Table name spelling and case sensitivity
- Database/schema qualification (database.table)
- Table exists in the current session
```

**Strategy**: Call GetTableSchema or ListTables tool to discover actual table names

#### column_not_found

**Confidence**: High

**Explanation**:
```
Column not found. Call GetTableSchema to discover actual column names
```

**Strategy**: Call GetTableSchema with table name to get actual columns

#### permission_denied

**Confidence**: High (but not auto-retryable)

**Explanation**:
```
Insufficient permissions. Check:
- User has required grants (SELECT, INSERT, UPDATE, etc.)
- Database access permissions
- Object ownership
```

**Strategy**: Report to user (cannot auto-fix)

#### timeout

**Confidence**: Medium

**Explanation**:
```
Query timeout. Consider:
- Adding WHERE clause to limit data
- Using indexes for better performance
- Breaking into smaller queries
- Caching intermediate results
```

**Strategy**: LLM optimizes query with LIMIT, WHERE clauses

#### unknown

**Confidence**: Low

**Explanation**:
```
Error encountered (attempt N): [error message]

Suggestions:
- [any context from backend]
```

**Strategy**: Limited retry (max 3 attempts)


## Circuit Breakers

### Overview

**Circuit breakers** implement the circuit breaker pattern to:
- **Prevent cascading failures**: One failing tool doesn't block other tools
- **Provide fail-fast behavior**: Reject requests immediately when circuit is open
- **Enable automatic recovery**: Test recovery after exponential backoff timeout

**Implementation**: Per-tool circuit breakers managed by `CircuitBreakerManager`
**Thread Safety**: All operations are thread-safe


### Circuit Breaker States

#### 1. Closed (Normal Operation)

**Behavior**:
- Allow all requests
- Track failures (increment `failureCount` on error)
- Reset `failureCount` on success

**Transition**:
- → **Open** when `failureCount >= FailureThreshold` (default: 5)

**Example**:
```
Request 1: Error → failureCount = 1
Request 2: Error → failureCount = 2
Request 3: Success → failureCount = 0 (reset)
Request 4: Error → failureCount = 1
Request 5: Error → failureCount = 2
```


#### 2. Open (Failing)

**Behavior**:
- **Reject all requests immediately** with error:
  ```
  circuit breaker open: too many consecutive failures (5), retry after 30s
  ```
- **Exponential backoff**: Calculate timeout based on `consecutiveOpens`
- **No requests executed**: Fail-fast to prevent overwhelming failing system

**Transition**:
- → **Half-Open** after exponential backoff timeout elapses

**Exponential Backoff**:
```
1st open: baseTimeout × 2^0 = 30s × 1 = 30s
2nd open: baseTimeout × 2^1 = 30s × 2 = 60s (capped)
3rd open: baseTimeout × 2^2 = 30s × 4 = 120s (capped at 60s max)
```

**Example**:
```
Time 0:00  - Circuit opens (5 consecutive failures)
Time 0:05  - Request arrives → Rejected (retry after 25s)
Time 0:30  - Timeout elapsed → Transition to Half-Open
```


#### 3. Half-Open (Testing Recovery)

**Behavior**:
- **Allow limited requests** to test if system has recovered
- Track successes (increment `successCount`)

**Transitions**:
- → **Closed** when `successCount >= SuccessThreshold` (default: 2)
  - Resets `failureCount`, `successCount`, `consecutiveOpens`
- → **Open** on any failure
  - Resets `successCount`, reopens circuit immediately

**Example (successful recovery)**:
```
Half-Open state
Request 1: Success → successCount = 1
Request 2: Success → successCount = 2 → Transition to Closed
Circuit fully recovered!
```

**Example (failed recovery)**:
```
Half-Open state
Request 1: Success → successCount = 1
Request 2: Error → Reopens immediately → Back to Open state
```


### Circuit Breaker Configuration

```go
type CircuitBreakerConfig struct {
    FailureThreshold int           // Failures to open circuit (default: 5)
    SuccessThreshold int           // Successes to close from half-open (default: 2)
    Timeout          time.Duration // Base timeout for exponential backoff (default: 30s)
    OnStateChange    func(from, to CircuitState) // Optional callback
}

// Default configuration
config := fabric.DefaultCircuitBreakerConfig()
// Returns: FailureThreshold=5, SuccessThreshold=2, Timeout=30s
```


### Per-Tool Circuit Breakers

**CircuitBreakerManager** provides **per-tool isolation**:

```go
manager := fabric.NewCircuitBreakerManager(config)

// Each tool gets its own circuit breaker
breaker1 := manager.GetBreaker("query_database")
breaker2 := manager.GetBreaker("call_api")
breaker3 := manager.GetBreaker("read_file")

// Tool1 fails repeatedly → Circuit opens for tool1 only
// Tool2, Tool3 continue to work normally
```

**Key Benefit**: One failing tool (e.g., database connection issues) doesn't block other tools (API calls, file operations).


### Using Circuit Breakers

#### Execute with Circuit Breaker

```go
breaker := manager.GetBreaker("query_database")

// Wrap operation
err := breaker.Execute(func() error {
    return backend.ExecuteQuery(ctx, sql)
})

if err != nil {
    // Error could be:
    // 1. Circuit breaker open (fail-fast)
    // 2. Actual execution error

    if strings.Contains(err.Error(), "circuit breaker open") {
        // Circuit is open - wait for timeout
        log.Warn("Circuit breaker protecting from cascading failure")
    } else {
        // Real execution error - handle normally
    }
}
```

#### Execute with Validation Flag

**Use case**: Pre-flight validation errors should not count toward circuit breaker threshold

```go
// Validation checks (expected to catch errors)
err := breaker.ExecuteEx(func() error {
    return validator.ValidateSQL(sql)
}, true) // isValidation = true

// Validation errors are logged but NOT counted toward threshold
```


### Circuit Breaker Statistics

```go
type CircuitBreakerStats struct {
    State            CircuitState  // closed, open, half-open
    FailureCount     int           // Current failure count
    SuccessCount     int           // Current success count (half-open only)
    LastFailureTime  time.Time     // When last failure occurred
    LastStateChange  time.Time     // When state last changed
    FailureThreshold int           // Config threshold
    SuccessThreshold int           // Config threshold
    ConsecutiveOpens int           // How many times circuit has opened (for backoff)
}

// Get stats for a tool
stats := breaker.GetStats()
fmt.Printf("State: %s, Failures: %d/%d\n",
    stats.State, stats.FailureCount, stats.FailureThreshold)

// Get stats for all tools
allStats := manager.GetAllStats()
for toolName, stats := range allStats {
    fmt.Printf("%s: %s (%d failures)\n",
        toolName, stats.State, stats.FailureCount)
}
```


### Manual Reset

```go
// Reset specific tool's circuit breaker
manager.Reset("query_database")

// Reset all circuit breakers
manager.ResetAll()

// Use case: Manual intervention after fixing underlying issue
// Example: Database server restarted, reset circuit to allow traffic
```


## Error Classification

### InferErrorType

**Purpose**: Classify errors based on error code and message

```go
func InferErrorType(errorCode, errorMessage string) string
```

**Classification Logic**:
```go
messageLower := strings.ToLower(errorMessage)

// Priority order (most specific first):
1. "syntax" → "syntax_error"
2. "permission" / "access denied" → "permission_denied"
3. "column" + ("not found" / "does not exist") → "column_not_found"
4. "table"/"object" + ("not found" / "does not exist") → "table_not_found"
5. "timeout" / "exceeded" → "timeout"
6. None of above → "unknown"
```

**Example**:
```go
// Syntax error
InferErrorType("", "SQL syntax error near 'SELET'")
// Returns: "syntax_error"

// Table not found
InferErrorType("3802", "Table 'sales' does not exist")
// Returns: "table_not_found"

// Column not found
InferErrorType("", "Column 'reveneu' not found in table sales")
// Returns: "column_not_found"

// Permission denied
InferErrorType("", "User does not have SELECT permission on table sales")
// Returns: "permission_denied"
```


### ClassifyError

**Purpose**: Classify Go errors by message content

```go
func ClassifyError(err error) string
```

**Similar to `InferErrorType` but operates on Go `error` type**:
```go
err := backend.ExecuteQuery(ctx, sql)
errorType := fabric.ClassifyError(err)

switch errorType {
case "syntax_error":
    // Handle syntax errors
case "connection":
    // Handle connection errors
case "permission_denied":
    // Handle permission errors
}
```


## Correction Strategies

### High-Confidence Corrections (Auto-Retry)

**Error Types**: `table_not_found`, `column_not_found`

**Strategy**:
1. Detect error type
2. Call schema discovery tool automatically
3. Retry with correct table/column names

**Flow**:
```
Query: SELECT revenue FROM sales
          ↓
Error: Table 'sales' does not exist
          ↓
Classify: table_not_found (high confidence)
          ↓
Auto-action: Call GetTableSchema or ListTables
          ↓
Discovery: Actual table is 'Sales_Data'
          ↓
Retry: SELECT revenue FROM Sales_Data
          ↓
Success!
```

**Implementation**:
```go
if analysis.ErrorType == "table_not_found" || analysis.ErrorType == "column_not_found" {
    // High confidence - auto-retry with schema discovery

    // 1. Call schema discovery tool
    schema, err := tools.GetTableSchema(ctx, tableGuess)

    // 2. Provide schema to LLM for correction
    // (Agent automatically includes schema in context)

    // 3. LLM generates corrected SQL

    // 4. Retry execution
}
```


### Medium-Confidence Corrections (LLM-Assisted)

**Error Types**: `syntax_error`, `timeout`

**Strategy**:
1. Analyze error
2. Generate detailed suggestions
3. Provide suggestions to LLM
4. LLM generates corrected SQL
5. Retry

**Flow**:
```
Query: SELECT revenue FROM sales WHERE  // Missing condition
          ↓
Error: Syntax error near 'WHERE'
          ↓
Classify: syntax_error (medium confidence)
          ↓
Generate suggestions:
  - Check for missing conditions after WHERE
  - Verify parentheses balance
  - Quote reserved keywords
          ↓
Provide to LLM with error context
          ↓
LLM generates: SELECT revenue FROM sales WHERE date > '2024-01-01'
          ↓
Retry
```


### Low-Confidence Corrections (Limited Retry)

**Error Types**: `unknown`

**Strategy**:
1. Track attempt count
2. Provide generic suggestions
3. Retry up to 3 times
4. Escalate to user if still failing

**Flow**:
```
Query: [Complex query]
          ↓
Error: [Unclear error message]
          ↓
Classify: unknown (low confidence)
          ↓
Attempt 1: Retry with error context → Still fails
Attempt 2: Retry with modifications → Still fails
Attempt 3: Retry with simpler approach → Still fails
          ↓
Max attempts reached → Return error to user
```


### Non-Retryable Errors (Fail Immediately)

**Error Types**: `permission_denied`

**Strategy**: Report to user, do not retry

**Rationale**: Permission errors cannot be auto-fixed by agent

**Flow**:
```
Query: DELETE FROM sales
          ↓
Error: User does not have DELETE permission
          ↓
Classify: permission_denied (high confidence, non-retryable)
          ↓
Report to user:
  "I don't have permission to delete from this table.
   Please grant DELETE permission or use a different approach."
          ↓
No retry
```


## Retry Policies

### Retry Decision Tree

```
Error Occurred
      │
      ├─ Circuit Breaker Open?
      │   Yes → Fail immediately (no retry)
      │
      ├─ Attempt Count >= Max (3)?
      │   Yes → Fail (max retries exceeded)
      │
      ├─ Error Type = permission_denied?
      │   Yes → Fail (non-retryable)
      │
      ├─ Error Type = table_not_found / column_not_found?
      │   Yes → Auto-retry with schema discovery (high confidence)
      │
      ├─ Error Type = syntax_error / timeout?
      │   Yes → Retry with LLM correction (medium confidence)
      │
      └─ Error Type = unknown?
          Yes → Retry with generic suggestions (low confidence)
```


### Retry Limits

**Maximum attempts per error**: 3

**Reasoning**:
- Attempt 1: Initial try
- Attempt 2: First correction
- Attempt 3: Alternative correction
- After 3 failures: Likely systematic issue, escalate to user

**Tracking**:
```go
record := engine.GetErrorRecord(sessionID)
if record != nil && record.AttemptCount >= 3 {
    return fmt.Errorf("max retry attempts (%d) exceeded: %s",
        record.AttemptCount, record.ErrorMessage)
}
```


### Exponential Backoff (Circuit Breaker)

**Formula**: `baseTimeout × 2^(consecutiveOpens - 1)`

**Example with 30s base timeout**:
```
1st failure sequence (5 failures) → Circuit opens
    Timeout: 30s × 2^0 = 30s
    Wait 30s → Half-open → 2 successes → Closed

2nd failure sequence (5 failures) → Circuit opens again
    Timeout: 30s × 2^1 = 60s
    Wait 60s → Half-open

3rd failure sequence → Circuit opens again
    Timeout: 30s × 2^2 = 120s (capped at 60s max)
    Wait 60s → Half-open
```

**Cap**: Maximum timeout = 60 seconds


### Inter-Attempt Delay

**No explicit delay between retry attempts within a conversation**

**Reasoning**:
- Circuit breaker provides backoff at component level
- Conversation-level retries should be fast for user experience
- LLM processing time provides natural delay (~2-5 seconds)

**If implementing explicit delay**:
```go
// Example: Add delay for timeout errors
if analysis.ErrorType == "timeout" {
    time.Sleep(5 * time.Second) // Brief delay before retry
}
```


## Configuration

### Agent-Level Configuration

```go
// Default (self-correction enabled)
agent := agent.NewAgent(backend, llm)

// Custom guardrails
customGuardrails := fabric.NewGuardrailEngine()
customGuardrails.RegisterValidator(myValidator)

agent := agent.NewAgent(backend, llm,
    agent.WithGuardrails(customGuardrails),
)

// Custom circuit breakers
customConfig := fabric.CircuitBreakerConfig{
    FailureThreshold: 3,  // More aggressive (default: 5)
    SuccessThreshold: 3,  // More conservative (default: 2)
    Timeout:          10 * time.Second, // Shorter timeout (default: 30s)
}
customBreakers := fabric.NewCircuitBreakerManager(customConfig)

agent := agent.NewAgent(backend, llm,
    agent.WithCircuitBreakers(customBreakers),
)

// Disable self-correction entirely
agent := agent.NewAgent(backend, llm,
    agent.WithoutSelfCorrection(),
)
```


### YAML Configuration

**Not yet implemented** (planned for v1.1.0)

**Planned structure**:
```yaml
agents:
  - id: sql-agent
    self_correction:
      enabled: true

      circuit_breaker:
        failure_threshold: 5
        success_threshold: 2
        timeout: 30s

      guardrails:
        max_retry_attempts: 3
        preflight_validation: true
        validators:
          - teradata_reserved_words
          - sql_injection_check

      retry_policy:
        syntax_error: auto_retry_with_llm
        table_not_found: auto_retry_with_schema
        column_not_found: auto_retry_with_schema
        permission_denied: fail_immediately
        timeout: retry_with_optimization
        unknown: limited_retry
```


## Protocol Integration

### WeaveResponse with Self-Correction

```protobuf
message WeaveResponse {
  string message = 1;
  repeated ToolCall tool_calls = 2;
  bool final = 3;
  string session_id = 4;
  CostInfo cost = 5;
  ExecutionMetadata metadata = 6;

  // Self-correction attempts (if any)
  repeated SelfCorrectionAttempt corrections = 7;

  string agent_id = 8;
}
```


### SelfCorrectionAttempt Message

```protobuf
message SelfCorrectionAttempt {
  // Error that triggered correction
  string original_error = 1;

  // Correction strategy applied
  string strategy = 2;

  // Was correction successful?
  bool successful = 3;

  // Confidence level (high, medium, low)
  string confidence = 4;

  // Time taken for correction
  int64 duration_ms = 5;
}
```

**Example**:
```json
{
  "corrections": [
    {
      "original_error": "Table 'sales' does not exist",
      "strategy": "schema_discovery",
      "successful": true,
      "confidence": "high",
      "duration_ms": 423
    }
  ]
}
```


### Execution Stages

```protobuf
enum ExecutionStage {
  EXECUTION_STAGE_UNKNOWN = 0;
  EXECUTION_STAGE_PATTERN_SELECTION = 1;
  EXECUTION_STAGE_SCHEMA_DISCOVERY = 2;
  EXECUTION_STAGE_LLM_GENERATION = 3;
  EXECUTION_STAGE_TOOL_EXECUTION = 4;
  EXECUTION_STAGE_HUMAN_IN_THE_LOOP = 9;
  EXECUTION_STAGE_GUARDRAIL_CHECK = 5;
  EXECUTION_STAGE_SELF_CORRECTION = 6;  // Self-correction in progress
  EXECUTION_STAGE_COMPLETED = 7;
  EXECUTION_STAGE_FAILED = 8;
}
```

**Usage in streaming**:
```json
{
  "stage": "EXECUTION_STAGE_SELF_CORRECTION",
  "status_message": "Error detected: Table not found. Discovering schema..."
}
```


### Execution Metadata

```protobuf
message ExecutionMetadata {
  int32 turns = 1;
  int32 llm_calls = 2;
  int32 tool_executions = 3;

  // Number of self-correction attempts
  int32 correction_attempts = 4;

  int64 total_duration_ms = 5;
  repeated string guardrails_checked = 6;
}
```

**Example**:
```json
{
  "metadata": {
    "turns": 2,
    "llm_calls": 3,
    "tool_executions": 2,
    "correction_attempts": 1,
    "total_duration_ms": 4523,
    "guardrails_checked": ["teradata_reserved_words", "sql_injection"]
  }
}
```


## Best Practices

### 1. Enable Self-Correction by Default

```go
// Good: Use defaults (self-correction enabled)
agent := agent.NewAgent(backend, llm)

// Avoid: Disabling without good reason
agent := agent.NewAgent(backend, llm,
    agent.WithoutSelfCorrection(), // Only if you have custom error handling
)
```

**Why**: Self-correction significantly improves success rate and user experience.


### 2. Register Backend-Specific Validators

```go
// PostgreSQL example
type PostgresValidator struct {
    db *sql.DB
}

func (v *PostgresValidator) Validate(ctx context.Context, sql string) []fabric.Issue {
    issues := []fabric.Issue{}

    // Check for common PostgreSQL errors
    if strings.Contains(strings.ToUpper(sql), "ILIKE") {
        // ILIKE is PostgreSQL-specific, warn if expecting ANSI SQL
    }

    // Check reserved words
    // Check syntax patterns
    // Etc.

    return issues
}

// Register
engine.RegisterValidator(&PostgresValidator{db: db})
```

**Why**: Catch backend-specific issues before execution.


### 3. Monitor Circuit Breaker State

```go
// Periodic monitoring
ticker := time.NewTicker(30 * time.Second)
go func() {
    for range ticker.C {
        stats := manager.GetAllStats()
        for toolName, stat := range stats {
            if stat.State == fabric.StateOpen {
                log.Warn("Circuit breaker open",
                    zap.String("tool", toolName),
                    zap.Int("failures", stat.FailureCount),
                    zap.Duration("next_retry", breaker.GetTimeout()))
            }
        }
    }
}()
```

**Why**: Detect cascading failures early, alert operations team.


### 4. Clear Error History on Success

```go
// After successful execution
if err == nil {
    engine.ClearErrorRecord(sessionID)
}
```

**Why**: Prevent old error history from affecting new queries.


### 5. Use Confidence Levels Wisely

**High confidence** (auto-retry):
- `table_not_found`
- `column_not_found`

**Medium confidence** (LLM-assisted):
- `syntax_error`
- `timeout`

**Low confidence** (limited retry):
- `unknown`

**Non-retryable**:
- `permission_denied`

**Why**: Avoid wasting retries on errors that require manual intervention.


### 6. Implement Custom Error Analysis

```go
// Backend-specific error analysis
type TeradataErrorAnalyzer struct{}

func (a *TeradataErrorAnalyzer) AnalyzeError(errorCode string, errorMessage string) *fabric.ErrorAnalysisInfo {
    // Teradata error codes
    switch errorCode {
    case "3802":
        return &fabric.ErrorAnalysisInfo{
            ErrorType:   "table_not_found",
            Summary:     errorMessage,
            Suggestions: []string{
                "Check database qualification: database.tablename",
                "Verify table exists: SELECT * FROM DBC.TablesV WHERE TableName = '...'",
            },
        }
    case "3807":
        return &fabric.ErrorAnalysisInfo{
            ErrorType:   "column_not_found",
            Summary:     errorMessage,
            Suggestions: []string{
                "Call GetTableSchema to discover actual columns",
            },
        }
    // ... more Teradata-specific error codes
    }

    // Fallback to generic classification
    return &fabric.ErrorAnalysisInfo{
        ErrorType: fabric.InferErrorType(errorCode, errorMessage),
        Summary:   errorMessage,
    }
}
```


### 7. Test Circuit Breaker Behavior

```go
// Test circuit breaker opens after failures
func TestCircuitBreakerOpens(t *testing.T) {
    config := fabric.CircuitBreakerConfig{
        FailureThreshold: 3,
        SuccessThreshold: 2,
        Timeout:          100 * time.Millisecond,
    }
    breaker := fabric.NewCircuitBreaker(config)

    // Cause 3 failures
    for i := 0; i < 3; i++ {
        err := breaker.Execute(func() error {
            return fmt.Errorf("simulated failure")
        })
        assert.Error(t, err)
    }

    // Circuit should be open
    assert.Equal(t, fabric.StateOpen, breaker.GetState())

    // Next request should fail immediately
    err := breaker.Execute(func() error {
        t.Fatal("Should not execute - circuit is open")
        return nil
    })
    assert.Contains(t, err.Error(), "circuit breaker open")

    // Wait for timeout
    time.Sleep(100 * time.Millisecond)

    // Should transition to half-open
    assert.Equal(t, fabric.StateHalfOpen, breaker.GetState())
}
```


### 8. Use -race Detector for Testing

```bash
# Always test with race detector
go test -race ./pkg/fabric/...

# Circuit breaker manager has complex locking
go test -race -run TestCircuitBreakerManager ./pkg/fabric/
```

**Why**: Guardrails and circuit breakers use complex locking (sync.RWMutex, double-checked locking).


### 9. Log Self-Correction Attempts

```go
// In agent conversation loop
if len(corrections) > 0 {
    for _, correction := range corrections {
        logger.Info("self_correction_attempt",
            zap.String("original_error", correction.OriginalError),
            zap.String("strategy", correction.Strategy),
            zap.Bool("successful", correction.Successful),
            zap.String("confidence", correction.Confidence))
    }
}
```

**Why**: Understand agent behavior, identify patterns in errors.


### 10. Expose Circuit Breaker Metrics

```go
// Prometheus metrics (example)
import "github.com/prometheus/client_golang/prometheus"

var (
    circuitBreakerState = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "circuit_breaker_state",
            Help: "Circuit breaker state (0=closed, 1=open, 2=half-open)",
        },
        []string{"tool"},
    )

    circuitBreakerFailures = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "circuit_breaker_failures_total",
            Help: "Total circuit breaker failures",
        },
        []string{"tool"},
    )
)

// Update metrics
stats := manager.GetAllStats()
for toolName, stat := range stats {
    circuitBreakerState.WithLabelValues(toolName).Set(float64(stat.State))
    circuitBreakerFailures.WithLabelValues(toolName).Add(float64(stat.FailureCount))
}
```


## Monitoring

### Key Metrics

| Metric | Type | Description | Alert Threshold |
|--------|------|-------------|----------------|
| `correction_attempts` | Counter | Total correction attempts per session | >5 per session |
| `circuit_breaker_opens` | Counter | Times circuit has opened per tool | >10 per hour |
| `circuit_breaker_state` | Gauge | Current state (0=closed, 1=open, 2=half-open) | State=open for >5min |
| `high_confidence_corrections` | Counter | Auto-retry corrections | - |
| `low_confidence_corrections` | Counter | Limited retry corrections | >50% of corrections |
| `correction_success_rate` | Gauge | Successful corrections / total attempts | <80% |


### Observability Integration

**Hawk Tracing**:
```go
// Self-correction spans
ctx, span := tracer.StartSpan(ctx, "self_correction")
span.SetAttribute("error_type", analysis.ErrorType)
span.SetAttribute("confidence", correction.ConfidenceLevel)
span.SetAttribute("attempt_count", record.AttemptCount)

defer func() {
    if correctionSuccessful {
        span.SetAttribute("outcome", "success")
    } else {
        span.SetAttribute("outcome", "failure")
    }
    tracer.EndSpan(span)
}()
```


### Logging Best Practices

```go
// Log circuit breaker events
logger.Info("circuit_breaker_opened",
    zap.String("tool", toolName),
    zap.Int("consecutive_failures", stats.FailureCount),
    zap.Int("consecutive_opens", stats.ConsecutiveOpens),
    zap.Duration("timeout", breaker.GetTimeout()))

// Log self-correction attempts
logger.Info("self_correction_attempt",
    zap.String("session_id", sessionID),
    zap.String("error_type", analysis.ErrorType),
    zap.String("confidence", correction.ConfidenceLevel),
    zap.Int("attempt", record.AttemptCount))

// Log correction outcomes
logger.Info("self_correction_outcome",
    zap.String("session_id", sessionID),
    zap.Bool("successful", successful),
    zap.Duration("duration", duration))
```


## Troubleshooting

### Issue: Circuit Breaker Opens Frequently

**Symptoms**:
- Circuit opens multiple times per hour
- "circuit breaker open" errors
- Exponential backoff timeouts increasing

**Diagnosis**:
```go
stats := manager.GetAllStats()
for tool, stat := range stats {
    if stat.ConsecutiveOpens > 3 {
        fmt.Printf("Tool %s has opened %d times\n", tool, stat.ConsecutiveOpens)
    }
}
```

**Causes**:
1. Backend service is down or unhealthy
2. Network issues (timeouts, connection refused)
3. Rate limiting from external service
4. Configuration issue (e.g., wrong credentials)

**Resolution**:
1. Check backend service health
2. Review backend logs for errors
3. Verify network connectivity
4. Check rate limits and quotas
5. Manually reset circuit: `manager.Reset(toolName)`


### Issue: Too Many Correction Attempts

**Symptoms**:
- Sessions require >5 correction attempts
- High `correction_attempts` counter

**Diagnosis**:
```go
record := engine.GetErrorRecord(sessionID)
fmt.Printf("Attempt count: %d\n", record.AttemptCount)
fmt.Printf("Previous attempts: %v\n", record.PreviousAttempts)
```

**Causes**:
1. Poor LLM corrections (same error repeated)
2. Missing validators (errors not caught pre-flight)
3. Schema discovery not working
4. Complex domain requiring better patterns

**Resolution**:
1. Review LLM-generated SQL quality
2. Add backend-specific validators
3. Verify schema discovery tools work
4. Add domain patterns to pattern library


### Issue: Low Correction Success Rate

**Symptoms**:
- `correction_success_rate` < 80%
- High `low_confidence_corrections` count

**Diagnosis**:
```go
// Track correction outcomes
successCount := 0
totalCount := 0

// After each correction attempt
totalCount++
if successful {
    successCount++
}

successRate := float64(successCount) / float64(totalCount)
```

**Causes**:
1. High proportion of unknown error types
2. Backend-specific errors not classified
3. Insufficient correction strategies

**Resolution**:
1. Implement custom error classification for backend
2. Add error-specific correction strategies
3. Improve error message parsing
4. Add more high-confidence correction paths


### Issue: Circuit Never Recovers

**Symptoms**:
- Circuit stays open for extended periods
- Half-open → Open cycle repeats

**Diagnosis**:
```go
stats := breaker.GetStats()
fmt.Printf("State: %s\n", stats.State)
fmt.Printf("Last failure: %s ago\n", time.Since(stats.LastFailureTime))
fmt.Printf("Last state change: %s ago\n", time.Since(stats.LastStateChange))
```

**Causes**:
1. Underlying issue not fixed
2. SuccessThreshold too high (requires too many successes)
3. Timeout too short (not enough time for recovery)

**Resolution**:
1. Fix underlying issue (backend, network, etc.)
2. Adjust SuccessThreshold to lower value
3. Increase Timeout to allow more recovery time
4. Manually reset if issue is fixed: `breaker.Reset()`


## Error Codes

### ERR_MAX_RETRIES_EXCEEDED

**Code**: `max_retries_exceeded`
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Maximum retry attempts (3) exceeded for an error.

**Example**:
```
Error: max retry attempts (3) exceeded: Table 'sales' does not exist
```

**Resolution**:
1. Review error history: `engine.GetErrorRecord(sessionID)`
2. Check if corrections are actually fixing the issue
3. Verify backend service is healthy
4. Check if error requires manual intervention

**Retry behavior**: Not retryable (max attempts reached)


### ERR_CIRCUIT_BREAKER_OPEN

**Code**: `circuit_breaker_open`
**HTTP Status**: 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Circuit breaker is open due to too many failures.

**Example**:
```
Error: circuit breaker open: too many consecutive failures (5), retry after 30s
```

**Resolution**:
1. Wait for exponential backoff timeout
2. Check backend service health
3. Review circuit breaker stats: `breaker.GetStats()`
4. Manually reset if issue resolved: `breaker.Reset()`

**Retry behavior**: Retryable after timeout (exponential backoff)


### ERR_PREFLIGHT_VALIDATION_FAILED

**Code**: `preflight_validation_failed`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Pre-flight validation detected errors.

**Example**:
```
Error: preflight validation failed: Reserved word 'USER' used without quotes
```

**Resolution**:
1. Review validation issues: `engine.PreflightCheck(ctx, sql)`
2. Fix SQL according to suggestions
3. Re-submit query

**Retry behavior**: Retryable after fixing SQL


### ERR_PERMISSION_DENIED_NOT_RETRYABLE

**Code**: `permission_denied_not_retryable`
**HTTP Status**: 403 Forbidden
**gRPC Code**: `PERMISSION_DENIED`

**Cause**: Permission error detected, marked as non-retryable.

**Example**:
```
Error: User does not have DELETE permission on table sales
```

**Resolution**:
1. Grant required permissions
2. Or use different approach (e.g., read-only query)

**Retry behavior**: Not retryable (requires manual permission grant)


## See Also

### Reference Documentation
- [Guardrails API](./guardrails-api.md) - Pre-flight validation API (planned)
- [Circuit Breaker API](./circuit-breaker-api.md) - Circuit breaker detailed API (planned)
- [Agent Reference](./agent-reference.md) - Agent configuration options (planned)

### Guides
- [Error Handling Guide](../guides/error-handling.md) - Error handling best practices (planned)
- [Pattern Library Guide](../guides/pattern-library-guide.md) - Using patterns for error prevention
- [Agent Configuration Guide](../guides/agent-configuration.md) - Configure self-correction

### Architecture Documentation
- [Self-Correction Architecture](../architecture/self-correction.md) - Design decisions (planned)
- [Agent System Design](../architecture/agent-system-design.md) - Overall agent architecture

### External Resources
- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html) - Martin Fowler's article
- [Exponential Backoff](https://en.wikipedia.org/wiki/Exponential_backoff) - Backoff strategy
- [Retry Strategies](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/) - AWS best practices
