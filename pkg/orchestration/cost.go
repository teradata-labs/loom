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

package orchestration

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
)

// addUsageToCost folds one LLM call's usage into a workflow cost under the
// given agent id. Executors use it for the calls that are not a stage or
// branch result of their own (a condition evaluation, a validation call), so
// a workflow's reported cost covers every call it made.
func addUsageToCost(cost *loomv1.WorkflowCost, agentID string, u types.Usage) {
	if cost == nil {
		return
	}
	cost.TotalCostUsd += u.CostUSD
	cost.TotalTokens += types.SafeInt32(u.TotalTokens)
	cost.LlmCalls++
	if cost.AgentCostsUsd == nil {
		cost.AgentCostsUsd = make(map[string]float64)
	}
	cost.AgentCostsUsd[agentID] += u.CostUSD
}

// mergeCost returns a copy of base with extra folded in. A nil base is an
// empty cost; a nil extra is a no-op. Neither input is modified.
func mergeCost(base, extra *loomv1.WorkflowCost) *loomv1.WorkflowCost {
	out := &loomv1.WorkflowCost{AgentCostsUsd: make(map[string]float64)}
	for _, c := range []*loomv1.WorkflowCost{base, extra} {
		if c == nil {
			continue
		}
		out.TotalCostUsd += c.TotalCostUsd
		out.TotalTokens += c.TotalTokens
		out.LlmCalls += c.LlmCalls
		for k, v := range c.AgentCostsUsd {
			out.AgentCostsUsd[k] += v
		}
	}
	return out
}
