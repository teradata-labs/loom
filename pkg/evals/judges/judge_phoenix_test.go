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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// verdictLLM answers every call with one judge verdict.
type verdictLLM struct{}

func (verdictLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: `{"factual_accuracy": 90, "hallucination_score": 10, "query_quality": 80,
		"completeness": 70, "verdict": "PASS", "reasoning": "answers the question", "issues": []}`}, nil
}
func (verdictLLM) Name() string  { return "verdict" }
func (verdictLLM) Model() string { return "judge-model" }

func TestLLMJudge_EvaluateRecordsVerdictAndAnnotatesTrace(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]interface{}
	var paths []string
	phoenixSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer phoenixSrv.Close()
	phoenix, err := observability.NewPhoenixClient(observability.PhoenixClientConfig{BaseURL: phoenixSrv.URL})
	require.NoError(t, err)

	tracer := observability.NewMockTracer()
	judge, err := NewLLMJudge(verdictLLM{}, &loomv1.JudgeConfig{Id: "quality-judge", Name: "Quality"}, tracer, WithPhoenixClient(phoenix))
	require.NoError(t, err)

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	result, err := judge.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a", TraceId: traceID})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, phoenix.Stop(ctx))

	t.Run("verdict on the judge span", func(t *testing.T) {
		span := tracer.GetSpanByName(observability.SpanJudgeEvaluation)
		require.NotNil(t, span)
		assert.Equal(t, result.OverallScore, span.Attributes["judge.score"])
		assert.Equal(t, "PASS", span.Attributes["judge.verdict"])
		assert.Equal(t, "answers the question", span.Attributes["judge.reasoning"])
		assert.Equal(t, 80.0, span.Attributes["judge.dimension_scores.quality"])

		llmSpans := tracer.GetSpansByName(observability.SpanLLMCompletion)
		require.Len(t, llmSpans, 1, "the judge's LLM call is traced")
		assert.Equal(t, span.SpanID, llmSpans[0].ParentID)
		assert.Equal(t, llm.RoleJudge, llmSpans[0].Attributes["llm.role"])
	})

	t.Run("one trace annotation per score", func(t *testing.T) {
		require.Equal(t, []string{"/v1/trace_annotations"}, paths)
		data := bodies[0]["data"].([]interface{})
		got := map[string]float64{}
		for _, d := range data {
			item := d.(map[string]interface{})
			assert.Equal(t, traceID, item["trace_id"])
			assert.Equal(t, "LLM", item["annotator_kind"])
			res := item["result"].(map[string]interface{})
			assert.Equal(t, "PASS", res["label"])
			assert.Equal(t, "answers the question", res["explanation"])
			name := item["name"].(string)
			assert.Equal(t, "quality-judge:"+name[len("loom."):], item["identifier"])
			got[name] = res["score"].(float64)
		}
		assert.Equal(t, map[string]float64{
			"loom.overall": result.OverallScore, "loom.quality": 80, "loom.correctness": 90,
			"loom.completeness": 70, "loom.safety": 90,
		}, got)
	})
}

func TestLLMJudge_NoTraceIDNoAnnotation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ }))
	defer srv.Close()
	phoenix, err := observability.NewPhoenixClient(observability.PhoenixClientConfig{BaseURL: srv.URL})
	require.NoError(t, err)

	judge, err := NewLLMJudge(verdictLLM{}, &loomv1.JudgeConfig{Id: "j", Name: "J"}, nil, WithPhoenixClient(phoenix))
	require.NoError(t, err)
	_, err = judge.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a"})
	require.NoError(t, err)
	require.NoError(t, phoenix.Stop(context.Background()))
	assert.Zero(t, calls, "nothing to annotate without a trace ID")

	_, err = judge.Evaluate(context.Background(), nil)
	assert.Error(t, err, "a nil evaluation context is an error, not a panic")
}
