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

// auxUsage collects the LLM calls of a run that no returned AgentResult
// carries: calls the executor makes itself (a merge, a stage validation, a
// swarm judge, a conditional classifier) and the cost of stage attempts that a
// retry replaced or discarded. Executors are built per run, so one auxUsage
// covers one run. It adds to the run totals only; agent_costs_usd stays the
// sum of the returned results. Safe for concurrent use.
//
// auxUsage is not checkpointed. When a pipeline suspends at a HITL gate and
// later resumes, the calls collected before the suspension are not in the
// resumed run's totals; the checkpointed AgentResults are.
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

// addResultCost records the cost of a stage attempt whose AgentResult the
// run discards (a retry replaced it, or the retry failed validation). A nil
// cost carries nothing and is skipped, as calculateCost skips it.
func (a *auxUsage) addResultCost(c *loomv1.AgentExecutionCost) {
	if c == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.costUSD += c.CostUsd
	a.tokens += int(c.TotalTokens)
	a.calls += int(resultLLMCalls(c))
}

// applyTo adds the collected calls to a run's totals.
func (a *auxUsage) applyTo(c *loomv1.WorkflowCost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c.TotalCostUsd += a.costUSD
	c.TotalTokens += types.SafeInt32(a.tokens)
	c.LlmCalls += types.SafeInt32(a.calls)
}
