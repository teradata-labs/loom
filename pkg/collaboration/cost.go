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

package collaboration

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
)

// addDebateUsage folds one LLM call into the debate's workflow cost under
// agentID. Every call a debate makes goes through here: positions,
// responses, moderator summaries and the final synthesis. Before this the
// debate result reported zero cost and zero calls.
func addDebateUsage(cost *loomv1.WorkflowCost, agentID string, u types.Usage) {
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

// positionCost renders a position's usage fields as an AgentExecutionCost
// for the workflow's AgentResult.
func positionCost(p *loomv1.AgentPosition) *loomv1.AgentExecutionCost {
	if p == nil {
		return nil
	}
	return &loomv1.AgentExecutionCost{
		TotalTokens:  p.TotalTokens,
		InputTokens:  p.InputTokens,
		OutputTokens: p.OutputTokens,
		CostUsd:      p.CostUsd,
	}
}
