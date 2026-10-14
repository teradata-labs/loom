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

//go:build fts5

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func okResult(id string) EntryResult {
	return EntryResult{QuestionID: id, Hypothesis: "h"}
}

func failedResult(id string, code codes.Code) EntryResult {
	return EntryResult{QuestionID: id, Error: "ask question: rpc error: code = " + code.String(), grpcCode: code}
}

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name          string
		results       []EntryResult
		runErr        error
		wantNil       bool
		wantTransient bool
		wantFailed    int
	}{
		{
			name:    "all entries succeeded",
			results: []EntryResult{okResult("a"), okResult("b")},
			wantNil: true,
		},
		{
			name:    "no results at all",
			wantNil: true,
		},
		{
			name:          "every failure is Unavailable",
			results:       []EntryResult{okResult("a"), failedResult("b", codes.Unavailable), failedResult("c", codes.Unavailable)},
			wantTransient: true,
			wantFailed:    2,
		},
		{
			name:          "mixed retryable statuses",
			results:       []EntryResult{failedResult("a", codes.DeadlineExceeded), failedResult("b", codes.ResourceExhausted), failedResult("c", codes.Unavailable)},
			wantTransient: true,
			wantFailed:    3,
		},
		{
			name:    "one deterministic failure among transient ones is charged",
			results: []EntryResult{failedResult("a", codes.Unavailable), failedResult("b", codes.Internal)},
			wantNil: true,
		},
		{
			name:    "Internal only (provider failure after looms' own retries)",
			results: []EntryResult{failedResult("a", codes.Internal)},
			wantNil: true,
		},
		{
			name:    "InvalidArgument is deterministic",
			results: []EntryResult{failedResult("a", codes.InvalidArgument)},
			wantNil: true,
		},
		{
			name:    "a non-RPC failure (no status) is deterministic",
			results: []EntryResult{{QuestionID: "a", Error: "parse question date: bad"}},
			wantNil: true,
		},
		{
			name:    "a cancelled entry from a user interrupt is not transient",
			results: []EntryResult{failedResult("a", codes.Canceled)},
			runErr:  context.Canceled,
			wantNil: true,
		},
		{
			name:          "user interrupt with only transient failures still asks for a retry",
			results:       []EntryResult{failedResult("a", codes.Unavailable)},
			runErr:        context.Canceled,
			wantTransient: true,
			wantFailed:    1,
		},
		{
			name:    "aborted run is an error even when entries look transient",
			results: []EntryResult{failedResult("a", codes.Unavailable)},
			runErr:  errors.New("server rejects occurred_at"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyOutcome(tt.results, tt.runErr)
			if tt.wantNil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var transient *TransientFailureError
			if !tt.wantTransient {
				assert.False(t, errors.As(err, &transient), "abort must not be classified transient: %v", err)
				assert.Contains(t, err.Error(), "benchmark run aborted")
				return
			}
			require.True(t, errors.As(err, &transient), "want *TransientFailureError, got %T: %v", err, err)
			assert.Equal(t, tt.wantFailed, transient.Failed)
			assert.Equal(t, len(tt.results), transient.Total)
		})
	}
}

func TestTransientFailureErrorMessage(t *testing.T) {
	err := &TransientFailureError{
		Failed: 3, Total: 9,
		Codes: map[codes.Code]int{codes.Unavailable: 2, codes.DeadlineExceeded: 1},
	}
	assert.Equal(t,
		"3 of 9 entries failed, all with retryable gRPC statuses (DeadlineExceeded=1, Unavailable=2); retry the run",
		err.Error())
}

func TestExitCodeFor(t *testing.T) {
	transient := &TransientFailureError{Failed: 1, Total: 1, Codes: map[codes.Code]int{codes.Unavailable: 1}}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, ExitOK},
		{"generic error", errors.New("boom"), ExitError},
		{"aborted run", fmt.Errorf("benchmark run aborted: %w", errors.New("x")), ExitError},
		{"transient", transient, ExitTransient},
		{"wrapped transient", fmt.Errorf("run: %w", transient), ExitTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exitCodeFor(tt.err))
		})
	}
	assert.NotEqual(t, ExitError, ExitTransient, "the transient status must be distinct")
	assert.NotEqual(t, ExitOK, ExitTransient)
}

// TestRunRecordsRPCStatusOnEveryFailureSite pins that each RPC the runner can
// fail on records its status, so a server restart during CreateSession or
// CreateAgentFromConfig is classified as transient just like a Weave failure.
func TestRunRecordsRPCStatusOnEveryFailureSite(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "connection refused")
	tests := []struct {
		name string
		mode RunMode
		fake *fakeLoomClient
	}{
		{"weave (ingest)", ModeIngest, &fakeLoomClient{weaveErr: unavailable}},
		{"weave (multi-session)", ModeMultiSession, &fakeLoomClient{weaveErr: unavailable}},
		{"weave (context-stuffing)", ModeContextStuffing, &fakeLoomClient{weaveErr: unavailable}},
		{"create session (ingest)", ModeIngest, &fakeLoomClient{createSessionErr: unavailable}},
		{"create session (multi-session)", ModeMultiSession, &fakeLoomClient{createSessionErr: unavailable}},
		{"create session (context-stuffing)", ModeContextStuffing, &fakeLoomClient{createSessionErr: unavailable}},
		{"create temp agent", ModeIngest, &fakeLoomClient{createAgentErr: unavailable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{
				config:    RunConfig{Mode: tt.mode, Isolate: true, Concurrency: 2},
				logger:    zap.NewNop(),
				client:    tt.fake,
				baseAgent: &loomv1.AgentConfig{Name: "lme-base"},
			}
			entries := []Entry{testEntry("q1"), testEntry("q2")}
			resultCh := make(chan EntryResult, len(entries))
			runErr := r.Run(context.Background(), entries, resultCh)
			close(resultCh)
			require.NoError(t, runErr)

			var results []EntryResult
			for res := range resultCh {
				require.NotEmpty(t, res.Error)
				assert.Equal(t, codes.Unavailable, res.grpcCode)
				results = append(results, res)
			}
			require.Len(t, results, 2)
			assert.Equal(t, ExitTransient, exitCodeFor(classifyOutcome(results, runErr)))
		})
	}

	t.Run("a deterministic Weave failure exits OK for the caller to judge", func(t *testing.T) {
		r := &Runner{
			config: RunConfig{Mode: ModeIngest, Concurrency: 1},
			logger: zap.NewNop(),
			client: &fakeLoomClient{weaveErr: status.Error(codes.Internal, "agent execution failed: content filter")},
		}
		resultCh := make(chan EntryResult, 1)
		runErr := r.Run(context.Background(), []Entry{testEntry("q1")}, resultCh)
		close(resultCh)
		res := <-resultCh
		assert.Equal(t, codes.Internal, res.grpcCode)
		assert.Equal(t, ExitOK, exitCodeFor(classifyOutcome([]EntryResult{res}, runErr)))
	})
}

// TestRunnerScriptTransientExitCode pins the contract between this binary and
// the AKS slice loop: runner-script.yaml must treat exactly ExitTransient as
// the transient status. If either side changes alone, transient failures
// would be charged to the deterministic budget again.
func TestRunnerScriptTransientExitCode(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "longmemeval", "runner-script.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^\s*HARNESS_RC_TRANSIENT=(\d+)\s*$`).FindSubmatch(data)
	require.NotNil(t, m, "runner-script.yaml must define HARNESS_RC_TRANSIENT=<n>")
	got, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)
	assert.Equal(t, ExitTransient, got)
}
