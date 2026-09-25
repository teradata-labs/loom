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
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Kind strings recorded on shadow rows.
const (
	KindNoul   = "noul"
	KindChoice = "choice"
	KindScore  = "score"
)

// Reference is what a call site's existing mechanism answered for one
// question, rendered the same way CandidateAnswer renders a decider answer,
// plus the name of that mechanism.
type Reference struct {
	// Answer is "true"/"false" for a noul question, an option key for a
	// choice, a level index ("0".."n-1") for a score. Empty means the site
	// had no reference answer for this question.
	Answer string
	// Source names the mechanism ("llm_rerank", "fabric.InferErrorType").
	Source string
	// Subject identifies what the question was about (see
	// DecisionShadowRecord.subject) so the row can be graded against an
	// external truth later. Optional; a Reference may carry only a Subject
	// when the site acted and has no reference answer.
	Subject string
}

// ShadowQuery selects shadow rows for a report.
type ShadowQuery struct {
	// Site filters to one call site; empty means all.
	Site string
	// Since excludes rows recorded before it; zero means no lower bound.
	Since time.Time
	// Limit caps the rows returned, newest first; zero means the store's
	// default cap.
	Limit int
}

// ShadowStore persists shadow comparisons. Implemented on SQLite and
// Postgres under pkg/storage; the report CLI reads through it.
type ShadowStore interface {
	// RecordShadow appends rows. Ids are assigned by the store.
	RecordShadow(ctx context.Context, records []*loomv1.DecisionShadowRecord) error
	// QueryShadow returns rows newest first.
	QueryShadow(ctx context.Context, q ShadowQuery) ([]*loomv1.DecisionShadowRecord, error)
}

// CandidateAnswer renders a decider answer as the string a shadow row and a
// Reference carry: "true"/"false" for noul (at 0.5), the option key for
// choice, the argmax level index for score. A nil or empty answer is "".
func CandidateAnswer(a *loomv1.DecisionAnswer) string {
	if a == nil {
		return ""
	}
	switch k := a.Kind.(type) {
	case *loomv1.DecisionAnswer_Noul:
		if k.Noul == nil {
			return ""
		}
		return strconv.FormatBool(k.Noul.Probability >= 0.5)
	case *loomv1.DecisionAnswer_Choice:
		if k.Choice == nil {
			return ""
		}
		return k.Choice.Choice
	case *loomv1.DecisionAnswer_Score:
		if k.Score == nil || len(k.Score.Probabilities) == 0 {
			return ""
		}
		return argmax(k.Score.Probabilities)
	}
	return ""
}

// answerKind returns the kind string for an answer, or "".
func answerKind(a *loomv1.DecisionAnswer) string {
	if a == nil {
		return ""
	}
	switch a.Kind.(type) {
	case *loomv1.DecisionAnswer_Noul:
		return KindNoul
	case *loomv1.DecisionAnswer_Choice:
		return KindChoice
	case *loomv1.DecisionAnswer_Score:
		return KindScore
	}
	return ""
}

// questionKind returns the kind string for a question, or "".
func questionKind(q *loomv1.DecisionQuestion) string {
	if q == nil {
		return ""
	}
	switch q.Kind.(type) {
	case *loomv1.DecisionQuestion_Noul:
		return KindNoul
	case *loomv1.DecisionQuestion_Choice:
		return KindChoice
	case *loomv1.DecisionQuestion_Score:
		return KindScore
	}
	return ""
}

// candidateProbabilities flattens an answer's distribution for the record.
func candidateProbabilities(a *loomv1.DecisionAnswer) map[string]float64 {
	if a == nil {
		return nil
	}
	switch k := a.Kind.(type) {
	case *loomv1.DecisionAnswer_Noul:
		if k.Noul == nil {
			return nil
		}
		return map[string]float64{"true": k.Noul.Probability}
	case *loomv1.DecisionAnswer_Choice:
		if k.Choice == nil {
			return nil
		}
		return cloneProbabilities(k.Choice.Probabilities)
	case *loomv1.DecisionAnswer_Score:
		if k.Score == nil {
			return nil
		}
		return cloneProbabilities(k.Score.Probabilities)
	}
	return nil
}

func cloneProbabilities(in map[string]float64) map[string]float64 {
	if in == nil {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// BuildShadowRecords turns one routed outcome plus the site's reference
// answers into one row per question, in question-id order. When the decider
// produced no response (ERROR, DISABLED, BUDGET) one row per question is
// still emitted with an empty candidate so reports can count decider
// failures against the same denominator. Provider names the decider
// ("jev", "llm:anthropic"); sessionID may be "".
func BuildShadowRecords(req *loomv1.DecisionRequest, out Outcome, provider, sessionID string, refs map[string]Reference) []*loomv1.DecisionShadowRecord {
	if req == nil || len(req.Questions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	now := timestamppb.Now()
	model := ""
	var tokens int64
	var cost float64
	if out.Response != nil {
		model = out.Response.Model
		if out.Response.Usage != nil {
			tokens = out.Response.Usage.InputTokens
			cost = out.Response.Usage.CostUsd
		}
	}

	records := make([]*loomv1.DecisionShadowRecord, 0, len(ids))
	for _, id := range ids {
		var ans *loomv1.DecisionAnswer
		if out.Response != nil {
			ans = out.Response.Answers[id]
		}
		kind := answerKind(ans)
		if kind == "" {
			kind = questionKind(req.Questions[id])
		}
		ref := refs[id]
		records = append(records, &loomv1.DecisionShadowRecord{
			RecordedAt:             now,
			Site:                   req.Site,
			SessionId:              sessionID,
			QuestionId:             id,
			Kind:                   kind,
			CandidateAnswer:        CandidateAnswer(ans),
			CandidateConfidence:    AnswerConfidence(ans),
			CandidateProbabilities: candidateProbabilities(ans),
			ReferenceAnswer:        ref.Answer,
			ReferenceSource:        ref.Source,
			LatencyMs:              out.Latency.Milliseconds(),
			InputTokens:            tokens,
			CostUsd:                cost,
			Model:                  model,
			Provider:               provider,
			Path:                   out.Path,
			Error:                  errorText(out.Err),
			Subject:                ref.Subject,
		})
	}
	return records
}

// maxErrorTextRunes bounds the error carried on a shadow row; a gateway
// body can be long and the first few hundred runes hold the reason.
const maxErrorTextRunes = 500

// errorText renders an outcome error for a shadow row, empty for nil.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if utf8.RuneCountInString(s) <= maxErrorTextRunes {
		return s
	}
	return string([]rune(s)[:maxErrorTextRunes]) + "…"
}

// ShadowRecorder writes shadow rows and counts store failures as metrics
// instead of surfacing them: a shadow comparison must never fail the call
// site it observes.
type ShadowRecorder struct {
	store  ShadowStore
	tracer observability.Tracer
}

// NewShadowRecorder wraps a store. A nil store records nothing; a nil tracer
// counts nothing.
func NewShadowRecorder(store ShadowStore, tracer observability.Tracer) *ShadowRecorder {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &ShadowRecorder{store: store, tracer: tracer}
}

// Enabled reports whether a store is wired.
func (r *ShadowRecorder) Enabled() bool { return r != nil && r.store != nil }

// Record writes rows, swallowing and counting any store error. It returns
// the error only so tests can observe it.
func (r *ShadowRecorder) Record(ctx context.Context, records []*loomv1.DecisionShadowRecord) error {
	if !r.Enabled() || len(records) == 0 {
		return nil
	}
	err := r.store.RecordShadow(ctx, records)
	site := records[0].Site
	if err != nil {
		r.tracer.RecordMetric(observability.MetricDecisionShadowErrors, 1, map[string]string{AttrDecisionSite: site})
		return err
	}
	r.tracer.RecordMetric(observability.MetricDecisionShadowRows, float64(len(records)), map[string]string{AttrDecisionSite: site})
	return nil
}
