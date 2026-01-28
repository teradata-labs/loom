# Extending Teleprompters

This guide shows how to add new DSPy-style teleprompters to Loom.

## Architecture Overview

The teleprompter system uses a plugin-based architecture:

```
Teleprompter Interface (teleprompter.go)
    ↓
BaseTeleprompter (base.go) - Shared utilities
    ↓
Concrete Implementations:
    - BootstrapFewShot (bootstrap_few_shot.go)
    - MIPRO (mipro.go) - TODO
    - COPRO (copro.go) - TODO
    - KNNFewShot (knn_few_shot.go) - TODO
    - Ensemble (ensemble.go) - TODO
```

## Adding a New Teleprompter

### Step 1: Implement the `Teleprompter` Interface

All teleprompters must implement:

```go
type Teleprompter interface {
    Compile(ctx context.Context, req *CompileRequest) (*CompilationResult, error)
    Type() loomv1.TeleprompterType
    Name() string
    SupportsMultiRound() bool
    SupportsTeacher() bool
}
```

### Step 2: Embed `BaseTeleprompter` for Shared Functionality

```go
type MyTeleprompter struct {
    *BaseTeleprompter
    // Add teleprompter-specific fields here
}

func NewMyTeleprompter(tracer observability.Tracer, registry *Registry) *MyTeleprompter {
    return &MyTeleprompter{
        BaseTeleprompter: NewBaseTeleprompter(tracer, registry),
    }
}
```

### Step 3: Implement `Compile()` Method

```go
func (mt *MyTeleprompter) Compile(
    ctx context.Context,
    req *CompileRequest,
) (*CompilationResult, error) {
    startTime := time.Now()
    tracer := req.Tracer

    ctx, span := tracer.StartSpan(ctx, "teleprompter.my_teleprompter.compile")
    defer tracer.EndSpan(span)

    // 1. Validate config
    if err := mt.ValidateConfig(req.Config); err != nil {
        return nil, err
    }
    mt.SetDefaultsConfig(req.Config)

    // 2. Run optimization algorithm
    optimizedPrompts, demonstrations, err := mt.optimize(ctx, req)
    if err != nil {
        return nil, err
    }

    // 3. Apply to agent's Learned Layer
    if err := mt.ApplyLearnedLayer(ctx, req.Agent, optimizedPrompts, demonstrations); err != nil {
        return nil, err
    }

    // 4. Evaluate performance
    trainsetScore, _ := mt.EvaluateOnDevset(ctx, req.Agent, req.Trainset, req.Metric)
    devsetScore, _ := mt.EvaluateOnDevset(ctx, req.Agent, req.Devset, req.Metric)

    // 5. Build result
    compilationTime := time.Since(startTime).Milliseconds()
    return mt.BuildCompilationResult(
        req.AgentID,
        loomv1.TeleprompterType_TELEPROMPTER_MY_TYPE,
        optimizedPrompts,
        demonstrations,
        trainsetScore,
        devsetScore,
        int32(len(req.Trainset)),
        /* ... other params ... */
        compilationTime,
    ), nil
}
```

### Step 4: Implement Required Methods

```go
func (mt *MyTeleprompter) Type() loomv1.TeleprompterType {
    return loomv1.TeleprompterType_TELEPROMPTER_MY_TYPE
}

func (mt *MyTeleprompter) Name() string {
    return "MyTeleprompter"
}

func (mt *MyTeleprompter) SupportsMultiRound() bool {
    return true // or false
}

func (mt *MyTeleprompter) SupportsTeacher() bool {
    return false // or true
}
```

### Step 5: Register with Registry

```go
registry := NewRegistry()
registry.RegisterTeleprompter(NewMyTeleprompter(tracer, registry))
```

## Example: MIPRO Implementation

Here's a skeleton for MIPRO (Multi-prompt Instruction Proposal Optimizer):

```go
// pkg/metaagent/teleprompter/mipro.go

package teleprompter

import (
    "context"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "github.com/teradata-labs/loom/pkg/observability"
)

// MIPRO implements the Multi-prompt Instruction Proposal Optimizer.
//
// Algorithm:
// 1. Generate N candidate instructions using LLM
// 2. For each instruction, bootstrap demonstrations
// 3. Evaluate each (instruction + demos) combination
// 4. Select best performing configuration
type MIPRO struct {
    *BaseTeleprompter
}

func NewMIPRO(tracer observability.Tracer, registry *Registry) *MIPRO {
    return &MIPRO{
        BaseTeleprompter: NewBaseTeleprompter(tracer, registry),
    }
}

func (m *MIPRO) Compile(
    ctx context.Context,
    req *CompileRequest,
) (*CompilationResult, error) {
    // Get MIPRO-specific config
    config := req.Config.Mipro
    if config == nil {
        return nil, fmt.Errorf("MIPRO config required")
    }

    // 1. Generate instruction candidates
    instructions := m.generateInstructionCandidates(ctx, req, int(config.NumCandidates))

    // 2. Bootstrap demonstrations for each instruction
    var bestConfig *MIPROConfig
    var bestScore float64

    for _, instruction := range instructions {
        // Create agent variant with this instruction
        variantAgent := m.createAgentVariant(req.Agent, instruction)

        // Bootstrap demonstrations
        traces, _ := m.RunOnTrainset(ctx, variantAgent, req.Trainset, req.Metric, req.Config.MinConfidence)
        demos, _ := m.SelectDemonstrations(ctx, traces, int(req.Config.MaxBootstrappedDemos), loomv1.BootstrapStrategy_BOOTSTRAP_TOP_K)

        // Evaluate
        score, _ := m.EvaluateOnDevset(ctx, variantAgent, req.Devset, req.Metric)

        if score > bestScore {
            bestScore = score
            bestConfig = &MIPROConfig{
                Instruction:     instruction,
                Demonstrations:  demos,
                Score:          score,
            }
        }
    }

    // 3. Apply best configuration
    optimizedPrompts := map[string]string{
        "system": bestConfig.Instruction,
    }
    m.ApplyLearnedLayer(ctx, req.Agent, optimizedPrompts, bestConfig.Demonstrations)

    // 4. Build result
    return m.BuildCompilationResult(/* ... */), nil
}

func (m *MIPRO) Type() loomv1.TeleprompterType {
    return loomv1.TeleprompterType_TELEPROMPTER_MIPRO
}

func (m *MIPRO) Name() string {
    return "MIPRO"
}

func (m *MIPRO) SupportsMultiRound() bool {
    return true
}

func (m *MIPRO) SupportsTeacher() bool {
    return true
}

// generateInstructionCandidates uses LLM to propose N candidate instructions
func (m *MIPRO) generateInstructionCandidates(
    ctx context.Context,
    req *CompileRequest,
    numCandidates int,
) []string {
    // TODO: Use LLM to generate instruction candidates
    // Prompt: "Generate N different system instructions for a {domain} agent..."
    return []string{}
}
```

## Using Shared Utilities

The `BaseTeleprompter` provides these utilities:

### Run Agent on Dataset
```go
traces, err := mt.RunOnTrainset(ctx, agent, trainset, metric, minConfidence)
```

### Evaluate Performance
```go
score, err := mt.EvaluateOnDevset(ctx, agent, devset, metric)
```

### Select Demonstrations
```go
demos, err := mt.SelectDemonstrations(ctx, traces, maxDemos, strategy)
```

### Apply to Learned Layer
```go
err := mt.ApplyLearnedLayer(ctx, agent, optimizedPrompts, demonstrations)
```

### Generate Version Hash
```go
version := mt.GenerateCompiledVersion(optimizedPrompts, demonstrations)
```

### Build Result
```go
result := mt.BuildCompilationResult(
    agentID,
    teleprompterType,
    optimizedPrompts,
    demonstrations,
    trainsetScore,
    devsetScore,
    examplesUsed,
    successfulTraces,
    optimizationRounds,
    improvementDelta,
    compilationTimeMs,
)
```

## Custom Demonstration Selectors

To add a new demonstration selection strategy:

```go
type MySelector struct{}

func NewMySelector() *MySelector {
    return &MySelector{}
}

func (s *MySelector) Select(
    ctx context.Context,
    traces []*ExecutionTrace,
    maxDemos int,
) ([]*loomv1.Demonstration, error) {
    // Your selection logic here
    return demonstrations, nil
}

func (s *MySelector) Strategy() loomv1.BootstrapStrategy {
    return loomv1.BootstrapStrategy_MY_STRATEGY
}

// Register it:
registry.RegisterSelector(NewMySelector())
```

## Custom Metrics

To add a new evaluation metric:

```go
type MyMetric struct{}

func (m *MyMetric) Evaluate(
    ctx context.Context,
    example *loomv1.Example,
    result *ExecutionResult,
) (float64, error) {
    // Your evaluation logic here
    // Return score in [0, 1]
    return score, nil
}

func (m *MyMetric) Type() loomv1.MetricType {
    return loomv1.MetricType_METRIC_MY_TYPE
}

func (m *MyMetric) Name() string {
    return "MyMetric"
}

// Register it:
registry.RegisterMetric(&MyMetric{})
```

## Testing Your Teleprompter

Create a test file `my_teleprompter_test.go`:

```go
package teleprompter

import (
    "context"
    "testing"

    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "github.com/teradata-labs/loom/pkg/observability"
)

func TestMyTeleprompter_Compile(t *testing.T) {
    tracer := observability.NewNoOpTracer()
    registry := NewRegistry()
    mt := NewMyTeleprompter(tracer, registry)

    // Create test agent, trainset, metric
    agent := &MockAgent{}
    trainset := []*loomv1.Example{/* ... */}
    metric := &MockMetric{}

    req := &CompileRequest{
        AgentID:  "test-agent",
        Agent:    agent,
        Trainset: trainset,
        Metric:   metric,
        Config:   &loomv1.TeleprompterConfig{},
    }

    result, err := mt.Compile(context.Background(), req)
    if err != nil {
        t.Fatalf("Compile failed: %v", err)
    }

    if result.Demonstrations == nil || len(result.Demonstrations) == 0 {
        t.Error("Expected demonstrations to be selected")
    }

    if result.TrainsetScore == 0 {
        t.Error("Expected non-zero trainset score")
    }
}
```

## Available Teleprompters

### ✅ Implemented
- **BootstrapFewShot**: Collect successful traces as demonstrations

### 🚧 Coming Soon
- **MIPRO**: Multi-prompt instruction optimization
- **COPRO**: Collaborative prompt optimization
- **KNNFewShot**: K-nearest neighbor demonstration selection
- **Ensemble**: Combine multiple compiled programs
- **BootstrapFewShotWithRandomSearch**: Bootstrap + hyperparameter search
- **BayesianSignatureOptimizer**: Bayesian optimization over signatures

## Best Practices

1. **Use `BaseTeleprompter` utilities**: Don't reimplement common functionality
2. **Trace everything**: Use `tracer.StartSpan()` for observability
3. **Validate config**: Always validate and set defaults
4. **Handle errors gracefully**: Don't fail the entire compilation for one bad example
5. **Return detailed results**: Include metadata for debugging
6. **Test with race detector**: Run `go test -race ./...`
7. **Document your algorithm**: Explain how your teleprompter works in comments

## Questions?

See existing implementations:
- `bootstrap_few_shot.go` - Basic bootstrapping
- `base.go` - Shared utilities
- `selector.go` - Demonstration selection strategies

Or check the DSPy paper: "Compiling Declarative Language Model Calls into Self-Improving Pipelines" (ICLR 2024)
