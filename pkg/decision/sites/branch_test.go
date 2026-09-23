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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestBranchRequest(t *testing.T) {
	t.Parallel()
	req, err := BranchRequest(strings.Repeat("x", maxBranchPromptRunes+5), []string{"feature", "bug"})
	require.NoError(t, err)
	assert.Equal(t, SiteWorkflowBranch, req.Site)
	require.Contains(t, req.Questions, QBranch)
	opts := req.Questions[QBranch].GetChoice().Options
	assert.Len(t, opts, 3, "two keys plus none_of_these")
	assert.Contains(t, opts, "bug")
	assert.Contains(t, opts, "feature")
	assert.Contains(t, opts, BranchNoneOfThese)

	state := req.State.GetStructValue().AsMap()
	assert.True(t, strings.HasSuffix(state["condition"].(string), "…"), "prompt truncated")
	keys := state["branch_keys"].([]any)
	assert.Equal(t, []any{"bug", "feature"}, keys, "keys sorted for determinism")

	_, err = BranchRequest("p", nil)
	assert.ErrorIs(t, err, decision.ErrValidation)
	_, err = BranchRequest("p", []string{""})
	assert.ErrorIs(t, err, decision.ErrValidation)
}

func TestBranchReferenceAndChosen(t *testing.T) {
	t.Parallel()
	refs := BranchReference("bug")
	assert.Equal(t, "bug", refs[QBranch].Answer)
	assert.Equal(t, ReferenceSourceSelectBranch, refs[QBranch].Source)
	assert.Equal(t, BranchNoneOfThese, BranchReference("")[QBranch].Answer, "empty selection is none_of_these")

	req, err := BranchRequest("Classify: the app crashes on start", []string{"bug", "feature"})
	require.NoError(t, err)
	m := mock.New().AnswerChoice(QBranch, map[string]float64{"bug": 0.9, "feature": 0.05, BranchNoneOfThese: 0.05})
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)
	key, ok := BranchChosen(out.Response)
	assert.True(t, ok)
	assert.Equal(t, "bug", key)
	assert.InDelta(t, 0.85, out.Confidence, 1e-9, "(3·0.9−1)/2")

	records := decision.BuildShadowRecords(req, out, "mock", "s", BranchReference("bug"))
	require.Len(t, records, 1)
	assert.Equal(t, records[0].CandidateAnswer, records[0].ReferenceAnswer)

	_, ok = BranchChosen(nil)
	assert.False(t, ok)
}
