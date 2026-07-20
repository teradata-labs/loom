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

package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
)

// fakeWorkflowStream implements grpc.ServerStreamingClient[WorkflowProgress].
type fakeWorkflowStream struct {
	grpc.ClientStream
	events []*loomv1.WorkflowProgress
	i      int
}

func (f *fakeWorkflowStream) Recv() (*loomv1.WorkflowProgress, error) {
	if f.i >= len(f.events) {
		return nil, io.EOF
	}
	e := f.events[f.i]
	f.i++
	return e, nil
}

// streamWorkflowMock overrides only StreamWorkflow on the full client interface.
type streamWorkflowMock struct {
	loomv1.LoomServiceClient
	events  []*loomv1.WorkflowProgress
	openErr error
	gotReq  *loomv1.ExecuteWorkflowRequest
}

func (m *streamWorkflowMock) StreamWorkflow(_ context.Context, in *loomv1.ExecuteWorkflowRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[loomv1.WorkflowProgress], error) {
	m.gotReq = in
	if m.openErr != nil {
		return nil, m.openErr
	}
	return &fakeWorkflowStream{events: m.events}, nil
}

func newWorkflowBridge(t *testing.T, mock loomv1.LoomServiceClient) *LoomBridge {
	t.Helper()
	return NewLoomBridgeFromClient(mock, apps.NewUIResourceRegistry(), zap.NewNop())
}

// debateYAML is a minimal, self-contained workflow spec (agents + type) that
// LoadWorkflowFromYAMLBytes can parse into a WorkflowPattern.
const debateYAML = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: pg-sharing-debate
spec:
  type: debate
  topic: "Should an agent framework and a BI tool share one Postgres?"
  agent_ids:
    - pro
    - con
  rounds: 1
`

func TestHandleWorkflowStream_YAMLSpec_StreamsAndReturnsResults(t *testing.T) {
	mock := &streamWorkflowMock{events: []*loomv1.WorkflowProgress{
		{CurrentAgentId: "pro", Message: "arguing for", Progress: 33},
		{CurrentAgentId: "con", Message: "arguing against", Progress: 66},
		{Progress: 100, PartialResults: []*loomv1.AgentResult{
			{AgentId: "pro", Output: "Shared Postgres = zero ETL."},
			{AgentId: "con", Output: "Shared Postgres = coupled failure domains."},
		}},
	}}
	bridge := newWorkflowBridge(t, mock)
	emit := &captureEmitter{}

	res, err := bridge.handleWorkflowStream(context.Background(),
		map[string]interface{}{"workflow_yaml": debateYAML}, emit)
	require.NoError(t, err)
	require.NotNil(t, mock.gotReq.GetPattern(), "workflow_yaml must be parsed into a pattern")
	require.NotNil(t, mock.gotReq.GetPattern().GetDebate(), "type: debate => debate pattern")

	require.Len(t, res.Content, 1)
	assert.Contains(t, res.Content[0].Text, "zero ETL")
	assert.Contains(t, res.Content[0].Text, "coupled failure domains")
	assert.Equal(t, []string{"pro: arguing for", "con: arguing against"}, emit.messages,
		"each progress event is forwarded as a status line")
}

func TestHandleWorkflowStream_WorkflowRefPassesThrough(t *testing.T) {
	// A workflow_ref (saved workflow name) must NOT be rejected by the
	// pattern/yaml guard — it flows through to the server, which resolves it.
	mock := &streamWorkflowMock{events: []*loomv1.WorkflowProgress{
		{Progress: 100, PartialResults: []*loomv1.AgentResult{{AgentId: "a", Output: "done"}}},
	}}
	bridge := newWorkflowBridge(t, mock)

	res, err := bridge.handleWorkflowStream(context.Background(),
		map[string]interface{}{"workflow_ref": "supabase-explorer"}, &captureEmitter{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError, "workflow_ref must not be rejected by the pattern/yaml guard")
	require.NotNil(t, mock.gotReq, "the request must reach StreamWorkflow")
	assert.Equal(t, "supabase-explorer", mock.gotReq.GetWorkflowRef(), "workflow_ref flows through to the server")
	assert.Nil(t, mock.gotReq.GetPattern(), "no inline pattern — the server resolves the ref")
}

func TestHandleWorkflowStream_MissingWorkflowIsError(t *testing.T) {
	bridge := newWorkflowBridge(t, &streamWorkflowMock{})
	res, err := bridge.handleWorkflowStream(context.Background(), map[string]interface{}{}, &captureEmitter{})
	require.NoError(t, err) // tool-level error, not transport
	require.Len(t, res.Content, 1)
	assert.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(res.Content[0].Text), "workflow is required")
}

func TestHandleWorkflowStream_InvalidYAMLIsError(t *testing.T) {
	bridge := newWorkflowBridge(t, &streamWorkflowMock{})
	res, err := bridge.handleWorkflowStream(context.Background(),
		map[string]interface{}{"workflow_yaml": "type: not_a_real_pattern\nagents: []"}, &captureEmitter{})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "invalid workflow_yaml")
}

func TestHandleWorkflowStream_OpenError(t *testing.T) {
	bridge := newWorkflowBridge(t, &streamWorkflowMock{openErr: errors.New("rpc failed")})
	_, err := bridge.handleWorkflowStream(context.Background(),
		map[string]interface{}{"workflow_yaml": debateYAML}, &captureEmitter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc failed")
}
