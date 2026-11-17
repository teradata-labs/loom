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

func TestValidationRequest(t *testing.T) {
	t.Parallel()
	req, err := ValidationRequest("Is {{output}} a markdown table with a header row?", strings.Repeat("x", maxValidationOutputRunes))
	require.NoError(t, err)
	assert.Equal(t, SiteStageValidation, req.Site)
	require.Contains(t, req.Questions, QOutputValid)
	require.NotNil(t, req.Questions[QOutputValid].GetNoul())

	state := req.State.GetStructValue().AsMap()
	assert.Equal(t, "Is the output a markdown table with a header row?", state["requirement"], "placeholder replaced by a stand-in")
	assert.Len(t, []rune(state["output"].(string)), maxValidationOutputRunes, "an output at the limit is carried whole")
	assert.Len(t, state, 2)

	_, err = ValidationRequest("   ", "out")
	assert.ErrorIs(t, err, decision.ErrValidation)
	_, err = ValidationRequest("{{output}}", "out")
	assert.NoError(t, err, "a bare placeholder still leaves a requirement")
}

// Review #410 M5: over a limit there is no request, never a cut one, so the
// LLM validator (which reads both whole) decides.
func TestValidationRequestOverLimitIsNotAsked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, requirement, output, field string
	}{
		{"output", "is it complete?", strings.Repeat("é", maxValidationOutputRunes+1), "state.output"},
		{"requirement", strings.Repeat("r", maxValidationRequirementRunes+1), "out", "state.requirement"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, err := ValidationRequest(tt.requirement, tt.output)
			assert.Nil(t, req)
			assert.ErrorIs(t, err, decision.ErrStateTooLarge)
			var ve *decision.ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Equal(t, tt.field, ve.Field)
		})
	}
}

func TestValidationReferenceAndVerdict(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "true", ValidationReference(true)[QOutputValid].Answer)
	assert.Equal(t, "false", ValidationReference(false)[QOutputValid].Answer)
	assert.Equal(t, ReferenceSourceValidationLLM, ValidationReference(true)[QOutputValid].Source)

	req, err := ValidationRequest("must mention a table", "SELECT * FROM orders")
	require.NoError(t, err)
	out := decision.NewRouter(mock.New().AnswerNoul(QOutputValid, 0.1)).Decide(context.Background(), req)
	require.NoError(t, out.Err)
	valid, ok := ValidationVerdict(out.Response, decision.Band{})
	assert.True(t, ok)
	assert.False(t, valid)
	assert.InDelta(t, 0.8, out.Confidence, 1e-9, "decisiveness |2p−1|")

	records := decision.BuildShadowRecords(req, out, "mock", "s", ValidationReference(true))
	require.Len(t, records, 1)
	assert.Equal(t, "false", records[0].CandidateAnswer)
	assert.Equal(t, "true", records[0].ReferenceAnswer, "the disagreement is what a shadow row is for")

	_, ok = ValidationVerdict(nil, decision.Band{})
	assert.False(t, ok)
}
