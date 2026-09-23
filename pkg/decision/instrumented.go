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

package decision

import (
	"context"
	"fmt"
	"sort"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Span attribute keys for decision spans.
const (
	AttrDecisionProvider     = "decision.provider"
	AttrDecisionModel        = "decision.model"
	AttrDecisionSite         = "decision.site"
	AttrDecisionQuestions    = "decision.questions"
	AttrDecisionQuestionIDs  = "decision.question_ids"
	AttrDecisionConfidence   = "decision.confidence"
	AttrDecisionInputTokens  = "decision.input_tokens"
	AttrDecisionOutputTokens = "decision.output_tokens"
	AttrDecisionCostUSD      = "decision.cost_usd"
	AttrDecisionLatencyMs    = "decision.latency_ms"
	AttrDecisionSessionID    = "decision.session_id"
)

// Instrumented wraps a Decider with tracing and metrics, the way
// pkg/llm.InstrumentedProvider wraps an LLMProvider. Every decision becomes a
// span and a set of metrics next to llm.*, which is what makes the auxiliary
// judgments that used to be uncounted generative calls visible.
type Instrumented struct {
	decider Decider
	tracer  observability.Tracer
}

// NewInstrumented wraps d. A nil tracer records nothing.
func NewInstrumented(d Decider, tracer observability.Tracer) *Instrumented {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &Instrumented{decider: d, tracer: tracer}
}

// Name delegates to the wrapped decider.
func (i *Instrumented) Name() string { return i.decider.Name() }

// Model delegates to the wrapped decider.
func (i *Instrumented) Model() string { return i.decider.Model() }

// Unwrap returns the wrapped decider.
func (i *Instrumented) Unwrap() Decider { return i.decider }

// Decide runs the wrapped decider inside a decision.evaluate span.
func (i *Instrumented) Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	ctx, span := i.tracer.StartSpan(ctx, observability.SpanDecisionEvaluate)
	defer i.tracer.EndSpan(span)

	provider, model := i.decider.Name(), i.decider.Model()
	site := ""
	questionCount := 0
	var ids []string
	if req != nil {
		site = req.Site
		questionCount = len(req.Questions)
		ids = make([]string, 0, questionCount)
		for id := range req.Questions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	labels := map[string]string{
		AttrDecisionProvider: provider,
		AttrDecisionModel:    model,
		AttrDecisionSite:     site,
	}

	span.SetAttribute(AttrDecisionProvider, provider)
	span.SetAttribute(AttrDecisionModel, model)
	span.SetAttribute(AttrDecisionSite, site)
	span.SetAttribute(AttrDecisionQuestions, questionCount)
	span.SetAttribute(AttrDecisionQuestionIDs, ids)
	if sid := SessionIDFromContext(ctx); sid != "" {
		span.SetAttribute(AttrDecisionSessionID, sid)
	}

	start := time.Now()
	resp, err := i.decider.Decide(ctx, req)
	latency := time.Since(start)
	span.SetAttribute(AttrDecisionLatencyMs, latency.Milliseconds())

	i.tracer.RecordMetric(observability.MetricDecisionCalls, 1, labels)
	i.tracer.RecordMetric(observability.MetricDecisionLatency, float64(latency.Milliseconds()), labels)

	if err != nil {
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		span.SetAttribute(observability.AttrErrorType, fmt.Sprintf("%T", err))
		span.SetAttribute(observability.AttrErrorMessage, err.Error())
		errLabels := map[string]string{
			AttrDecisionProvider:        provider,
			AttrDecisionModel:           model,
			AttrDecisionSite:            site,
			observability.AttrErrorType: fmt.Sprintf("%T", err),
		}
		i.tracer.RecordMetric(observability.MetricDecisionErrors, 1, errLabels)
		return nil, err
	}

	if resp != nil {
		if resp.Model != "" {
			span.SetAttribute(AttrDecisionModel, resp.Model)
		}
		if resp.Usage != nil {
			span.SetAttribute(AttrDecisionInputTokens, resp.Usage.InputTokens)
			span.SetAttribute(AttrDecisionOutputTokens, resp.Usage.OutputTokens)
			span.SetAttribute(AttrDecisionCostUSD, resp.Usage.CostUsd)
			i.tracer.RecordMetric(observability.MetricDecisionTokens, float64(resp.Usage.InputTokens), labels)
			i.tracer.RecordMetric(observability.MetricDecisionCost, resp.Usage.CostUsd, labels)
		}
		span.SetAttribute(AttrDecisionConfidence, MinConfidence(resp))
		for _, id := range ids {
			span.SetAttribute(AttrDecisionConfidence+"."+id, AnswerConfidence(resp.Answers[id]))
		}
	}
	return resp, nil
}
