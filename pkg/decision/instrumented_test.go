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

package decision_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestInstrumentedSuccessSpanAndMetrics(t *testing.T) {
	t.Parallel()
	tracer := newRecordingTracer()
	m := mock.New().AnswerNoul("q", 0.9).SetUsage(123, 0, 0.0007).SetModel("mock-9")
	d := decision.NewInstrumented(m, tracer)
	assert.Equal(t, "mock", d.Name())
	assert.Equal(t, "mock-9", d.Model())
	assert.Same(t, m, d.Unwrap())

	ctx := decision.WithSessionID(context.Background(), "sess-1")
	resp, err := d.Decide(ctx, noulRequest(t, "recall.rerank"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	spans := tracer.GetSpans()
	require.Len(t, spans, 1)
	sp := spans[0]
	assert.Equal(t, observability.SpanDecisionEvaluate, sp.Name)
	assert.Equal(t, "mock", sp.Attributes[decision.AttrDecisionProvider])
	assert.Equal(t, "mock-9", sp.Attributes[decision.AttrDecisionModel])
	assert.Equal(t, "recall.rerank", sp.Attributes[decision.AttrDecisionSite])
	assert.Equal(t, 1, sp.Attributes[decision.AttrDecisionQuestions])
	assert.Equal(t, []string{"q"}, sp.Attributes[decision.AttrDecisionQuestionIDs])
	assert.Equal(t, "sess-1", sp.Attributes[decision.AttrDecisionSessionID])
	assert.Equal(t, int64(123), sp.Attributes[decision.AttrDecisionInputTokens])
	assert.InDelta(t, 0.0007, sp.Attributes[decision.AttrDecisionCostUSD].(float64), 1e-12)
	assert.InDelta(t, 0.8, sp.Attributes[decision.AttrDecisionConfidence].(float64), 1e-9)
	assert.InDelta(t, 0.8, sp.Attributes[decision.AttrDecisionConfidence+".q"].(float64), 1e-9)
	assert.Contains(t, sp.Attributes, decision.AttrDecisionLatencyMs)
	assert.NotEqual(t, observability.StatusError, sp.Status.Code)

	for _, name := range []string{
		observability.MetricDecisionCalls,
		observability.MetricDecisionLatency,
		observability.MetricDecisionTokens,
		observability.MetricDecisionCost,
	} {
		ms := tracer.Metrics(name)
		require.Len(t, ms, 1, name)
		assert.Equal(t, "recall.rerank", ms[0].Labels[decision.AttrDecisionSite], name)
		assert.Equal(t, "mock", ms[0].Labels[decision.AttrDecisionProvider], name)
	}
	assert.InDelta(t, 123, tracer.Metrics(observability.MetricDecisionTokens)[0].Value, 1e-9)
	assert.Empty(t, tracer.Metrics(observability.MetricDecisionErrors))
}

func TestInstrumentedErrorSpanAndMetrics(t *testing.T) {
	t.Parallel()
	tracer := newRecordingTracer()
	d := decision.NewInstrumented(mock.New().SetError(decision.ErrOverloaded), tracer)
	_, err := d.Decide(context.Background(), noulRequest(t, "s"))
	assert.True(t, errors.Is(err, decision.ErrOverloaded))

	spans := tracer.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, observability.StatusError, spans[0].Status.Code)
	assert.Contains(t, spans[0].Attributes, observability.AttrErrorType)
	assert.Contains(t, spans[0].Attributes, decision.AttrDecisionLatencyMs)

	errs := tracer.Metrics(observability.MetricDecisionErrors)
	require.Len(t, errs, 1)
	assert.Equal(t, "s", errs[0].Labels[decision.AttrDecisionSite])
	assert.Len(t, tracer.Metrics(observability.MetricDecisionCalls), 1, "calls are counted even on error")
	assert.Empty(t, tracer.Metrics(observability.MetricDecisionCost))
}

func TestInstrumentedNilTracerAndNilRequest(t *testing.T) {
	t.Parallel()
	d := decision.NewInstrumented(mock.New(), nil)
	_, err := d.Decide(context.Background(), nil)
	assert.True(t, errors.Is(err, decision.ErrValidation), "nil request reaches the decider and is rejected there")
}

func TestInstrumentedConcurrent(t *testing.T) {
	t.Parallel()
	tracer := newRecordingTracer()
	d := decision.NewInstrumented(mock.New().AnswerNoul("q", 0.9), tracer)
	req := noulRequest(t, "c")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Decide(context.Background(), req)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Len(t, tracer.GetSpans(), 32)
	assert.Len(t, tracer.Metrics(observability.MetricDecisionCalls), 32)
}

func TestOff(t *testing.T) {
	t.Parallel()
	var d decision.Decider = decision.Off{}
	assert.Equal(t, "off", d.Name())
	assert.Equal(t, "", d.Model())
	resp, err := d.Decide(context.Background(), &loomv1.DecisionRequest{})
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, decision.ErrDisabled))
}

func TestSessionIDContext(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", decision.SessionIDFromContext(context.Background()))
	assert.Equal(t, "", decision.SessionIDFromContext(nil)) //nolint:staticcheck // nil ctx is the case under test
	ctx := decision.WithSessionID(context.Background(), "abc")
	assert.Equal(t, "abc", decision.SessionIDFromContext(ctx))
}
