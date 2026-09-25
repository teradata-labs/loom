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

package sites

import (
	"sort"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteWorkflowBranch is the conditional workflow's branch selection
// (orchestration.ConditionalExecutor). Today a whole agent turn answers the
// condition prompt with a branch key; here it is one Choice over the keys.
const SiteWorkflowBranch = "workflow.branch"

// QBranch is the single question id at SiteWorkflowBranch.
const QBranch = "branch"

// BranchNoneOfThese is the answer when no branch fits. It is
// decision.NoneOption, named here so callers do not couple to the builder.
const BranchNoneOfThese = decision.NoneOption

// ReferenceSourceSelectBranch labels a reference taken from the branch the
// condition agent's answer selected (after coercion and retry).
const ReferenceSourceSelectBranch = "orchestration.conditional.selectBranch"

// maxBranchPromptRunes bounds the condition prompt carried in state. A
// condition prompt is the text to classify, so it gets more room than a
// rerank query.
const maxBranchPromptRunes = 4000

// BranchRequest builds the request: state is the condition prompt, the
// question is which branch key it calls for, with none_of_these available.
// Keys are the branch keys as configured; the decider answers with one of
// them. It returns a *decision.ValidationError when keys is empty or a key is
// empty, and when there are more keys than a Choice allows.
func BranchRequest(conditionPrompt string, keys []string) (*loomv1.DecisionRequest, error) {
	if len(keys) == 0 {
		return nil, &decision.ValidationError{Field: "questions.branch.options", Msg: "no branch keys"}
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	options := make(map[string]any, len(sorted))
	for _, k := range sorted {
		options[k] = "The condition calls for the branch named " + k + "."
	}
	q, err := decision.Choice(
		"Which branch does the condition text call for?",
		options,
		decision.WithNoneOption("The condition text does not call for any of the named branches."),
	)
	if err != nil {
		return nil, err
	}
	state := map[string]any{
		"condition":   truncateRunes(conditionPrompt, maxBranchPromptRunes),
		"branch_keys": sorted,
	}
	return decision.NewRequest(SiteWorkflowBranch, state, map[string]*loomv1.DecisionQuestion{QBranch: q})
}

// BranchReference renders the branch key the existing mechanism selected.
// Pass BranchNoneOfThese when the mechanism fell through to the default
// branch or matched nothing.
func BranchReference(selectedKey string) map[string]decision.Reference {
	if selectedKey == "" {
		selectedKey = BranchNoneOfThese
	}
	return map[string]decision.Reference{
		QBranch: {Answer: selectedKey, Source: ReferenceSourceSelectBranch},
	}
}

// BranchChosen returns the decider's branch key. ok is false when the
// response carries no usable Choice answer. The key may be
// BranchNoneOfThese; the caller decides whether a default branch makes that
// actionable.
func BranchChosen(resp *loomv1.DecisionResponse) (key string, ok bool) {
	if resp == nil {
		return "", false
	}
	a, err := decision.ChoiceOf(resp, QBranch)
	if err != nil || a == nil || a.Choice == "" {
		return "", false
	}
	return a.Choice, true
}
