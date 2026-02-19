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
	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUserIDUnaryInterceptor_ValidHeader(t *testing.T) {
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	// Build incoming context with x-user-id metadata.
	md := metadata.Pairs(UserIDHeader, "alice")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	require.NotNil(t, capturedCtx, "handler must be called")
	assert.Equal(t, "alice", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDUnaryInterceptor_MissingHeader_RequireTrue(t *testing.T) {
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	// No metadata at all.
	ctx := context.Background()

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called when user ID is missing and required")
		return nil, nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	assert.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Contains(t, st.Message(), "x-user-id")
}

func TestUserIDUnaryInterceptor_MissingHeader_RequireFalse_DefaultUserId(t *testing.T) {
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: false,
		DefaultUserID: "test-user",
	})

	ctx := context.Background()

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	require.NotNil(t, capturedCtx, "handler must be called")
	assert.Equal(t, "test-user", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDUnaryInterceptor_MissingHeader_RequireFalse_FallbackDefault(t *testing.T) {
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: false,
		DefaultUserID: "", // empty triggers "default-user" fallback
	})

	ctx := context.Background()

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	require.NotNil(t, capturedCtx, "handler must be called")
	assert.Equal(t, "default-user", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDUnaryInterceptor_EmptyHeader(t *testing.T) {
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	// Metadata present but the value is an empty string.
	md := metadata.Pairs(UserIDHeader, "")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called when user ID header is empty and required")
		return nil, nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	assert.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

// mockServerStream is a minimal grpc.ServerStream implementation for testing.
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestUserIDStreamInterceptor_ValidHeader(t *testing.T) {
	interceptor := UserIDStreamInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	md := metadata.Pairs(UserIDHeader, "bob")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	stream := &mockServerStream{ctx: ctx}

	var capturedCtx context.Context
	handler := func(srv any, ss grpc.ServerStream) error {
		capturedCtx = ss.Context()
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)

	require.NoError(t, err)
	require.NotNil(t, capturedCtx, "handler must be called")
	assert.Equal(t, "bob", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDStreamInterceptor_MissingHeader_RequireTrue(t *testing.T) {
	interceptor := UserIDStreamInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	ctx := context.Background()
	stream := &mockServerStream{ctx: ctx}

	handler := func(srv any, ss grpc.ServerStream) error {
		t.Fatal("handler should not be called when user ID is missing and required")
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

func TestUserIDStreamInterceptor_MissingHeader_RequireFalse(t *testing.T) {
	interceptor := UserIDStreamInterceptor(UserIDConfig{
		RequireUserID: false,
		DefaultUserID: "stream-default",
	})

	ctx := context.Background()
	stream := &mockServerStream{ctx: ctx}

	var capturedCtx context.Context
	handler := func(srv any, ss grpc.ServerStream) error {
		capturedCtx = ss.Context()
		return nil
	}

	err := interceptor(nil, stream, &grpc.StreamServerInfo{}, handler)

	require.NoError(t, err)
	require.NotNil(t, capturedCtx, "handler must be called")
	assert.Equal(t, "stream-default", postgres.UserIDFromContext(capturedCtx))
}
