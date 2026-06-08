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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
)

// fakeWeaveStream implements grpc.ServerStreamingClient[loomv1.WeaveProgress] by
// replaying a fixed sequence of progress events, then io.EOF. Only Recv is
// exercised by handleWeaveStream; the embedded ClientStream satisfies the rest.
type fakeWeaveStream struct {
	grpc.ClientStream
	events []*loomv1.WeaveProgress
	idx    int
}

func (f *fakeWeaveStream) Recv() (*loomv1.WeaveProgress, error) {
	if f.idx >= len(f.events) {
		return nil, io.EOF
	}
	e := f.events[f.idx]
	f.idx++
	return e, nil
}

// streamWeaveMock embeds the full client interface and overrides only StreamWeave.
type streamWeaveMock struct {
	loomv1.LoomServiceClient
	events  []*loomv1.WeaveProgress
	openErr error
}

func (m *streamWeaveMock) StreamWeave(_ context.Context, _ *loomv1.WeaveRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[loomv1.WeaveProgress], error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return &fakeWeaveStream{events: m.events}, nil
}

// captureEmitter records the progress values forwarded by handleWeaveStream.
type captureEmitter struct {
	progress []float64
}

func (c *captureEmitter) EmitProgress(progress, _ float64) error {
	c.progress = append(c.progress, progress)
	return nil
}

func newStreamBridge(t *testing.T, mock loomv1.LoomServiceClient) *LoomBridge {
	t.Helper()
	return NewLoomBridgeFromClient(mock, apps.NewUIResourceRegistry(), zap.NewNop())
}

func TestHandleWeaveStream_ForwardsProgressAndFinalContent(t *testing.T) {
	mock := &streamWeaveMock{events: []*loomv1.WeaveProgress{
		{Progress: 30, Message: "thinking"}, // intermediate (stage unspecified)
		{Stage: loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED, Progress: 100, PartialContent: "final answer"},
	}}
	bridge := newStreamBridge(t, mock)
	emit := &captureEmitter{}

	res, err := bridge.handleWeaveStream(context.Background(), map[string]interface{}{"query": "hi"}, emit)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "final answer", res.Content[0].Text)
	assert.Equal(t, []float64{30, 100}, emit.progress, "every progress>0 event should be forwarded")
}

func TestHandleWeaveStream_FallsBackToPartialResultDataJSON(t *testing.T) {
	mock := &streamWeaveMock{events: []*loomv1.WeaveProgress{
		{
			Stage:         loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
			Progress:      100,
			PartialResult: &loomv1.ExecutionResult{Type: "text", DataJson: "from result"},
		},
	}}
	bridge := newStreamBridge(t, mock)
	emit := &captureEmitter{}

	res, err := bridge.handleWeaveStream(context.Background(), map[string]interface{}{"query": "hi"}, emit)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "from result", res.Content[0].Text,
		"when PartialContent is empty, fall back to the completion event's PartialResult")
}

func TestHandleWeaveStream_OpenError(t *testing.T) {
	mock := &streamWeaveMock{openErr: errors.New("rpc failed")}
	bridge := newStreamBridge(t, mock)

	_, err := bridge.handleWeaveStream(context.Background(), map[string]interface{}{"query": "hi"}, &captureEmitter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc failed")
}
