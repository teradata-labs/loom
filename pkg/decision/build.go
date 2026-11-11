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
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ToValue converts any JSON-representable Go value to a protobuf Value.
// Scalars convert directly; every composite (maps, slices, structs, []byte)
// goes through a JSON round trip, and proto messages through protojson, so a
// tool schema or a skill manifest can be passed intact.
//
// Invalid UTF-8 is replaced with U+FFFD rather than rejected: state is
// routinely derived from tool results, which can carry arbitrary bytes, and a
// decision request must never fail or panic because of them.
func ToValue(v any) (*structpb.Value, error) {
	switch x := v.(type) {
	case nil:
		return structpb.NewNullValue(), nil
	case *structpb.Value:
		return x, nil
	case string:
		return structpb.NewStringValue(strings.ToValidUTF8(x, "�")), nil
	case bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return structpb.NewValue(x)
	case interface{ ProtoReflect() protoreflectMessage }:
		return protoToValue(x)
	}
	// json.Marshal coerces invalid UTF-8 inside strings to U+FFFD, which is
	// why composites take this path instead of structpb.NewValue directly.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("decision: state is not JSON-representable: %w", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decision: state JSON round trip: %w", err)
	}
	return structpb.NewValue(generic)
}

// mustValue is ToValue for builders whose input is a caller-authored literal.
// It panics on a non-JSON value, which is a programming error at the call
// site, not a runtime condition.
func mustValue(v any, what string) *structpb.Value {
	val, err := ToValue(v)
	if err != nil {
		panic(fmt.Sprintf("decision: %s: %v", what, err))
	}
	return val
}

// Noul builds an "is this true?" question. Instructions state the claim as
// directly as possible; double negatives and indirection degrade every
// decision model.
func Noul(instructions any, opts ...NoulOption) *loomv1.DecisionQuestion {
	q := &loomv1.NoulQuestion{}
	for _, o := range opts {
		o(q)
	}
	return &loomv1.DecisionQuestion{
		Instructions: mustValue(instructions, "noul instructions"),
		Kind:         &loomv1.DecisionQuestion_Noul{Noul: q},
	}
}

// NoulOption configures a Noul question.
type NoulOption func(*loomv1.NoulQuestion)

// WithCriteria describes what makes the answer true and what makes it false.
// Either may be nil.
func WithCriteria(whenTrue, whenFalse any) NoulOption {
	return func(q *loomv1.NoulQuestion) {
		if whenTrue != nil {
			q.CriteriaTrue = mustValue(whenTrue, "noul criteria_true")
		}
		if whenFalse != nil {
			q.CriteriaFalse = mustValue(whenFalse, "noul criteria_false")
		}
	}
}

// Choice builds a "which of these?" question over named options. Keys are
// what the answer returns; values describe each option. It returns a
// *ValidationError when the option set is outside [MinChoiceOptions,
// MaxChoiceOptions] or a key is empty.
func Choice(instructions any, options map[string]any, opts ...ChoiceOption) (*loomv1.DecisionQuestion, error) {
	q := &loomv1.ChoiceQuestion{Options: make(map[string]*structpb.Value, len(options)+1)}
	for k, v := range options {
		if k == "" {
			return nil, validationErr("questions.choice.options", "empty option key")
		}
		val, err := ToValue(v)
		if err != nil {
			return nil, validationErr("questions.choice.options."+k, "%v", err)
		}
		q.Options[k] = val
	}
	for _, o := range opts {
		if err := o(q); err != nil {
			return nil, err
		}
	}
	if n := len(q.Options); n < MinChoiceOptions || n > MaxChoiceOptions {
		return nil, validationErr("questions.choice.options", "%d options; need %d..%d", n, MinChoiceOptions, MaxChoiceOptions)
	}
	return &loomv1.DecisionQuestion{
		Instructions: mustValue(instructions, "choice instructions"),
		Kind:         &loomv1.DecisionQuestion_Choice{Choice: q},
	}, nil
}

// ChoiceOption configures a Choice question.
type ChoiceOption func(*loomv1.ChoiceQuestion) error

// WithNoneOption adds the NoneOption key with the given description. Use it
// on every Choice where the input may match nothing, so the model has an
// honest place for its probability mass.
func WithNoneOption(description any) ChoiceOption {
	return func(q *loomv1.ChoiceQuestion) error {
		if _, exists := q.Options[NoneOption]; exists {
			return validationErr("questions.choice.options", "option %q already present", NoneOption)
		}
		val, err := ToValue(description)
		if err != nil {
			return validationErr("questions.choice.options."+NoneOption, "%v", err)
		}
		q.Options[NoneOption] = val
		return nil
	}
}

// Score builds a "where on this ordered scale?" question. Levels are ordered
// lowest first; the answer's score is a probability-weighted level index. It
// returns a *ValidationError when the level count is outside
// [MinScoreLevels, MaxScoreLevels].
func Score(instructions any, levels ...any) (*loomv1.DecisionQuestion, error) {
	if n := len(levels); n < MinScoreLevels || n > MaxScoreLevels {
		return nil, validationErr("questions.score.levels", "%d levels; need %d..%d", n, MinScoreLevels, MaxScoreLevels)
	}
	q := &loomv1.ScoreQuestion{Levels: make([]*structpb.Value, 0, len(levels))}
	for i, l := range levels {
		val, err := ToValue(l)
		if err != nil {
			return nil, validationErr(fmt.Sprintf("questions.score.levels[%d]", i), "%v", err)
		}
		q.Levels = append(q.Levels, val)
	}
	return &loomv1.DecisionQuestion{
		Instructions: mustValue(instructions, "score instructions"),
		Kind:         &loomv1.DecisionQuestion_Score{Score: q},
	}, nil
}

// NewRequest assembles and validates a request. Site names the call site
// ("recall.rerank"); it selects the Router band and labels traces. State is
// the filtered material the questions are judged against, never a whole
// transcript or a tool payload.
func NewRequest(site string, state any, questions map[string]*loomv1.DecisionQuestion) (*loomv1.DecisionRequest, error) {
	stateVal, err := ToValue(state)
	if err != nil {
		return nil, validationErr("state", "%v", err)
	}
	req := &loomv1.DecisionRequest{
		State:     stateVal,
		Questions: questions,
		Site:      site,
	}
	if err := Validate(req); err != nil {
		return nil, err
	}
	return req, nil
}

// TokenCounter counts tokens in text. The default estimator is deliberately
// conservative (over-counts) so a request that passes here is not rejected
// by a vendor's exact count.
type TokenCounter interface {
	CountTokens(text string) int
}

// EstimateTokens is the default TokenCounter: one token per three bytes.
// English prose runs about four bytes per token, JSON with short keys closer
// to three, so this over-counts prose and is roughly exact for JSON.
type EstimateTokens struct{}

// CountTokens implements TokenCounter.
func (EstimateTokens) CountTokens(text string) int { return (len(text) + 2) / 3 }

// Validate checks a request's shape and size using the default token
// estimator. See ValidateWith.
func Validate(req *loomv1.DecisionRequest) error { return ValidateWith(req, EstimateTokens{}) }

// ValidateWith checks that the request has state, at least one question, a
// kind on every question, option and level counts within limits, and that
// its size is within MaxRequestTokens and MaxStateTokens as measured by
// counter. Every failure is a *ValidationError.
func ValidateWith(req *loomv1.DecisionRequest, counter TokenCounter) error {
	if req == nil {
		return validationErr("request", "nil")
	}
	if req.State == nil {
		return validationErr("state", "missing")
	}
	if len(req.Questions) == 0 {
		return validationErr("questions", "at least one question required")
	}
	if counter == nil {
		counter = EstimateTokens{}
	}
	stateTokens := counter.CountTokens(valueText(req.State))
	totalTokens := stateTokens
	longestQuestion := 0

	// Deterministic iteration so the first error reported is stable.
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		q := req.Questions[id]
		field := "questions." + id
		if id == "" {
			return validationErr("questions", "empty question id")
		}
		if q == nil {
			return validationErr(field, "nil question")
		}
		if q.Instructions == nil {
			return validationErr(field+".instructions", "missing")
		}
		switch k := q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			if k.Noul == nil {
				return validationErr(field+".noul", "nil")
			}
		case *loomv1.DecisionQuestion_Choice:
			if k.Choice == nil {
				return validationErr(field+".choice", "nil")
			}
			n := len(k.Choice.Options)
			if n < MinChoiceOptions || n > MaxChoiceOptions {
				return validationErr(field+".choice.options", "%d options; need %d..%d", n, MinChoiceOptions, MaxChoiceOptions)
			}
			for key := range k.Choice.Options {
				if key == "" {
					return validationErr(field+".choice.options", "empty option key")
				}
			}
		case *loomv1.DecisionQuestion_Score:
			if k.Score == nil {
				return validationErr(field+".score", "nil")
			}
			n := len(k.Score.Levels)
			if n < MinScoreLevels || n > MaxScoreLevels {
				return validationErr(field+".score.levels", "%d levels; need %d..%d", n, MinScoreLevels, MaxScoreLevels)
			}
		default:
			return validationErr(field+".kind", "missing: one of noul, choice, score")
		}
		qt := counter.CountTokens(questionText(q))
		totalTokens += qt
		if qt > longestQuestion {
			longestQuestion = qt
		}
	}

	if totalTokens > MaxRequestTokens {
		return &ValidationError{
			Field: "request",
			Msg:   fmt.Sprintf("%d tokens across state and questions; limit %d", totalTokens, MaxRequestTokens),
			Cause: ErrStateTooLarge,
		}
	}
	if stateTokens+longestQuestion > MaxStateTokens {
		return &ValidationError{
			Field: "state",
			Msg:   fmt.Sprintf("%d tokens for state plus longest question; limit %d", stateTokens+longestQuestion, MaxStateTokens),
			Cause: ErrStateTooLarge,
		}
	}
	return nil
}

// valueText renders a Value as compact JSON for size estimation and prompt
// rendering. A string Value renders as its raw text, not a quoted literal.
func valueText(v *structpb.Value) string {
	if v == nil {
		return ""
	}
	if s, ok := v.Kind.(*structpb.Value_StringValue); ok {
		return s.StringValue
	}
	b, err := protojson.Marshal(v)
	if err != nil {
		return fmt.Sprint(v.AsInterface())
	}
	return string(b)
}

// questionText renders every text-bearing part of a question, for size
// estimation.
func questionText(q *loomv1.DecisionQuestion) string {
	if q == nil {
		return ""
	}
	b, err := protojson.Marshal(q)
	if err != nil {
		return ""
	}
	return string(b)
}
