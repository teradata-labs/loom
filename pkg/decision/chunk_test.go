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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

// fanOutRequest builds a rerank-shaped request: n candidates in state under
// "candidates", one Noul per candidate, fan_out_key set.
func fanOutRequest(t *testing.T, n int) *loomv1.DecisionRequest {
	t.Helper()
	items := make([]any, 0, n)
	questions := make(map[string]*loomv1.DecisionQuestion, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		items = append(items, map[string]any{"id": id, "text": fmt.Sprintf("candidate %d", i)})
		questions[id] = decision.Noul("relevant?")
	}
	req, err := decision.NewRequest("test.rerank", map[string]any{"query": "q", "candidates": items}, questions)
	require.NoError(t, err)
	req.FanOutKey = "candidates"
	return req
}

func scriptAll(m *mock.Decider, n int) {
	for i := 0; i < n; i++ {
		m.AnswerNoul(fmt.Sprintf("c%d", i), 0.5+float64(i%5)/10)
	}
}

// candidatesIn counts the fan-out items a request carries.
func candidatesIn(req *loomv1.DecisionRequest) int {
	return len(req.GetState().GetStructValue().Fields["candidates"].GetListValue().Values)
}

type hinted struct {
	*mock.Decider
	max int
}

func (h hinted) MaxQuestionsPerRequest() int { return h.max }

func TestChunkedPreSplitsAndCarvesState(t *testing.T) {
	m := mock.New().SetUsage(100, 0, 0.001)
	scriptAll(m, 40)
	d := decision.Chunk(m, decision.WithChunkSize(16))
	resp, err := d.Decide(context.Background(), fanOutRequest(t, 40))
	require.NoError(t, err)
	assert.Len(t, resp.Answers, 40, "every question answered after the merge")
	assert.Equal(t, "mock-1", resp.Model)
	assert.Equal(t, int64(300), resp.Usage.InputTokens, "usage summed over 3 chunks")
	assert.InDelta(t, 0.003, resp.Usage.CostUsd, 1e-9)

	calls := m.Calls()
	require.Len(t, calls, 3, "40 questions in chunks of 16, 16, 8")
	total := 0
	for _, c := range calls {
		assert.LessOrEqual(t, len(c.Questions), 16)
		assert.Equal(t, len(c.Questions), candidatesIn(c), "each chunk carries only its own candidates")
		assert.Equal(t, "q", c.GetState().GetStructValue().Fields["query"].GetStringValue(), "shared state is kept")
		total += len(c.Questions)
	}
	assert.Equal(t, 40, total)
}

func TestChunkedUsesDeciderHint(t *testing.T) {
	m := mock.New()
	scriptAll(m, 20)
	d := decision.Chunk(hinted{m, 10})
	assert.Equal(t, 10, d.(*decision.Chunked).Size())
	_, err := d.Decide(context.Background(), fanOutRequest(t, 20))
	require.NoError(t, err)
	assert.Equal(t, 2, m.CallCount())

	// An explicit size wins over the hint.
	m2 := mock.New()
	scriptAll(m2, 20)
	_, err = decision.Chunk(hinted{m2, 10}, decision.WithChunkSize(20)).Decide(context.Background(), fanOutRequest(t, 20))
	require.NoError(t, err)
	assert.Equal(t, 1, m2.CallCount())
}

func TestChunkedSmallRequestPassesThroughUntouched(t *testing.T) {
	m := mock.New()
	scriptAll(m, 5)
	d := decision.Chunk(m, decision.WithChunkSize(16))
	req := fanOutRequest(t, 5)
	_, err := d.Decide(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, m.CallCount())
	assert.Equal(t, 5, candidatesIn(m.Calls()[0]))
}

// The provider's real limit is unknown: a decider that fails anything above
// 12 questions with an overload error is found by bisection, and every
// question is still answered.
func TestChunkedBisectsOnOverload(t *testing.T) {
	m := mock.New()
	scriptAll(m, 40)
	var over atomic.Int32
	m.SetHook(func(req *loomv1.DecisionRequest) error {
		if len(req.Questions) > 12 {
			over.Add(1)
			return fmt.Errorf("%w: HTTP 503", decision.ErrOverloaded)
		}
		return nil
	})
	d := decision.Chunk(m, decision.WithChunkSize(32))
	resp, err := d.Decide(context.Background(), fanOutRequest(t, 40))
	require.NoError(t, err)
	assert.Len(t, resp.Answers, 40)
	assert.Greater(t, int(over.Load()), 0, "the oversized chunks were tried first")
	for _, c := range m.Calls() {
		if len(c.Questions) <= 12 {
			assert.Equal(t, len(c.Questions), candidatesIn(c))
		}
	}
}

func TestChunkedDoesNotBisectPermanentErrors(t *testing.T) {
	for _, perm := range []error{decision.ErrUnauthorized, decision.ErrValidation, decision.ErrMalformedAnswer, &decision.ValidationError{Field: "x", Msg: "bad"}} {
		m := mock.New()
		scriptAll(m, 8)
		m.SetHook(func(*loomv1.DecisionRequest) error { return perm })
		_, err := decision.Chunk(m, decision.WithChunkSize(4)).Decide(context.Background(), fanOutRequest(t, 8))
		require.Error(t, err)
		assert.Equal(t, 2, m.CallCount(), "two chunks tried once each, no bisection for %v", perm)
	}
}

func TestChunkedSingleQuestionFailureSurfaces(t *testing.T) {
	m := mock.New()
	scriptAll(m, 8)
	m.SetHook(func(req *loomv1.DecisionRequest) error {
		if _, ok := req.Questions["c3"]; ok {
			return decision.ErrOverloaded
		}
		return nil
	})
	_, err := decision.Chunk(m, decision.WithChunkSize(4)).Decide(context.Background(), fanOutRequest(t, 8))
	require.ErrorIs(t, err, decision.ErrOverloaded, "a question that fails alone fails the request")
}

func TestChunkedWithoutBisectReturnsTheError(t *testing.T) {
	m := mock.New()
	scriptAll(m, 8)
	m.SetHook(func(*loomv1.DecisionRequest) error { return decision.ErrOverloaded })
	_, err := decision.Chunk(m, decision.WithChunkSize(4), decision.WithoutBisect()).Decide(context.Background(), fanOutRequest(t, 8))
	require.ErrorIs(t, err, decision.ErrOverloaded)
	assert.Equal(t, 2, m.CallCount())
}

func TestChunkedHonoursConcurrencyBound(t *testing.T) {
	m := mock.New()
	scriptAll(m, 64)
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	m.SetHook(func(*loomv1.DecisionRequest) error {
		n := inFlight.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		defer inFlight.Add(-1)
		return nil
	})
	d := decision.Chunk(m, decision.WithChunkSize(8), decision.WithChunkConcurrency(2))
	resp, err := d.Decide(context.Background(), fanOutRequest(t, 64))
	require.NoError(t, err)
	assert.Len(t, resp.Answers, 64)
	assert.Equal(t, 8, m.CallCount())
	assert.LessOrEqual(t, int(peak.Load()), 2)
}

func TestChunkedStopsOnCancelledContext(t *testing.T) {
	m := mock.New()
	scriptAll(m, 8)
	ctx, cancel := context.WithCancel(context.Background())
	m.SetHook(func(*loomv1.DecisionRequest) error {
		cancel()
		return decision.ErrOverloaded
	})
	_, err := decision.Chunk(m, decision.WithChunkSize(8)).Decide(ctx, fanOutRequest(t, 8))
	require.Error(t, err)
	assert.True(t, errors.Is(err, decision.ErrOverloaded) || errors.Is(err, context.Canceled))
	assert.Equal(t, 1, m.CallCount(), "no bisection once the caller's context is gone")
}

func TestSubRequestKeepsItemsWithoutIDAndSharedState(t *testing.T) {
	req, err := decision.NewRequest("s", map[string]any{
		"query":      "q",
		"candidates": []any{map[string]any{"id": "a", "t": 1}, map[string]any{"note": "context"}, map[string]any{"id": "b", "t": 2}},
	}, map[string]*loomv1.DecisionQuestion{"a": decision.Noul("?"), "b": decision.Noul("?")})
	require.NoError(t, err)
	req.FanOutKey = "candidates"
	sub := decision.SubRequest(req, []string{"b"})
	require.Len(t, sub.Questions, 1)
	items := sub.GetState().GetStructValue().Fields["candidates"].GetListValue().Values
	require.Len(t, items, 2, "the item without an id is context and travels with every chunk")
	assert.Equal(t, "context", items[0].GetStructValue().Fields["note"].GetStringValue())
	assert.Equal(t, "b", items[1].GetStructValue().Fields["id"].GetStringValue())
	// The original is untouched.
	assert.Len(t, req.GetState().GetStructValue().Fields["candidates"].GetListValue().Values, 3)
	assert.Len(t, req.Questions, 2)

	// No fan-out key: state shared verbatim.
	req.FanOutKey = ""
	sub = decision.SubRequest(req, []string{"a"})
	assert.Len(t, sub.GetState().GetStructValue().Fields["candidates"].GetListValue().Values, 3)
}

func TestChunkNilAndNoSize(t *testing.T) {
	assert.Nil(t, decision.Chunk(nil))
	m := mock.New()
	scriptAll(m, 3)
	d := decision.Chunk(m)
	assert.Equal(t, 0, d.(*decision.Chunked).Size(), "no size and no hint: no pre-split")
	assert.Equal(t, "mock", d.Name())
	assert.Equal(t, "mock-1", d.Model())
	resp, err := d.Decide(context.Background(), fanOutRequest(t, 3))
	require.NoError(t, err)
	assert.Len(t, resp.Answers, 3)
	assert.Equal(t, 1, m.CallCount())
}

func TestChunkedWaves(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		concurrency int
		questions   int
		want        int
	}{
		{"fits in one chunk", 16, 4, 16, 1},
		{"four chunks, four at a time", 16, 4, 64, 1},
		{"five chunks, four at a time", 16, 4, 65, 2},
		{"four chunks, two at a time", 16, 2, 64, 2},
		{"thirteen chunks, four at a time", 16, 4, 200, 4},
		{"no chunking configured", 0, 4, 200, 1},
		{"serial chunks", 8, 1, 40, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := decision.Chunk(mock.New(), decision.WithChunkSize(tt.size), decision.WithChunkConcurrency(tt.concurrency))
			assert.Equal(t, tt.want, d.(*decision.Chunked).Waves(tt.questions))
			// The package function finds the chunker through wrappers.
			assert.Equal(t, tt.want, decision.Waves(decision.NewInstrumented(d, nil), tt.questions))
		})
	}
	assert.Equal(t, 1, decision.Waves(mock.New(), 500), "a decider that does not chunk takes one round")
	assert.Equal(t, 1, decision.Waves(nil, 10))
}

// A live caller's deadline has to cover the rounds its request will take, or
// the biggest requests always time out; the cap keeps a pathological one
// from outlasting the fallback it was meant to skip.
func TestBudgetScalesWithWavesAndCaps(t *testing.T) {
	d := decision.Chunk(mock.New(), decision.WithChunkSize(16), decision.WithChunkConcurrency(4))
	perWave, max := 4*time.Second, 12*time.Second
	assert.Equal(t, 4*time.Second, decision.Budget(d, 64, perWave, max), "four chunks go out together: one round")
	assert.Equal(t, 8*time.Second, decision.Budget(d, 65, perWave, max))
	assert.Equal(t, 12*time.Second, decision.Budget(d, 400, perWave, max), "capped")
	assert.Equal(t, 4*time.Second, decision.Budget(mock.New(), 400, perWave, max), "unchunked stays at one round")
	assert.Equal(t, max, decision.Budget(d, 64, 0, max), "no per-wave allowance falls back to the cap")
	// 400 questions = 25 chunks of 16, four at a time: seven rounds.
	assert.Equal(t, 28*time.Second, decision.Budget(d, 400, perWave, 0), "no cap means no cap")
}
