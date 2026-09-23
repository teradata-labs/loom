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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// artifactSessionMockClient captures the artifact requests the bridge builds.
// The shared mockLoomClient leaves these RPCs on the nil embedded interface,
// so this test carries its own.
type artifactSessionMockClient struct {
	loomv1.LoomServiceClient

	gotList *loomv1.ListArtifactsRequest
	gotGet  *loomv1.GetArtifactRequest
}

func (m *artifactSessionMockClient) ListArtifacts(_ context.Context, in *loomv1.ListArtifactsRequest, _ ...grpc.CallOption) (*loomv1.ListArtifactsResponse, error) {
	m.gotList = in
	return &loomv1.ListArtifactsResponse{}, nil
}

func (m *artifactSessionMockClient) GetArtifact(_ context.Context, in *loomv1.GetArtifactRequest, _ ...grpc.CallOption) (*loomv1.GetArtifactResponse, error) {
	m.gotGet = in
	return &loomv1.GetArtifactResponse{Artifact: &loomv1.Artifact{Id: "a1"}}, nil
}

// A field that works but is not advertised is unreachable for a model driving
// the tool from its published schema, which is the surface loom-mcp exposes.
// These two tools are the read path a model uses to find a session's files.
func TestLoomBridge_ArtifactToolsAdvertiseSessionID(t *testing.T) {
	bridge := NewLoomBridgeFromClient(&artifactSessionMockClient{}, nil, zaptest.NewLogger(t))

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	byName := make(map[string]protocol.Tool, len(tools))
	for _, tl := range tools {
		byName[tl.Name] = tl
	}

	for _, name := range []string{"loom_list_artifacts", "loom_get_artifact"} {
		t.Run(name, func(t *testing.T) {
			tl, ok := byName[name]
			require.Truef(t, ok, "%s should be advertised", name)

			props, _ := tl.InputSchema["properties"].(map[string]interface{})
			require.NotNilf(t, props, "%s should have schema properties", name)

			raw, has := props["session_id"]
			require.Truef(t, has, "%s should expose session_id", name)

			spec, _ := raw.(map[string]interface{})
			require.NotNilf(t, spec, "%s session_id should be an object spec", name)
			assert.Equal(t, "string", spec["type"], "%s session_id should be a string", name)
			assert.NotEmptyf(t, spec["description"], "%s session_id needs a description a model can act on", name)
		})
	}
}

// The bridge marshals raw tool args straight into the proto request, so the
// schema addition and the wire behaviour have to agree: an advertised
// session_id must actually arrive as ListArtifactsRequest.session_id /
// GetArtifactRequest.session_id.
func TestLoomBridge_ArtifactToolsForwardSessionID(t *testing.T) {
	mock := &artifactSessionMockClient{}
	bridge := NewLoomBridgeFromClient(mock, nil, zaptest.NewLogger(t))
	ctx := context.Background()

	res, err := bridge.CallTool(ctx, "loom_list_artifacts", map[string]interface{}{"session_id": "sess-1"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, mock.gotList, "loom_list_artifacts should have reached the client")
	assert.Equal(t, "sess-1", mock.gotList.SessionId)

	res, err = bridge.CallTool(ctx, "loom_get_artifact", map[string]interface{}{
		"name":       "report.md",
		"session_id": "sess-2",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, mock.gotGet, "loom_get_artifact should have reached the client")
	assert.Equal(t, "report.md", mock.gotGet.Name)
	assert.Equal(t, "sess-2", mock.gotGet.SessionId)
}
