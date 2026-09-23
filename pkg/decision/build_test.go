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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

type stateStruct struct {
	Tool  string   `json:"tool"`
	Codes []int    `json:"codes"`
	Tags  []string `json:"tags"`
}

func TestToValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      any
		want    any // AsInterface() form
		wantErr bool
	}{
		{name: "nil", in: nil, want: nil},
		{name: "string", in: "hello", want: "hello"},
		{name: "bool", in: true, want: true},
		{name: "int", in: 42, want: float64(42)},
		{name: "float", in: 1.5, want: 1.5},
		{name: "generic map", in: map[string]any{"a": "b"}, want: map[string]any{"a": "b"}},
		{name: "generic slice", in: []any{"a", 1}, want: []any{"a", float64(1)}},
		{name: "typed map via json", in: map[string]string{"k": "v"}, want: map[string]any{"k": "v"}},
		{name: "typed slice via json", in: []string{"x", "y"}, want: []any{"x", "y"}},
		{name: "struct via json", in: stateStruct{Tool: "t", Codes: []int{1}, Tags: []string{"a"}},
			want: map[string]any{"tool": "t", "codes": []any{float64(1)}, "tags": []any{"a"}}},
		{name: "proto message via protojson", in: &loomv1.DecisionUsage{InputTokens: 7},
			want: map[string]any{"inputTokens": "7"}},
		{name: "existing value passthrough", in: structpb.NewStringValue("v"), want: "v"},
		{name: "unrepresentable", in: make(chan int), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ToValue(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.AsInterface())
		})
	}
}

func TestChoiceBuilder(t *testing.T) {
	t.Parallel()
	opts := func(n int) map[string]any {
		m := make(map[string]any, n)
		for i := 0; i < n; i++ {
			m[strings.Repeat("o", i+1)] = "desc"
		}
		return m
	}
	tests := []struct {
		name      string
		options   map[string]any
		opts      []ChoiceOption
		wantErr   bool
		wantField string
		wantN     int
	}{
		{name: "two options", options: opts(2), wantN: 2},
		{name: "max options", options: opts(MaxChoiceOptions), wantN: MaxChoiceOptions},
		{name: "one option rejected", options: opts(1), wantErr: true, wantField: "questions.choice.options"},
		{name: "too many rejected", options: opts(MaxChoiceOptions + 1), wantErr: true, wantField: "questions.choice.options"},
		{name: "empty key rejected", options: map[string]any{"": "x", "a": "y"}, wantErr: true, wantField: "questions.choice.options"},
		{name: "none option added", options: opts(2), opts: []ChoiceOption{WithNoneOption("nothing fits")}, wantN: 3},
		{name: "none option duplicate rejected", options: map[string]any{"a": 1, NoneOption: 2},
			opts: []ChoiceOption{WithNoneOption("dup")}, wantErr: true, wantField: "questions.choice.options"},
		{name: "unrepresentable description", options: map[string]any{"a": make(chan int), "b": 1}, wantErr: true, wantField: "questions.choice.options.a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q, err := Choice("which?", tt.options, tt.opts...)
			if tt.wantErr {
				var ve *ValidationError
				require.ErrorAs(t, err, &ve)
				assert.Equal(t, tt.wantField, ve.Field)
				assert.True(t, errors.Is(err, ErrValidation))
				return
			}
			require.NoError(t, err)
			c := q.GetChoice()
			require.NotNil(t, c)
			assert.Len(t, c.Options, tt.wantN)
			assert.Equal(t, "which?", q.Instructions.GetStringValue())
		})
	}
}

func TestScoreBuilder(t *testing.T) {
	t.Parallel()
	levels := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "level"
		}
		return out
	}
	tests := []struct {
		name    string
		levels  []any
		wantErr bool
	}{
		{name: "two levels", levels: levels(2)},
		{name: "ten levels", levels: levels(MaxScoreLevels)},
		{name: "one level rejected", levels: levels(1), wantErr: true},
		{name: "eleven rejected", levels: levels(MaxScoreLevels + 1), wantErr: true},
		{name: "structured level", levels: []any{map[string]any{"summary": "low"}, map[string]any{"summary": "high"}}},
		{name: "unrepresentable level", levels: []any{"a", make(chan int)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q, err := Score("how severe?", tt.levels...)
			if tt.wantErr {
				assert.True(t, errors.Is(err, ErrValidation), "got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, q.GetScore().Levels, len(tt.levels))
		})
	}
}

func TestNoulBuilder(t *testing.T) {
	t.Parallel()
	q := Noul("is it a failure?", WithCriteria("the call did not do what was asked", nil))
	n := q.GetNoul()
	require.NotNil(t, n)
	assert.Equal(t, "the call did not do what was asked", n.CriteriaTrue.GetStringValue())
	assert.Nil(t, n.CriteriaFalse)

	assert.Panics(t, func() { Noul(make(chan int)) }, "non-JSON instructions are a programming error")
}

func mustChoice(t *testing.T, opts ...ChoiceOption) *loomv1.DecisionQuestion {
	t.Helper()
	q, err := Choice("pick", map[string]any{"a": "A", "b": "B", "c": "C"}, opts...)
	require.NoError(t, err)
	return q
}

func mustScore(t *testing.T) *loomv1.DecisionQuestion {
	t.Helper()
	q, err := Score("rate", "low", "mid", "high")
	require.NoError(t, err)
	return q
}

func TestValidate(t *testing.T) {
	t.Parallel()
	good := func() *loomv1.DecisionRequest {
		return &loomv1.DecisionRequest{
			State: structpb.NewStringValue("state"),
			Questions: map[string]*loomv1.DecisionQuestion{
				"n": Noul("q"),
				"c": mustChoice(t),
				"s": mustScore(t),
			},
			Site: "test",
		}
	}
	tests := []struct {
		name      string
		mutate    func(*loomv1.DecisionRequest) *loomv1.DecisionRequest
		wantField string
		wantIs    error
	}{
		{name: "valid", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest { return r }},
		{name: "nil request", mutate: func(*loomv1.DecisionRequest) *loomv1.DecisionRequest { return nil }, wantField: "request"},
		{name: "missing state", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest { r.State = nil; return r }, wantField: "state"},
		{name: "no questions", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest { r.Questions = nil; return r }, wantField: "questions"},
		{name: "empty id", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest { r.Questions[""] = Noul("x"); return r }, wantField: "questions"},
		{name: "nil question", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest { r.Questions["z"] = nil; return r }, wantField: "questions.z"},
		{name: "missing instructions", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			r.Questions["n"].Instructions = nil
			return r
		}, wantField: "questions.n.instructions"},
		{name: "missing kind", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			r.Questions["k"] = &loomv1.DecisionQuestion{Instructions: structpb.NewStringValue("x")}
			return r
		}, wantField: "questions.k.kind"},
		{name: "choice too few options", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			delete(r.Questions["c"].GetChoice().Options, "a")
			delete(r.Questions["c"].GetChoice().Options, "b")
			return r
		}, wantField: "questions.c.choice.options"},
		{name: "choice empty key", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			r.Questions["c"].GetChoice().Options[""] = structpb.NewStringValue("x")
			return r
		}, wantField: "questions.c.choice.options"},
		{name: "score too few levels", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			r.Questions["s"].GetScore().Levels = r.Questions["s"].GetScore().Levels[:1]
			return r
		}, wantField: "questions.s.score.levels"},
		{name: "state too large", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			r.State = structpb.NewStringValue(strings.Repeat("x", MaxStateTokens*3+3))
			return r
		}, wantField: "state", wantIs: ErrStateTooLarge},
		{name: "request too large", mutate: func(r *loomv1.DecisionRequest) *loomv1.DecisionRequest {
			// State fits under the 32k state bound; three big questions push the 64k total.
			r.State = structpb.NewStringValue(strings.Repeat("x", 20_000*3))
			for _, id := range []string{"a", "b", "c"} {
				r.Questions["big"+id] = Noul(strings.Repeat("y", 16_000*3))
			}
			return r
		}, wantField: "request", wantIs: ErrStateTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.mutate(good()))
			if tt.wantField == "" {
				require.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.ErrorAs(t, err, &ve, "got %v", err)
			assert.Equal(t, tt.wantField, ve.Field)
			assert.True(t, errors.Is(err, ErrValidation))
			if tt.wantIs != nil {
				assert.True(t, errors.Is(err, tt.wantIs), "want errors.Is %v, got %v", tt.wantIs, err)
			}
		})
	}
}

func TestNewRequest(t *testing.T) {
	t.Parallel()
	req, err := NewRequest("recall.rerank", map[string]any{"message": "hi"}, map[string]*loomv1.DecisionQuestion{
		"relevant": Noul("relevant?"),
	})
	require.NoError(t, err)
	assert.Equal(t, "recall.rerank", req.Site)
	assert.Equal(t, "hi", req.State.GetStructValue().Fields["message"].GetStringValue())

	_, err = NewRequest("x", make(chan int), map[string]*loomv1.DecisionQuestion{"q": Noul("q")})
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "state", ve.Field)

	_, err = NewRequest("x", "s", nil)
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "questions", ve.Field)
}

type fixedCounter int

func (f fixedCounter) CountTokens(string) int { return int(f) }

func TestValidateWithCustomCounter(t *testing.T) {
	t.Parallel()
	req := &loomv1.DecisionRequest{
		State:     structpb.NewStringValue("tiny"),
		Questions: map[string]*loomv1.DecisionQuestion{"n": Noul("q")},
	}
	// A counter that reports every text as huge trips the state bound even
	// though the bytes are tiny: the counter is authoritative.
	err := ValidateWith(req, fixedCounter(MaxStateTokens))
	assert.True(t, errors.Is(err, ErrStateTooLarge))
	require.NoError(t, ValidateWith(req, fixedCounter(1)))
	require.NoError(t, ValidateWith(req, nil), "nil counter falls back to the estimator")
}

func TestEstimateTokensIsConservative(t *testing.T) {
	t.Parallel()
	// Roughly three bytes per token over-counts English prose (≈4 bytes/token),
	// so anything that passes here passes a vendor's exact count.
	assert.Equal(t, 0, EstimateTokens{}.CountTokens(""))
	assert.Equal(t, 1, EstimateTokens{}.CountTokens("ab"))
	assert.Equal(t, 100, EstimateTokens{}.CountTokens(strings.Repeat("abc", 100)))
}

func TestToValueSanitizesInvalidUTF8(t *testing.T) {
	t.Parallel()
	// Tool results can carry arbitrary bytes; a decision request must never
	// fail or panic because of them.
	// A run of invalid bytes becomes one replacement character.
	v, err := ToValue("ok\x92\xffend")
	require.NoError(t, err)
	assert.Equal(t, "ok�end", v.GetStringValue())

	v, err = ToValue(map[string]any{"result": "bad\xffbyte", "tags": []string{"\x92"}})
	require.NoError(t, err)
	assert.Equal(t, "bad�byte", v.GetStructValue().Fields["result"].GetStringValue())

	assert.NotPanics(t, func() { Noul("\xff") })
	_, err = Choice("\xff", map[string]any{"a": "\x92", "b": "ok"})
	require.NoError(t, err)
	_, err = Score("s", "\xff", "ok")
	require.NoError(t, err)
}

func FuzzToValueRoundTrip(f *testing.F) {
	f.Add("plain")
	f.Add(`{"a":1}`)
	f.Add("")
	f.Add("\x92")
	f.Fuzz(func(t *testing.T, s string) {
		v, err := ToValue(s)
		if err != nil {
			t.Fatalf("string must always convert: %v", err)
		}
		want := strings.ToValidUTF8(s, "�")
		if got := valueText(v); got != want {
			t.Fatalf("string state must round-trip up to UTF-8 repair: %q != %q", got, want)
		}
	})
}

func FuzzValidateState(f *testing.F) {
	f.Add("x", 1)
	f.Add("", 0)
	f.Add(strings.Repeat("y", 1000), 3)
	f.Add("\xff", 1)
	f.Fuzz(func(t *testing.T, state string, n int) {
		if n < 0 {
			n = -n
		}
		n = n%4 + 1
		qs := make(map[string]*loomv1.DecisionQuestion, n)
		for i := 0; i < n; i++ {
			qs[strings.Repeat("q", i+1)] = Noul(state) // must not panic on any input
		}
		stateVal, err := ToValue(state)
		if err != nil {
			t.Fatalf("state must always convert: %v", err)
		}
		req := &loomv1.DecisionRequest{State: stateVal, Questions: qs}
		err = Validate(req)
		// Validation must never panic, and must reject exactly when the
		// estimator says the request is over a bound.
		counter := EstimateTokens{}
		st := counter.CountTokens(valueText(stateVal))
		total, longest := st, 0
		for _, q := range qs {
			qt := counter.CountTokens(questionText(q))
			total += qt
			if qt > longest {
				longest = qt
			}
		}
		over := st+longest > MaxStateTokens || total > MaxRequestTokens
		if over && !errors.Is(err, ErrStateTooLarge) {
			t.Fatalf("expected ErrStateTooLarge, got %v", err)
		}
		if !over && err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
