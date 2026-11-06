// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"sync"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/types"
)

// resultLLMCalls is the number of LLM calls behind one result's cost. A
// producer that predates llm_calls leaves it 0; such a result counts as one
// call, which is what the totals assumed before.
func resultLLMCalls(c *loomv1.AgentExecutionCost) int32 {
	if c.GetLlmCalls() > 0 {
		return c.GetLlmCalls()
	}
	return 1
}

// auxUsage collects the LLM calls an executor makes itself, outside any
// AgentResult: a merge, a stage validation, a swarm judge. Executors are built
// per run, so one auxUsage covers one run. Safe for concurrent use.
type auxUsage struct {
	mu      sync.Mutex
	costUSD float64
	tokens  int
	calls   int
}

// addCall records one raw LLM call.
func (a *auxUsage) addCall(u types.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.costUSD += u.CostUSD
	a.tokens += u.TotalTokens
	a.calls++
}

// addTurn records every call of one agent turn.
func (a *auxUsage) addTurn(r *agent.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.costUSD += r.TurnUsage.CostUSD
	a.tokens += r.TurnUsage.TotalTokens
	a.calls += r.TurnLLMCalls
}

// applyTo adds the collected calls to a run's totals.
func (a *auxUsage) applyTo(c *loomv1.WorkflowCost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c.TotalCostUsd += a.costUSD
	c.TotalTokens += types.SafeInt32(a.tokens)
	c.LlmCalls += types.SafeInt32(a.calls)
}

// absorb moves another collector's calls into a. Used when a run creates a
// short-lived inner executor (the iterative pipeline's per-stage executor).
func (a *auxUsage) absorb(other *auxUsage) {
	other.mu.Lock()
	cost, tokens, calls := other.costUSD, other.tokens, other.calls
	other.costUSD, other.tokens, other.calls = 0, 0, 0
	other.mu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.costUSD += cost
	a.tokens += tokens
	a.calls += calls
}
