// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

package evals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestExportToHawk_Success(t *testing.T) {
	// Create mock Hawk server
	var receivedResult *loomv1.EvalResult
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/evals", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "loom-eval/1.0", r.Header.Get("User-Agent"))

		// Parse body
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedResult)

		// Respond success
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()

	// Create eval result
	result := &loomv1.EvalResult{
		SuiteName: "test-suite",
		AgentId:   "test-agent",
		RunAt:     timestamppb.Now(),
		Overall: &loomv1.EvalMetrics{
			TotalTests:     3,
			PassedTests:    2,
			FailedTests:    1,
			Accuracy:       0.666,
			TotalCostUsd:   0.006,
			TotalLatencyMs: 150,
		},
		TestResults: []*loomv1.TestCaseResult{
			{TestName: "test1", Passed: true, CostUsd: 0.002, LatencyMs: 50},
			{TestName: "test2", Passed: true, CostUsd: 0.002, LatencyMs: 50},
			{TestName: "test3", Passed: false, CostUsd: 0.002, LatencyMs: 50, FailureReason: "output mismatch"},
		},
		Passed: false,
	}

	// Export
	config := &HawkExportConfig{
		Endpoint: server.URL,
		APIKey:   "test-key",
		Timeout:  5 * time.Second,
	}

	ctx := context.Background()
	err := ExportToHawk(ctx, result, config)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, receivedResult)
	assert.Equal(t, "test-suite", receivedResult.SuiteName)
	assert.Equal(t, "test-agent", receivedResult.AgentId)
	assert.Equal(t, int32(3), receivedResult.Overall.TotalTests)
}

func TestExportToHawk_DefaultConfig(t *testing.T) {
	// Create mock server
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, "", r.Header.Get("Authorization"), "Should not have auth header without API key")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	// Set env var for endpoint
	t.Setenv("HAWK_ENDPOINT", server.URL)

	// Export with nil config (uses defaults)
	result := &loomv1.EvalResult{
		SuiteName: "test",
		AgentId:   "agent",
		RunAt:     timestamppb.Now(),
		Overall:   &loomv1.EvalMetrics{TotalTests: 1},
	}

	ctx := context.Background()
	err := ExportToHawk(ctx, result, nil)

	// Verify
	require.NoError(t, err)
	assert.True(t, called, "Should have called Hawk server")
}

func TestExportToHawk_ServerError(t *testing.T) {
	// Create mock server that returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal server error"))
	}))
	defer server.Close()

	// Export
	result := &loomv1.EvalResult{
		SuiteName: "test",
		AgentId:   "agent",
		RunAt:     timestamppb.Now(),
		Overall:   &loomv1.EvalMetrics{},
	}

	config := &HawkExportConfig{
		Endpoint: server.URL,
		Timeout:  5 * time.Second,
	}

	ctx := context.Background()
	err := ExportToHawk(ctx, result, config)

	// Verify error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hawk export failed with status 500")
}

func TestExportToHawk_Timeout(t *testing.T) {
	// Create server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Export with short timeout
	result := &loomv1.EvalResult{
		SuiteName: "test",
		AgentId:   "agent",
		RunAt:     timestamppb.Now(),
		Overall:   &loomv1.EvalMetrics{},
	}

	config := &HawkExportConfig{
		Endpoint: server.URL,
		Timeout:  10 * time.Millisecond, // Very short timeout
	}

	ctx := context.Background()
	err := ExportToHawk(ctx, result, config)

	// Verify timeout error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to export to hawk")
}

func TestExportBatch_Success(t *testing.T) {
	// Create mock server
	var receivedResults []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/evals/batch", r.URL.Path)

		// Parse batch payload
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)

		results, ok := payload["results"].([]interface{})
		assert.True(t, ok)
		for _, res := range results {
			receivedResults = append(receivedResults, res.(map[string]interface{}))
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	// Create multiple results
	results := []*loomv1.EvalResult{
		{
			SuiteName: "suite1",
			AgentId:   "agent1",
			RunAt:     timestamppb.Now(),
			Overall:   &loomv1.EvalMetrics{TotalTests: 1, PassedTests: 1},
		},
		{
			SuiteName: "suite2",
			AgentId:   "agent2",
			RunAt:     timestamppb.Now(),
			Overall:   &loomv1.EvalMetrics{TotalTests: 2, PassedTests: 1},
		},
	}

	// Export batch
	config := &HawkExportConfig{
		Endpoint: server.URL,
		Timeout:  5 * time.Second,
	}

	ctx := context.Background()
	err := ExportBatch(ctx, results, config)

	// Verify
	require.NoError(t, err)
	assert.Len(t, receivedResults, 2)
}

func TestExportBatch_EmptyResults(t *testing.T) {
	// Export empty batch should be no-op
	ctx := context.Background()
	err := ExportBatch(ctx, []*loomv1.EvalResult{}, nil)

	// Should succeed without making any requests
	require.NoError(t, err)
}

func TestExportToHawk_ContextCancellation(t *testing.T) {
	// Create server that delays
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create context that cancels immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Export
	result := &loomv1.EvalResult{
		SuiteName: "test",
		AgentId:   "agent",
		RunAt:     timestamppb.Now(),
		Overall:   &loomv1.EvalMetrics{},
	}

	config := &HawkExportConfig{
		Endpoint: server.URL,
		Timeout:  5 * time.Second,
	}

	err := ExportToHawk(ctx, result, config)

	// Should error due to context cancellation
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}
