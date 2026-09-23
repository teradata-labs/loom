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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
)

func validationBand(actMin float64, mode loomv1.DecisionBandMode) decision.RouterOption {
	return decision.WithBands([]*loomv1.DecisionBand{{Site: sites.SiteStageValidation, ActMin: actMin, Mode: mode}})
}

// stageValidationRig runs one pipeline stage whose output is "a prose answer"
// with a validation prompt. validator is the orchestrator's merge LLM (what
// the generative validator calls); it is a counted mock so a test can prove
// it was or was not consulted.
func stageValidationRig(t *testing.T, dec decision.Decider, store decision.ShadowStore, validatorReply string, opts ...decision.RouterOption) (*loomv1.WorkflowResult, *lvlMockLLM, *lvlMockLLM, *agent.Agent, error) {
	t.Helper()
	validator := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0, validatorReply)
	orch := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		LLMProvider: validator,
	})
	worker := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "a prose answer")
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := agent.NewAgent(&mockBackend{}, worker,
		agent.WithName("worker"),
		agent.WithConfig(cfg),
		agent.WithDecisionRouter(decision.NewRouter(dec, opts...)),
		agent.WithDecisionShadowStore(store))
	orch.RegisterAgent("worker", ag)

	res, err := runLevelingPipeline(t, orch, &loomv1.PipelineStage{
		AgentId:          "worker",
		PromptTemplate:   "{{previous}}",
		ValidationPrompt: "Is {{output}} a complete answer?",
	})
	return res, validator, worker, ag, err
}

func TestStageValidationDecision_LiveBandSkipsValidatorLLM(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul(sites.QOutputValid, 0.95)
	store := &memShadowStore{}
	res, validator, worker, ag, err := stageValidationRig(t, dec, store, "invalid", // would fail it if consulted
		validationBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE))
	require.NoError(t, err)
	assert.Equal(t, "a prose answer", res.MergedOutput)
	assert.Equal(t, 0, validator.count(), "the validator LLM was skipped")
	assert.Equal(t, 1, worker.count(), "no retry: the decider passed the output")
	assert.NotContains(t, res.Metadata, "validation_warnings")

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sites.SiteStageValidation, rows[0].Site)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, rows[0].Path)
	assert.Equal(t, "true", rows[0].CandidateAnswer)
	assert.Equal(t, "", rows[0].ReferenceAnswer)
}

func TestStageValidationDecision_TightenOnlyNeverPassesOnItsOwn(t *testing.T) {
	t.Parallel()
	t.Run("confident valid still runs the LLM", func(t *testing.T) {
		t.Parallel()
		dec := decisionmock.New().AnswerNoul(sites.QOutputValid, 0.95)
		store := &memShadowStore{}
		res, validator, _, ag, err := stageValidationRig(t, dec, store, "valid",
			validationBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY))
		require.NoError(t, err)
		assert.Equal(t, "a prose answer", res.MergedOutput)
		assert.Equal(t, 1, validator.count(), "a gate may only tighten: the LLM still decides a pass")

		ag.WaitDecisionShadows()
		rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
		assert.Equal(t, "true", rows[0].ReferenceAnswer)
		assert.Equal(t, 1, dec.CallCount())
	})
	t.Run("confident invalid acts", func(t *testing.T) {
		t.Parallel()
		dec := decisionmock.New().AnswerNoul(sites.QOutputValid, 0.05)
		res, validator, _, _, err := stageValidationRig(t, dec, &memShadowStore{}, "valid", // would pass it if consulted
			validationBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY))
		// No retry policy: a failed validation is a hard stage error, exactly
		// as it is when the LLM validator fails an output.
		require.Error(t, err)
		assert.Nil(t, res)
		assert.Contains(t, err.Error(), "output validation failed")
		assert.Equal(t, 0, validator.count(), "the decider failed it without the LLM")
	})
}

func TestStageValidationDecision_BelowBandFallsBackAndRecords(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul(sites.QOutputValid, 0.55) // decisiveness 0.1
	store := &memShadowStore{}
	res, validator, _, ag, err := stageValidationRig(t, dec, store, "valid",
		validationBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE))
	require.NoError(t, err)
	assert.Equal(t, "a prose answer", res.MergedOutput)
	assert.Equal(t, 1, validator.count(), "the LLM decided")
	assert.Equal(t, 1, dec.CallCount(), "the live answer is recorded, not re-asked")
	require.NotNil(t, res.Cost)
	assert.Equal(t, int32(2), res.Cost.LlmCalls, "stage worker + validation call; validation used to be missing from the cost")

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "true", rows[0].CandidateAnswer)
	assert.Equal(t, "true", rows[0].ReferenceAnswer)
	assert.Equal(t, sites.ReferenceSourceValidationLLM, rows[0].ReferenceSource)
}

func TestStageValidationDecision_ShadowBandRecordsAgainstScrapedVerdict(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul(sites.QOutputValid, 0.1)
	store := &memShadowStore{}
	_, validator, _, ag, err := stageValidationRig(t, dec, store, "yes, this is fine") // no band: shadow
	require.NoError(t, err)
	assert.Equal(t, 1, validator.count())

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "false", rows[0].CandidateAnswer)
	assert.Equal(t, "true", rows[0].ReferenceAnswer, "the disagreement is recorded, not acted on")
	req := dec.Calls()[0]
	assert.Contains(t, req.State.String(), "Is the output a complete answer?")
	assert.Contains(t, req.State.String(), "a prose answer")
}

func TestStageValidationDecision_DeciderErrorFallsBack(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().SetError(decision.ErrOverloaded)
	res, validator, _, ag, err := stageValidationRig(t, dec, &memShadowStore{}, "valid",
		validationBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE))
	require.NoError(t, err)
	assert.Equal(t, "a prose answer", res.MergedOutput)
	assert.Equal(t, 1, validator.count(), "a decider error never costs the workflow anything")
	ag.WaitDecisionShadows()
}
