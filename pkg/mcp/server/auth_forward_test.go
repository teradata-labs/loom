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
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestAppendOutgoingAuth(t *testing.T) {
	t.Run("forwards bearer and user id", func(t *testing.T) {
		ctx := ContextWithOutgoingAuth(context.Background(), "jwt-token", "user-42")
		md, ok := metadata.FromOutgoingContext(appendOutgoingAuth(ctx))
		require.True(t, ok)
		assert.Equal(t, []string{"Bearer jwt-token"}, md.Get("authorization"))
		assert.Equal(t, []string{"user-42"}, md.Get("x-user-id"))
	})

	t.Run("no outgoing auth leaves context unchanged", func(t *testing.T) {
		ctx := context.Background()
		_, ok := metadata.FromOutgoingContext(appendOutgoingAuth(ctx))
		assert.False(t, ok, "no metadata should be attached when no outgoing auth is present")
	})

	t.Run("bearer only", func(t *testing.T) {
		ctx := ContextWithOutgoingAuth(context.Background(), "jwt-token", "")
		md, _ := metadata.FromOutgoingContext(appendOutgoingAuth(ctx))
		assert.Equal(t, []string{"Bearer jwt-token"}, md.Get("authorization"))
		assert.Empty(t, md.Get("x-user-id"))
	})
}

func TestBearerForwardUnaryInterceptor(t *testing.T) {
	var capturedAuthz, capturedUID []string
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		capturedAuthz = md.Get("authorization")
		capturedUID = md.Get("x-user-id")
		return nil
	}
	ctx := ContextWithOutgoingAuth(context.Background(), "tok", "sub-1")
	err := bearerForwardUnaryInterceptor(ctx, "/loom.v1.LoomService/Weave", nil, nil, nil, invoker)
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer tok"}, capturedAuthz)
	assert.Equal(t, []string{"sub-1"}, capturedUID)
}
