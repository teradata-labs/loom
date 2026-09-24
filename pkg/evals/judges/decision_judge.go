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

package judges

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	decisionllm "github.com/teradata-labs/loom/pkg/decision/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// DecisionJudge is a Judge backed by a typed decision model (pkg/decision,
// for example Jev) instead of a generative LLM. Each criterion becomes one
// Noul ("does the response satisfy this criterion?") and overall quality one
// Score, all in a single decider request. The verdict comes from the
// criterion probabilities; the confidence is the decider's, not a number a
// model wrote into prose. It fits the same multi-judge framework as
// LLMJudge: weight, criticality, dimensions, aggregation.
type DecisionJudge struct {
	id       string
	config   *loomv1.JudgeConfig
	decider  decision.Decider
	router   *decision.Router
	tracer   observability.Tracer
	criteria []string
	site     string
}

// Compile-time interface check.
var _ Judge = (*DecisionJudge)(nil)

// judgeSitePrefix is the decision site name prefix; the judge id follows, so
// bands, spans and shadow rows are per judge.
const judgeSitePrefix = "judge."

// Question ids in a judge request.
const (
	judgeQualityQuestion = "quality"
	judgeCriterionPrefix = "crit"
)

// Quality levels, lowest first; the Score answer's expected level index maps
// onto 0..100.
var judgeQualityLevels = []any{"unusable", "weak", "adequate", "good", "excellent"}

// defaultJudgeCriteria is used when the config names none; it mirrors the
// LLM judge's default dimensions.
var defaultJudgeCriteria = []string{
	"The response answers what the request asked.",
	"The response is complete for what was asked; nothing required is missing.",
	"The response states no facts that the request or general knowledge contradicts.",
}

const (
	maxJudgePromptRunes   = 6000
	maxJudgeResponseRunes = 12000
)

// NewDecisionJudge builds a DecisionJudge over an already constructed
// decider. Defaults follow LLMJudge: weight 1, minimum passing score 80,
// critical criticality.
func NewDecisionJudge(decider decision.Decider, config *loomv1.JudgeConfig, tracer observability.Tracer) (*DecisionJudge, error) {
	if config == nil {
		return nil, errors.New("judge config is required")
	}
	if decider == nil {
		return nil, errors.New("decision judge: decider is required")
	}
	if err := validateCustomDimensions(config); err != nil {
		return nil, fmt.Errorf("invalid custom dimension config: %w", err)
	}
	if config.Weight == 0 {
		config.Weight = 1.0
	}
	if config.MinPassingScore == 0 {
		config.MinPassingScore = 80
	}
	if config.Criticality == loomv1.JudgeCriticality_JUDGE_CRITICALITY_UNSPECIFIED {
		config.Criticality = loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL
	}
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	id := config.Id
	if id == "" {
		id = uuid.New().String()
	}
	criteria := SplitCriteria(config.Criteria)
	if len(criteria) == 0 {
		criteria = append([]string(nil), defaultJudgeCriteria...)
	}
	site := judgeSitePrefix + id
	size := 0
	if dc := config.GetDecision(); dc != nil {
		size = int(dc.MaxQuestionsPerRequest)
	}
	decider = decision.Chunk(decider, decision.WithChunkSize(size))
	router := decision.NewRouter(decision.NewInstrumented(decider, tracer),
		decision.WithTracer(tracer),
		// A judge always acts on the decider's answer; its confidence is
		// reported, not used as a gate.
		decision.WithBands([]*loomv1.DecisionBand{{Site: site, ActMin: 0, Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE}}),
	)
	return &DecisionJudge{id: id, config: config, decider: decider, router: router, tracer: tracer, criteria: criteria, site: site}, nil
}

// NewDecisionJudgeFromConfig builds the decider named by config.decision:
// "jev" (credentials from the environment, client shared per process) or
// "llm" (the decision adapter over fallback, the judge-role LLM).
func NewDecisionJudgeFromConfig(config *loomv1.JudgeConfig, fallback types.LLMProvider, tracer observability.Tracer) (*DecisionJudge, error) {
	if config == nil {
		return nil, errors.New("judge config is required")
	}
	dc := config.GetDecision()
	if dc == nil {
		return nil, errors.New("decision judge: config.decision is required (provider jev or llm)")
	}
	var decider decision.Decider
	switch strings.ToLower(strings.TrimSpace(dc.Provider)) {
	case "jev":
		jcfg, err := jev.FromDecisionConfig(dc)
		if err != nil {
			return nil, fmt.Errorf("decision judge: %w", err)
		}
		client, err := jev.Shared(jcfg)
		if err != nil {
			return nil, fmt.Errorf("decision judge: %w", err)
		}
		decider = client
	case "llm":
		if fallback == nil {
			return nil, errors.New("decision judge: provider llm needs an LLM provider")
		}
		var opts []decisionllm.Option
		if dc.TimeoutMs > 0 {
			opts = append(opts, decisionllm.WithTimeout(time.Duration(dc.TimeoutMs)*time.Millisecond))
		}
		decider = decisionllm.New(fallback, opts...)
	default:
		return nil, fmt.Errorf("decision judge: unsupported decision.provider %q (jev or llm)", dc.Provider)
	}
	return NewDecisionJudge(decider, config, tracer)
}

// NewJudgeFromConfig is the one constructor callers should use: it builds a
// DecisionJudge for JUDGE_TYPE_DECISION and an LLMJudge otherwise, so a
// judge's implementation is chosen by its config, not by the call site.
func NewJudgeFromConfig(llmProvider types.LLMProvider, config *loomv1.JudgeConfig, tracer observability.Tracer, opts ...LLMJudgeOption) (Judge, error) {
	if config != nil && config.Type == loomv1.JudgeType_JUDGE_TYPE_DECISION {
		return NewDecisionJudgeFromConfig(config, llmProvider, tracer)
	}
	return NewLLMJudge(llmProvider, config, tracer, opts...)
}

// SplitCriteria turns a criteria string into individual criteria: one per
// line, with list markers ("- ", "* ", "1.", "1)") removed; a single line
// containing semicolons splits on them. Empty results are dropped.
func SplitCriteria(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) == 1 && strings.Contains(s, ";") {
		lines = strings.Split(s, ";")
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "-*• ")
		// Numbered markers: "1." or "1)" followed by a space.
		if i := strings.IndexAny(l, ".)"); i > 0 && i <= 3 && strings.TrimSpace(l[:i]) != "" && isDigits(l[:i]) {
			l = strings.TrimSpace(l[i+1:])
		}
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (j *DecisionJudge) ID() string                           { return j.id }
func (j *DecisionJudge) Name() string                         { return j.config.Name }
func (j *DecisionJudge) Criteria() []string                   { return j.criteria }
func (j *DecisionJudge) Weight() float64                      { return j.config.Weight }
func (j *DecisionJudge) Config() *loomv1.JudgeConfig          { return j.config }
func (j *DecisionJudge) Criticality() loomv1.JudgeCriticality { return j.config.Criticality }

// Dimensions returns the configured dimensions, defaulting to quality.
func (j *DecisionJudge) Dimensions() []loomv1.JudgeDimension {
	if len(j.config.Dimensions) > 0 {
		return j.config.Dimensions
	}
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}

// Site is the decision site name this judge records under.
func (j *DecisionJudge) Site() string { return j.site }

func criterionID(i int) string { return fmt.Sprintf("%s%d", judgeCriterionPrefix, i) }

// buildRequest renders the evaluation as one decider request: state is the
// request, the response and the criteria; questions are one Noul per
// criterion and a Score for overall quality.
func (j *DecisionJudge) buildRequest(evalCtx *loomv1.EvaluationContext) (*loomv1.DecisionRequest, error) {
	crits := make([]any, 0, len(j.criteria))
	questions := make(map[string]*loomv1.DecisionQuestion, len(j.criteria)+1)
	for i, c := range j.criteria {
		id := criterionID(i)
		crits = append(crits, map[string]any{"id": id, "text": c})
		questions[id] = decision.Noul(
			fmt.Sprintf("Does the response satisfy criterion %s?", id),
			decision.WithCriteria(
				"the response meets that criterion as written",
				"the response misses, contradicts, or only partially meets that criterion",
			),
		)
	}
	quality, err := decision.Score("How good is the response as an answer to the request, all things considered?", judgeQualityLevels...)
	if err != nil {
		return nil, err
	}
	questions[judgeQualityQuestion] = quality
	state := map[string]any{
		"request":  truncateRunes(evalCtx.GetPrompt(), maxJudgePromptRunes),
		"response": truncateRunes(evalCtx.GetResponse(), maxJudgeResponseRunes),
		"criteria": crits,
	}
	req, err := decision.NewRequest(j.site, state, questions)
	if err != nil {
		return nil, err
	}
	// The criteria list pairs with the criterion questions; the quality
	// question has no item and sees the whole list when chunked.
	req.FanOutKey = "criteria"
	return req, nil
}

// Evaluate implements Judge.
func (j *DecisionJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	ctx, span := j.tracer.StartSpan(ctx, observability.SpanJudgeEvaluation)
	defer j.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("judge.name", j.Name())
		span.SetAttribute("judge.id", j.ID())
		span.SetAttribute("judge.type", "decision")
		span.SetAttribute("judge.criticality", j.Criticality().String())
	}
	start := time.Now()
	fail := func(err error) (*loomv1.JudgeResult, error) {
		return &loomv1.JudgeResult{
			JudgeId: j.ID(), JudgeName: j.Name(), JudgeModel: j.decider.Model(),
			Error: err.Error(), Verdict: "FAIL", JudgedAt: timestamppb.Now(),
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, fmt.Errorf("decision judge failed: %w", err)
	}

	req, err := j.buildRequest(evalCtx)
	if err != nil {
		return fail(err)
	}
	out := j.router.Decide(ctx, req)
	if out.Err != nil {
		return fail(out.Err)
	}
	if out.Response == nil {
		return fail(fmt.Errorf("no answer (path %s)", out.Path))
	}

	var (
		sum       float64
		answered  int
		satisfied int
		issues    []string
		reasoning strings.Builder
	)
	for i, c := range j.criteria {
		a, err := decision.NoulOf(out.Response, criterionID(i))
		if err != nil || a == nil {
			issues = append(issues, fmt.Sprintf("criterion %d unanswered: %s", i+1, c))
			continue
		}
		answered++
		sum += a.Probability
		met := a.Probability >= 0.5
		if met {
			satisfied++
		} else {
			issues = append(issues, fmt.Sprintf("criterion %d not met (p=%.2f): %s", i+1, a.Probability, c))
		}
		fmt.Fprintf(&reasoning, "criterion %d: p(met)=%.2f %s\n", i+1, a.Probability, c)
	}
	if answered == 0 {
		return fail(errors.New("decider answered no criterion"))
	}
	overall := 100 * sum / float64(answered)
	completeness := 100 * float64(satisfied) / float64(answered)

	qualityPct := math.NaN()
	if q, err := decision.ScoreOf(out.Response, judgeQualityQuestion); err == nil && q != nil && len(judgeQualityLevels) > 1 {
		qualityPct = 100 * q.Score / float64(len(judgeQualityLevels)-1)
		fmt.Fprintf(&reasoning, "quality: expected level %.2f of %d (%.0f/100)\n", q.Score, len(judgeQualityLevels)-1, qualityPct)
	}
	fmt.Fprintf(&reasoning, "decider confidence (min over questions): %.2f", out.Confidence)

	verdict := "PARTIAL"
	switch {
	case satisfied == answered && overall >= float64(j.config.MinPassingScore):
		verdict = "PASS"
	case satisfied == 0 || overall < 50:
		verdict = "FAIL"
	}

	dims := map[string]float64{
		"correctness":  overall,
		"completeness": completeness,
	}
	if !math.IsNaN(qualityPct) {
		dims["quality"] = qualityPct
	}
	if j.config.CustomDimensionName != "" {
		for _, d := range j.config.Dimensions {
			if d == loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM {
				dims[j.config.CustomDimensionName] = overall / 100.0
				break
			}
		}
	}

	res := &loomv1.JudgeResult{
		JudgeId:         j.ID(),
		JudgeName:       j.Name(),
		JudgeModel:      j.decider.Model(),
		Criteria:        j.criteria,
		FactualAccuracy: types.SafeInt32(int(math.Round(overall))),
		Completeness:    types.SafeInt32(int(math.Round(completeness))),
		OverallScore:    overall,
		Verdict:         verdict,
		Reasoning:       strings.TrimSpace(reasoning.String()),
		Issues:          issues,
		DimensionScores: dims,
		JudgedAt:        timestamppb.Now(),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
		CostUsd:         out.Response.GetUsage().GetCostUsd(),
	}
	if !math.IsNaN(qualityPct) {
		res.QueryQuality = types.SafeInt32(int(math.Round(qualityPct)))
	}
	if span != nil {
		span.SetAttribute("judge.verdict", verdict)
		span.SetAttribute("judge.overall_score", fmt.Sprintf("%.1f", overall))
		span.SetAttribute("judge.decider_confidence", fmt.Sprintf("%.2f", out.Confidence))
	}
	return res, nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
