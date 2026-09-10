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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBuildWeaveRequest(t *testing.T) {
	tests := []struct {
		name       string
		occurredAt time.Time
		wantSet    bool
	}{
		{
			name:       "zero time omits occurred_at",
			occurredAt: time.Time{},
			wantSet:    false,
		},
		{
			name:       "historical date sets occurred_at",
			occurredAt: time.Date(2023, 4, 10, 17, 50, 0, 0, time.UTC),
			wantSet:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildWeaveRequest("sess-1", "query", "agent-1", tt.occurredAt)
			assert.Equal(t, "sess-1", req.SessionId)
			assert.Equal(t, "query", req.Query)
			assert.Equal(t, "agent-1", req.AgentId)

			if !tt.wantSet {
				assert.Nil(t, req.OccurredAt)
				return
			}
			require.NotNil(t, req.OccurredAt)
			assert.True(t, tt.occurredAt.Equal(req.OccurredAt.AsTime()))
		})
	}
}

func TestBuildWeaveRequestFromDatasetDate(t *testing.T) {
	// The exact format the LongMemEval dataset uses for haystack/question dates.
	parsed, err := ParseDate("2023/04/10 (Mon) 17:50")
	require.NoError(t, err)

	req := buildWeaveRequest("sess-1", "query", "agent-1", parsed)
	require.NotNil(t, req.OccurredAt)
	assert.True(t, parsed.Equal(req.OccurredAt.AsTime()))
}

func TestSessionOccurredAt(t *testing.T) {
	sess := SessionWithDate{
		Date:     "2023/04/10 (Mon) 17:50",
		ParsedAt: time.Date(2023, 4, 10, 17, 50, 0, 0, time.UTC),
	}

	enabled := &Runner{config: RunConfig{Mode: ModeIngest, UseOccurredAt: true}}
	assert.True(t, sess.ParsedAt.Equal(enabled.sessionOccurredAt(sess)))

	disabled := &Runner{config: RunConfig{Mode: ModeIngest, UseOccurredAt: false}}
	assert.True(t, disabled.sessionOccurredAt(sess).IsZero())

	// Context-stuffing is the no-memory baseline: never sends occurred_at.
	stuffing := &Runner{config: RunConfig{Mode: ModeContextStuffing, UseOccurredAt: true}}
	assert.True(t, stuffing.sessionOccurredAt(sess).IsZero())
}

func TestOccurredAtEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  RunConfig
		want bool
	}{
		{"ingest with flag", RunConfig{Mode: ModeIngest, UseOccurredAt: true}, true},
		{"multi-session with flag", RunConfig{Mode: ModeMultiSession, UseOccurredAt: true}, true},
		{"context-stuffing with flag", RunConfig{Mode: ModeContextStuffing, UseOccurredAt: true}, false},
		{"ingest without flag", RunConfig{Mode: ModeIngest, UseOccurredAt: false}, false},
		{"context-stuffing without flag", RunConfig{Mode: ModeContextStuffing, UseOccurredAt: false}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{config: tt.cfg}
			assert.Equal(t, tt.want, r.occurredAtEnabled())
		})
	}
}

// A refused override must abort the run, whichever override it is: it
// invalidates the benchmark rather than degrading it, and a run that writes
// error rows and exits zero hands automation a fake result.
func TestOverrideRejection(t *testing.T) {
	tests := []struct {
		name     string
		result   EntryResult
		want     bool
		wantFlag string
	}{
		{
			name: "failed precondition mentioning occurred_at",
			result: EntryResult{
				Error:    "ingest session 0: rpc error: code = FailedPrecondition desc = occurred_at override is disabled on this server",
				grpcCode: codes.FailedPrecondition,
			},
			want:     true,
			wantFlag: "allow_time_override",
		},
		{
			name:     "flag-name substring without a status code",
			result:   EntryResult{Error: "server refused: enable allow_time_override"},
			want:     true,
			wantFlag: "allow_time_override",
		},
		{
			// The MAJOR-001 case: conversation mode against a default server.
			name: "failed precondition mentioning replay_assistant_message",
			result: EntryResult{
				Error:    "replay turn 3: rpc error: code = FailedPrecondition desc = replay_assistant_message override is disabled on this server (set server.allow_assistant_override: true ...)",
				grpcCode: codes.FailedPrecondition,
			},
			want:     true,
			wantFlag: "allow_assistant_override",
		},
		{
			name:     "assistant flag-name substring without a status code",
			result:   EntryResult{Error: "server refused: enable allow_assistant_override"},
			want:     true,
			wantFlag: "allow_assistant_override",
		},
		{
			name: "unrelated failed precondition",
			result: EntryResult{
				Error:    "ask question: agent is busy",
				grpcCode: codes.FailedPrecondition,
			},
			want: false,
		},
		{
			name: "unrelated error",
			result: EntryResult{
				Error:    "ingest session 0: connection reset",
				grpcCode: codes.Unavailable,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, got := overrideRejection(tt.result)
			assert.Equal(t, tt.want, got)
			if tt.want {
				assert.Contains(t, msg, tt.wantFlag,
					"the abort message must name the flag the operator has to change")
			} else {
				assert.Empty(t, msg)
			}
		})
	}
}

// fakeLoomClient stubs the subset of LoomServiceClient the runner touches.
// Calls to any other method panic via the embedded nil interface.
type fakeLoomClient struct {
	loomv1.LoomServiceClient

	mu                 sync.Mutex
	weaveReqs          []*loomv1.WeaveRequest
	weaveErr           error
	deleteAgentCtxErrs []error
}

func (f *fakeLoomClient) CreateSession(_ context.Context, _ *loomv1.CreateSessionRequest, _ ...grpc.CallOption) (*loomv1.Session, error) {
	return &loomv1.Session{Id: "sess-fake"}, nil
}

func (f *fakeLoomClient) DeleteSession(_ context.Context, _ *loomv1.DeleteSessionRequest, _ ...grpc.CallOption) (*loomv1.DeleteSessionResponse, error) {
	return &loomv1.DeleteSessionResponse{}, nil
}

func (f *fakeLoomClient) CreateAgentFromConfig(_ context.Context, _ *loomv1.CreateAgentRequest, _ ...grpc.CallOption) (*loomv1.AgentInfo, error) {
	return &loomv1.AgentInfo{Id: "agent-fake"}, nil
}

func (f *fakeLoomClient) DeleteAgent(ctx context.Context, _ *loomv1.DeleteAgentRequest, _ ...grpc.CallOption) (*loomv1.DeleteAgentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteAgentCtxErrs = append(f.deleteAgentCtxErrs, ctx.Err())
	return &loomv1.DeleteAgentResponse{}, nil
}

func (f *fakeLoomClient) Weave(_ context.Context, in *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.weaveReqs = append(f.weaveReqs, in)
	if f.weaveErr != nil {
		return nil, f.weaveErr
	}
	return &loomv1.WeaveResponse{Text: "answer"}, nil
}

func (f *fakeLoomClient) snapshotWeaveReqs() []*loomv1.WeaveRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*loomv1.WeaveRequest(nil), f.weaveReqs...)
}

func testEntry(qid string) Entry {
	return Entry{
		QuestionID:       qid,
		QuestionType:     "temporal-reasoning",
		Question:         "When did the trip happen?",
		QuestionDate:     "2023/05/20 (Sat) 02:21",
		HaystackDates:    []string{"2023/04/10 (Mon) 17:50"},
		HaystackSessions: [][]Turn{{{Role: "user", Content: "I went on a trip today."}}},
	}
}

func TestRunEntryContextStuffingSendsNoOccurredAt(t *testing.T) {
	fake := &fakeLoomClient{}
	r := &Runner{
		config: RunConfig{Mode: ModeContextStuffing, UseOccurredAt: true},
		logger: zap.NewNop(),
		client: fake,
	}

	result := r.runEntry(context.Background(), testEntry("q-cs"))
	require.Empty(t, result.Error)

	reqs := fake.snapshotWeaveReqs()
	require.NotEmpty(t, reqs)
	for _, req := range reqs {
		assert.Nil(t, req.OccurredAt, "context-stuffing must never send occurred_at")
	}
}

func TestRunEntryIngestSendsOccurredAt(t *testing.T) {
	fake := &fakeLoomClient{}
	r := &Runner{
		config: RunConfig{Mode: ModeIngest, UseOccurredAt: true},
		logger: zap.NewNop(),
		client: fake,
	}

	result := r.runEntry(context.Background(), testEntry("q-ingest"))
	require.Empty(t, result.Error)

	reqs := fake.snapshotWeaveReqs()
	require.Len(t, reqs, 2) // one haystack session + the question
	for _, req := range reqs {
		require.NotNil(t, req.OccurredAt)
	}
}

func TestRunAbortsOnTimeOverrideRejection(t *testing.T) {
	fake := &fakeLoomClient{
		weaveErr: status.Error(codes.FailedPrecondition,
			"occurred_at override is disabled on this server (set server.allow_time_override: true to accept replayed/imported conversations)"),
	}
	r := &Runner{
		config: RunConfig{
			Mode:          ModeIngest,
			UseOccurredAt: true,
			Isolate:       true,
			Concurrency:   3,
		},
		logger:    zap.NewNop(),
		client:    fake,
		baseAgent: &loomv1.AgentConfig{Name: "lme-base"},
	}

	entries := []Entry{testEntry("q1"), testEntry("q2"), testEntry("q3")}
	resultCh := make(chan EntryResult, len(entries))

	err := r.Run(context.Background(), entries, resultCh)
	require.Error(t, err, "an aborted run must surface a non-nil error")
	assert.Contains(t, err.Error(), "allow_time_override")

	// Every entry that created a temp agent must have deleted it on a live
	// context, even though the abort cancelled the run context.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.NotEmpty(t, fake.deleteAgentCtxErrs)
	for i, ctxErr := range fake.deleteAgentCtxErrs {
		assert.NoError(t, ctxErr, "temp-agent cleanup %d ran on a cancelled context", i)
	}
}

// weaveErrClient fails every Weave with a fixed status. Only Weave is
// called, so the embedded nil interface is never dereferenced.
type weaveErrClient struct {
	loomv1.LoomServiceClient
	err error
}

func (c *weaveErrClient) Weave(_ context.Context, _ *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
	return nil, c.err
}

// The other half of MAJOR-001: replayTurn dropped the gRPC status, so a
// server refusing the replay override was indistinguishable from a transient
// per-entry failure and the run never aborted. This walks the whole path —
// the refusal a default server actually returns, through replayTurn, into
// the fail-fast check — because either half alone silently restores the bug.
func TestReplayTurn_RecordsRejectionSoTheRunAborts(t *testing.T) {
	r := &Runner{
		client: &weaveErrClient{err: status.Error(codes.FailedPrecondition,
			"replay_assistant_message override is disabled on this server "+
				"(set server.allow_assistant_override: true to accept generation-free conversation replay)")},
	}

	var result EntryResult
	err := r.replayTurn(context.Background(), "sess-1", "user text", "assistant text", "", time.Time{}, &result)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, result.grpcCode,
		"replayTurn must record the status, or the rejection looks transient")

	// What Runner.Run stores before consulting the fail-fast check.
	result.Error = err.Error()
	msg, rejected := overrideRejection(result)
	assert.True(t, rejected, "a refused replay override must abort the run, not write error rows and exit zero")
	assert.Contains(t, msg, "allow_assistant_override")
}

// A successful replay must not mark the entry as rejected.
func TestReplayTurn_SuccessLeavesNoRejection(t *testing.T) {
	r := &Runner{client: &weaveOKClient{}}

	var result EntryResult
	require.NoError(t, r.replayTurn(context.Background(), "sess-1", "u", "a", "", time.Time{}, &result))
	assert.Equal(t, codes.OK, result.grpcCode)

	_, rejected := overrideRejection(result)
	assert.False(t, rejected)
}

type weaveOKClient struct {
	loomv1.LoomServiceClient
}

func (c *weaveOKClient) Weave(_ context.Context, _ *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
	return &loomv1.WeaveResponse{}, nil
}
