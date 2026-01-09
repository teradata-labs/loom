// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"context"
	"encoding/json"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// JudgeAgentAdapter wraps a Judge to work with Loom's orchestration Fork-Join pattern.
// This adapter allows judges to be treated as agents for parallel execution.
type JudgeAgentAdapter struct {
	agent   *agent.Agent // Not embedded - we override Chat()
	judge   Judge
	evalCtx *loomv1.EvaluationContext
}

// NewJudgeAgentAdapter creates an agent wrapper around a judge.
// The agent uses a NoOpBackend since judges don't need backend execution.
func NewJudgeAgentAdapter(judge Judge, evalCtx *loomv1.EvaluationContext, tracer observability.Tracer) (*JudgeAgentAdapter, error) {
	if judge == nil {
		return nil, fmt.Errorf("judge cannot be nil")
	}
	if evalCtx == nil {
		return nil, fmt.Errorf("evaluation context cannot be nil")
	}
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}

	// Create a minimal agent with NoOp backend and LLM provider
	// The agent won't actually use these - we'll override Chat() behavior
	backend := &NoOpBackend{}
	llmProvider := &NoOpLLMProvider{judgeName: judge.Name()}

	ag := agent.NewAgent(backend, llmProvider,
		agent.WithTracer(tracer),
		agent.WithName(judge.Name()),
		agent.WithDescription(fmt.Sprintf("Judge: %s", judge.Name())),
	)

	return &JudgeAgentAdapter{
		agent:   ag,
		judge:   judge,
		evalCtx: evalCtx,
	}, nil
}

// Chat overrides the agent's Chat method to call Judge.Evaluate() instead.
// The prompt parameter is ignored - judges use the pre-configured EvaluationContext.
func (j *JudgeAgentAdapter) Chat(ctx context.Context, sessionID string, prompt string) (*agent.Response, error) {
	// Execute judge evaluation
	result, err := j.judge.Evaluate(ctx, j.evalCtx)
	if err != nil {
		return &agent.Response{
			Content: fmt.Sprintf("Judge evaluation failed: %v", err),
			Metadata: map[string]interface{}{
				"error": err.Error(),
			},
		}, err
	}

	// Convert JudgeResult to JSON for agent response
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal judge result: %w", err)
	}

	// Build agent response with judge verdict embedded
	output := fmt.Sprintf("Judge: %s\nVerdict: %s\nScore: %.1f/100\nReasoning: %s",
		j.judge.Name(),
		result.Verdict,
		result.OverallScore,
		result.Reasoning,
	)

	return &agent.Response{
		Content: output,
		Metadata: map[string]interface{}{
			"judge_result": string(resultJSON),
			"judge_id":     result.JudgeId,
			"judge_name":   result.JudgeName,
			"verdict":      result.Verdict,
			"score":        result.OverallScore,
		},
	}, nil
}

// GetJudge returns the underlying judge.
func (j *JudgeAgentAdapter) GetJudge() Judge {
	return j.judge
}

// GetAgent returns the internal agent (for orchestration compatibility).
func (j *JudgeAgentAdapter) GetAgent() *agent.Agent {
	return j.agent
}

// GetJudgeResult extracts JudgeResult from agent metadata.
// Returns nil if the response doesn't contain a judge result.
func GetJudgeResult(resp *agent.Response) (*loomv1.JudgeResult, error) {
	if resp == nil || resp.Metadata == nil {
		return nil, fmt.Errorf("response or metadata is nil")
	}

	resultJSON, ok := resp.Metadata["judge_result"].(string)
	if !ok {
		return nil, fmt.Errorf("judge_result not found in metadata")
	}

	var result loomv1.JudgeResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal judge result: %w", err)
	}

	return &result, nil
}

// NoOpBackend is a minimal backend implementation that does nothing.
// JudgeAgentAdapter doesn't execute queries, so this is safe.
type NoOpBackend struct{}

func (b *NoOpBackend) Name() string {
	return "noop"
}

func (b *NoOpBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return nil, fmt.Errorf("NoOpBackend: judges don't execute queries")
}

func (b *NoOpBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return nil, fmt.Errorf("NoOpBackend: judges don't access schema")
}

func (b *NoOpBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return nil, fmt.Errorf("NoOpBackend: judges don't list resources")
}

func (b *NoOpBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{"type": "noop"}, nil
}

func (b *NoOpBackend) Ping(ctx context.Context) error {
	return nil // Always healthy
}

func (b *NoOpBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}

func (b *NoOpBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("NoOpBackend: custom operations not supported")
}

func (b *NoOpBackend) Close() error {
	return nil // Nothing to close
}

// NoOpLLMProvider is a minimal LLM provider that returns judge name as model.
type NoOpLLMProvider struct {
	judgeName string
}

func (p *NoOpLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return nil, fmt.Errorf("NoOpLLMProvider: judges don't call LLM chat")
}

func (p *NoOpLLMProvider) Name() string {
	return "judge"
}

func (p *NoOpLLMProvider) Model() string {
	return fmt.Sprintf("judge-%s", p.judgeName)
}
