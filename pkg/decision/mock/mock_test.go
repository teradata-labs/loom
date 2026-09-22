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

package mock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

func fullRequest(t *testing.T) *loomv1.DecisionRequest {
	t.Helper()
	choice, err := decision.Choice("pick", map[string]any{"a": "A", "b": "B"})
	require.NoError(t, err)
	score, err := decision.Score("rate", "low", "high")
	require.NoError(t, err)
	req, err := decision.NewRequest("mock.test", map[string]any{"k": "v"}, map[string]*loomv1.DecisionQuestion{
		"n": decision.Noul("?"),
		"c": choice,
		"s": score,
	})
	require.NoError(t, err)
	return req
}

func TestMockAnswersAllKinds(t *testing.T) {
	t.Parallel()
	m := New().
		AnswerNoul("n", 0.7).
		AnswerChoice("c", map[string]float64{"a": 3, "b": 1}).
		AnswerScore("s", map[string]float64{"0": 0.25, "1": 0.75}).
		SetUsage(12, 0, 0.0005).
		SetModel("mock-2")

	resp, err := m.Decide(context.Background(), fullRequest(t))
	require.NoError(t, err)
	assert.Equal(t, "mock-2", resp.Model)
	assert.Equal(t, int64(12), resp.Usage.InputTokens)
	assert.InDelta(t, 0.0005, resp.Usage.CostUsd, 1e-12)

	n, err := decision.NoulOf(resp, "n")
	require.NoError(t, err)
	assert.InDelta(t, 0.7, n.Probability, 1e-9)
	c, err := decision.ChoiceOf(resp, "c")
	require.NoError(t, err)
	assert.Equal(t, "a", c.Choice)
	assert.InDelta(t, 0.75, c.Probabilities["a"], 1e-9, "distribution normalized")
	s, err := decision.ScoreOf(resp, "s")
	require.NoError(t, err)
	assert.InDelta(t, 0.75, s.Score, 1e-9)
	assert.Equal(t, map[string]string{"0": "low", "1": "high"}, s.Legend, "legend filled from the question")

	require.Equal(t, 1, m.CallCount())
	calls := m.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "mock.test", calls[0].Site)
	calls[0].Site = "mutated"
	assert.Equal(t, "mock.test", m.Calls()[0].Site, "Calls returns copies")
}

func TestMockMissingAnswerFailsLoudly(t *testing.T) {
	t.Parallel()
	m := New().AnswerNoul("n", 0.5) // c and s not scripted
	_, err := m.Decide(context.Background(), fullRequest(t))
	assert.True(t, errors.Is(err, decision.ErrMalformedAnswer), "got %v", err)
	assert.Equal(t, 1, m.CallCount(), "the call is still recorded")
}

func TestMockErrorAndValidation(t *testing.T) {
	t.Parallel()
	m := New().AnswerNoul("n", 0.5).SetError(decision.ErrOverloaded)
	req, err := decision.NewRequest("x", "s", map[string]*loomv1.DecisionQuestion{"n": decision.Noul("?")})
	require.NoError(t, err)
	_, err = m.Decide(context.Background(), req)
	assert.True(t, errors.Is(err, decision.ErrOverloaded))

	_, err = m.Decide(context.Background(), &loomv1.DecisionRequest{})
	assert.True(t, errors.Is(err, decision.ErrValidation))
	assert.Equal(t, 1, m.CallCount(), "invalid requests are not recorded")

	m.Reset()
	assert.Equal(t, 0, m.CallCount())
	_, err = m.Decide(context.Background(), req)
	assert.True(t, errors.Is(err, decision.ErrMalformedAnswer), "reset cleared scripted answers and the error")
}

func TestMockLatencyHonoursContext(t *testing.T) {
	t.Parallel()
	m := New().AnswerNoul("n", 0.5).SetLatency(time.Second)
	req, err := decision.NewRequest("x", "s", map[string]*loomv1.DecisionQuestion{"n": decision.Noul("?")})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = m.Decide(ctx, req)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestMockBadScriptPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { New().AnswerChoice("c", map[string]float64{}) })
	assert.Panics(t, func() { New().AnswerScore("s", map[string]float64{"x": 1}) })
}

func TestMockConcurrent(t *testing.T) {
	t.Parallel()
	m := New().AnswerNoul("n", 0.5)
	req, err := decision.NewRequest("x", "s", map[string]*loomv1.DecisionQuestion{"n": decision.Noul("?")})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Decide(context.Background(), req)
			assert.NoError(t, err)
			_ = m.Calls()
			m.SetModel("m")
		}()
	}
	wg.Wait()
	assert.Equal(t, 32, m.CallCount())
}
