// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package teleprompter

import (
	"context"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Teleprompter is the interface all DSPy-style optimizers must implement.
// A teleprompter takes a program (agent) and training data, then returns
// an optimized version with improved prompts and demonstrations.
//
// Implementations:
// - BootstrapFewShot: Collect successful traces as demonstrations
// - BootstrapFewShotWithRandomSearch: Bootstrap + hyperparameter search
// - MIPRO: Multi-prompt Instruction Proposal Optimizer
// - COPRO: Collaborative Prompt Optimizer
// - KNNFewShot: K-nearest neighbor demonstration selection
// - Ensemble: Combine multiple compiled programs
type Teleprompter interface {
	// Compile optimizes an agent using training data and a metric.
	// Returns the compilation result with optimized prompts/demonstrations.
	Compile(ctx context.Context, req *CompileRequest) (*CompilationResult, error)

	// Type returns the teleprompter type for identification
	Type() loomv1.TeleprompterType

	// Name returns a human-readable name for logging
	Name() string

	// SupportsMultiRound indicates if this teleprompter supports iterative optimization
	SupportsMultiRound() bool

	// SupportsTeacher indicates if this teleprompter supports teacher-student bootstrapping
	SupportsTeacher() bool
}

// CompileRequest contains all inputs needed for compilation
type CompileRequest struct {
	AgentID  string
	Agent    Agent // The agent to optimize
	Trainset []*loomv1.Example
	Devset   []*loomv1.Example // Optional validation set
	Metric   Metric
	Config   *loomv1.TeleprompterConfig
	Tracer   observability.Tracer
}

// Agent represents the agent being optimized.
// This interface decouples teleprompters from concrete agent implementations.
type Agent interface {
	// Run executes the agent on an example and returns the result
	Run(ctx context.Context, inputs map[string]string) (*ExecutionResult, error)

	// Clone creates a copy of this agent for compilation
	Clone() Agent

	// GetMemory returns the agent's segmented memory for learned layer updates
	GetMemory() Memory

	// GetID returns the agent's unique identifier
	GetID() string
}

// Memory represents the agent's segmented memory.
// Used to update the Learned Layer with optimized content.
type Memory interface {
	// UpdateLearnedLayer applies optimized prompts and demonstrations
	UpdateLearnedLayer(
		optimizedPrompts map[string]string,
		demonstrations []*loomv1.Demonstration,
	) error

	// GetLearnedLayer retrieves current learned content
	GetLearnedLayer() (map[string]string, []*loomv1.Demonstration, error)

	// GetLearnedVersion returns the version hash of learned content
	GetLearnedVersion() string
}

// ExecutionResult contains the output of running an agent on an example
type ExecutionResult struct {
	Inputs    map[string]string
	Outputs   map[string]string
	Rationale string // Chain-of-thought reasoning (if available)
	TraceID   string // Hawk trace ID for observability
	Success   bool
	Error     error
}

// CompilationResult represents the output of teleprompter compilation
type CompilationResult struct {
	CompilationID string
	AgentID       string
	Teleprompter  loomv1.TeleprompterType

	// Learned content
	OptimizedPrompts map[string]string
	Demonstrations   []*loomv1.Demonstration

	// Metrics
	TrainsetScore      float64
	DevsetScore        float64
	ExamplesUsed       int32
	SuccessfulTraces   int32
	OptimizationRounds int32
	ImprovementDelta   float64 // Score improvement vs baseline

	// Performance
	CompilationTimeMs int64

	// Metadata
	CompiledVersion string // Hash of learned content
	CompiledAt      int64  // Unix timestamp
	Metadata        map[string]string
}

// Metric evaluates the quality of an agent's output
type Metric interface {
	// Evaluate scores the agent's output against the expected output
	// Returns a score in [0, 1] where 1 is perfect
	Evaluate(ctx context.Context, example *loomv1.Example, result *ExecutionResult) (float64, error)

	// Type returns the metric type for identification
	Type() loomv1.MetricType

	// Name returns a human-readable name
	Name() string
}

// DemonstrationSelector chooses which successful traces to use as demonstrations
type DemonstrationSelector interface {
	// Select chooses the best demonstrations from successful traces
	Select(ctx context.Context, traces []*ExecutionTrace, maxDemos int) ([]*loomv1.Demonstration, error)

	// Strategy returns the selection strategy used
	Strategy() loomv1.BootstrapStrategy
}

// ExecutionTrace represents a single execution of the agent with full trace data
type ExecutionTrace struct {
	TraceID      string
	Example      *loomv1.Example
	Result       *ExecutionResult
	QualityScore float64 // Overall score from metric evaluation
	Timestamp    int64
	Metadata     map[string]string

	// Multi-judge specific fields (populated when using MultiJudgeMetric)
	DimensionScores map[string]float64    // Per-dimension scores (e.g., quality: 85.0, safety: 90.0)
	JudgeVerdicts   []*loomv1.JudgeResult // Full judge verdicts for detailed analysis
}

// TeacherAgent is an optional enhanced agent for bootstrapping.
// Typically uses a more capable LLM (e.g., Opus vs Sonnet) to generate
// higher-quality demonstrations for the student agent.
type TeacherAgent interface {
	Agent

	// IsTeacher marks this as a teacher agent
	IsTeacher() bool
}

// Registry manages available teleprompters
type Registry struct {
	teleprompters map[loomv1.TeleprompterType]Teleprompter
	metrics       map[loomv1.MetricType]Metric
	selectors     map[loomv1.BootstrapStrategy]DemonstrationSelector
}

// NewRegistry creates a new teleprompter registry
func NewRegistry() *Registry {
	return &Registry{
		teleprompters: make(map[loomv1.TeleprompterType]Teleprompter),
		metrics:       make(map[loomv1.MetricType]Metric),
		selectors:     make(map[loomv1.BootstrapStrategy]DemonstrationSelector),
	}
}

// RegisterTeleprompter adds a teleprompter to the registry
func (r *Registry) RegisterTeleprompter(t Teleprompter) {
	r.teleprompters[t.Type()] = t
}

// RegisterMetric adds a metric to the registry
func (r *Registry) RegisterMetric(m Metric) {
	r.metrics[m.Type()] = m
}

// RegisterSelector adds a demonstration selector to the registry
func (r *Registry) RegisterSelector(s DemonstrationSelector) {
	r.selectors[s.Strategy()] = s
}

// GetTeleprompter retrieves a teleprompter by type
func (r *Registry) GetTeleprompter(t loomv1.TeleprompterType) (Teleprompter, bool) {
	tp, ok := r.teleprompters[t]
	return tp, ok
}

// GetMetric retrieves a metric by type
func (r *Registry) GetMetric(t loomv1.MetricType) (Metric, bool) {
	m, ok := r.metrics[t]
	return m, ok
}

// GetSelector retrieves a demonstration selector by strategy
func (r *Registry) GetSelector(s loomv1.BootstrapStrategy) (DemonstrationSelector, bool) {
	sel, ok := r.selectors[s]
	return sel, ok
}

// ListTeleprompters returns all registered teleprompters
func (r *Registry) ListTeleprompters() []loomv1.TeleprompterType {
	types := make([]loomv1.TeleprompterType, 0, len(r.teleprompters))
	for t := range r.teleprompters {
		types = append(types, t)
	}
	return types
}
