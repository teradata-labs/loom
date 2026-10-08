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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store/filestore"
)

func newLuaRPCServer(t *testing.T) (*MultiAgentServer, *filestore.Store) {
	t.Helper()
	st, err := filestore.Open(t.TempDir(), luasandbox.Limits{})
	require.NoError(t, err)
	srv := NewMultiAgentServer(nil, nil)
	srv.SetLuaScriptStore(st)
	return srv, st
}

func codeOf(err error) codes.Code { return status.Code(err) }

func TestLuaScriptRPCs_Disabled(t *testing.T) {
	srv := NewMultiAgentServer(nil, nil)
	ctx := context.Background()
	_, err := srv.ListLuaScripts(ctx, &loomv1.ListLuaScriptsRequest{})
	assert.Equal(t, codes.FailedPrecondition, codeOf(err))
	_, err = srv.SaveLuaScript(ctx, &loomv1.SaveLuaScriptRequest{Name: "abc", Description: "d", Source: "return 1"})
	assert.Equal(t, codes.FailedPrecondition, codeOf(err))
	_, err = srv.GetLuaScript(ctx, &loomv1.GetLuaScriptRequest{Name: "abc"})
	assert.Equal(t, codes.FailedPrecondition, codeOf(err))
	_, err = srv.DeleteLuaScript(ctx, &loomv1.DeleteLuaScriptRequest{Name: "abc"})
	assert.Equal(t, codes.FailedPrecondition, codeOf(err))
}

// The design's round trip: save, get, list, overwrite, delete.
func TestLuaScriptRPCs_RoundTrip(t *testing.T) {
	srv, st := newLuaRPCServer(t)
	ctx := context.Background()
	params, err := structpb.NewStruct(map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{"region": map[string]interface{}{"type": "string"}},
	})
	require.NoError(t, err)

	saved, err := srv.SaveLuaScript(ctx, &loomv1.SaveLuaScriptRequest{
		Name: "top_stores", Description: "Top stores by sales.", Source: `return args.region`,
		Manifest: &loomv1.LuaScriptManifest{Parameters: params, Requires: []string{"execute_query"}, Returns: "rows"},
	})
	require.NoError(t, err)
	assert.True(t, saved.Created)
	assert.Equal(t, int32(1), saved.Script.Version)
	assert.Equal(t, "server", saved.Script.Owner)
	assert.Equal(t, `return args.region`, saved.Script.Source)
	assert.NotNil(t, saved.Script.UpdatedAt)

	_, err = srv.SaveLuaScript(ctx, &loomv1.SaveLuaScriptRequest{Name: "top_stores", Description: "d", Source: "return 1"})
	assert.Equal(t, codes.AlreadyExists, codeOf(err))

	got, err := srv.GetLuaScript(ctx, &loomv1.GetLuaScriptRequest{Name: "top_stores"})
	require.NoError(t, err)
	assert.Equal(t, []string{"execute_query"}, got.Script.Manifest.Requires)
	assert.Equal(t, "object", got.Script.Manifest.Parameters.AsMap()["type"])

	require.NoError(t, st.SetPublished(ctx, "top_stores", true))
	require.NoError(t, st.Attach(ctx, "sql-agent", "top_stores"))
	list, err := srv.ListLuaScripts(ctx, &loomv1.ListLuaScriptsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Scripts, 1)
	assert.Empty(t, list.Scripts[0].Source, "list omits the source")
	assert.True(t, list.Scripts[0].Published)
	assert.Equal(t, []string{"sql-agent"}, list.Scripts[0].AttachedAgents)

	over, err := srv.SaveLuaScript(ctx, &loomv1.SaveLuaScriptRequest{Name: "top_stores", Description: "v2", Source: "return 2", Overwrite: true})
	require.NoError(t, err)
	assert.False(t, over.Created)
	assert.Equal(t, int32(2), over.Script.Version)
	assert.True(t, over.Script.Published, "a new version keeps its published state")

	del, err := srv.DeleteLuaScript(ctx, &loomv1.DeleteLuaScriptRequest{Name: "top_stores"})
	require.NoError(t, err)
	assert.True(t, del.Deleted)
	_, err = srv.GetLuaScript(ctx, &loomv1.GetLuaScriptRequest{Name: "top_stores"})
	assert.Equal(t, codes.NotFound, codeOf(err))
	_, err = srv.DeleteLuaScript(ctx, &loomv1.DeleteLuaScriptRequest{Name: "top_stores"})
	assert.Equal(t, codes.NotFound, codeOf(err))
}

func TestLuaScriptRPCs_Validation(t *testing.T) {
	srv, _ := newLuaRPCServer(t)
	ctx := context.Background()
	for name, req := range map[string]*loomv1.SaveLuaScriptRequest{
		"bad name":       {Name: "Bad", Description: "d", Source: "return 1"},
		"no description": {Name: "abc", Source: "return 1"},
		"does not parse": {Name: "abc", Description: "d", Source: "return ("},
	} {
		_, err := srv.SaveLuaScript(ctx, req)
		assert.Equal(t, codes.InvalidArgument, codeOf(err), name)
	}
	_, err := srv.GetLuaScript(ctx, &loomv1.GetLuaScriptRequest{Name: "../etc"})
	assert.Equal(t, codes.InvalidArgument, codeOf(err), "names are validated before any lookup")
	_, err = srv.DeleteLuaScript(ctx, &loomv1.DeleteLuaScriptRequest{Name: ""})
	assert.Equal(t, codes.InvalidArgument, codeOf(err))
}
