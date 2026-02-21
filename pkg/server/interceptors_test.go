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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"go.uber.org/zap/zaptest"
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

func TestUserIDInterceptor_LogsDefaultFallback(t *testing.T) {
	// Verify that the interceptor works correctly with an explicit Logger set,
	// exercising the default-fallback code path with logging enabled.
	testLogger := zaptest.NewLogger(t)

	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: false,
		DefaultUserID: "logged-default",
		Logger:        testLogger,
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
	assert.Equal(t, "logged-default", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDInterceptor_NilLoggerNoPanic(t *testing.T) {
	// Verify that a nil Logger does not cause a panic (the code initializes
	// a no-op logger when Logger is nil).
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: false,
		DefaultUserID: "safe-default",
		Logger:        nil,
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
	assert.Equal(t, "safe-default", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDInterceptor_LoggerWithValidHeader(t *testing.T) {
	// Verify that providing a Logger alongside a valid header does not panic
	// and correctly extracts the user ID (exercises the Debug log path).
	testLogger := zaptest.NewLogger(t)

	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: true,
		Logger:        testLogger,
	})

	md := metadata.Pairs(UserIDHeader, "carol")
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
	assert.Equal(t, "carol", postgres.UserIDFromContext(capturedCtx))
}

func TestUserIDValidation_TooLong(t *testing.T) {
	// A user ID with 257 characters should be rejected by the interceptor.
	longID := strings.Repeat("a", 257)
	interceptor := UserIDUnaryInterceptor(UserIDConfig{
		RequireUserID: true,
	})

	md := metadata.Pairs(UserIDHeader, longID)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called for an invalid user ID")
		return nil, nil
	}

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

	require.Error(t, err)
	assert.Nil(t, resp)
	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "exceeds maximum length")
}

func TestUserIDValidation_ControlChars(t *testing.T) {
	tests := []struct {
		name   string
		userID string
	}{
		{name: "null byte", userID: "user\x00id"},
		{name: "SOH control char", userID: "user\x01id"},
		{name: "newline", userID: "user\nid"},
		{name: "tab", userID: "user\tid"},
		{name: "leading null", userID: "\x00admin"},
		{name: "escape char", userID: "admin\x1b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := UserIDUnaryInterceptor(UserIDConfig{
				RequireUserID: true,
			})

			md := metadata.Pairs(UserIDHeader, tt.userID)
			ctx := metadata.NewIncomingContext(context.Background(), md)

			handler := func(ctx context.Context, req any) (any, error) {
				t.Fatal("handler should not be called for a user ID with control characters")
				return nil, nil
			}

			resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)

			require.Error(t, err)
			assert.Nil(t, resp)
			st, ok := status.FromError(err)
			require.True(t, ok, "error should be a gRPC status")
			assert.Equal(t, codes.InvalidArgument, st.Code())
			assert.Contains(t, st.Message(), "control character")
		})
	}
}

func TestUserIDValidation_ValidChars(t *testing.T) {
	tests := []struct {
		name   string
		userID string
	}{
		{name: "simple alphanumeric", userID: "alice123"},
		{name: "with dashes", userID: "alice-bob"},
		{name: "with dots", userID: "alice.bob"},
		{name: "with underscores", userID: "alice_bob"},
		{name: "with spaces", userID: "alice bob"},
		{name: "email format", userID: "alice@example.com"},
		{name: "unicode characters", userID: "user-\u00e9\u00e8\u00ea"},
		{name: "CJK characters", userID: "\u7528\u6237\u540d"},
		{name: "mixed unicode and ascii", userID: "user-\u00fc\u00f1\u00ee-123"},
		{name: "max length 256", userID: strings.Repeat("x", 256)},
		{name: "single character", userID: "a"},
		{name: "UUID format", userID: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "with slashes", userID: "org/team/user"},
		{name: "with colons", userID: "tenant:user:123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := UserIDUnaryInterceptor(UserIDConfig{
				RequireUserID: true,
			})

			md := metadata.Pairs(UserIDHeader, tt.userID)
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
			assert.Equal(t, tt.userID, postgres.UserIDFromContext(capturedCtx))
		})
	}
}

func TestValidateUserID_Unit(t *testing.T) {
	// Direct unit tests for the validateUserID function.
	tests := []struct {
		name    string
		id      string
		wantErr bool
		errMsg  string
	}{
		{name: "empty string", id: "", wantErr: true, errMsg: "must not be empty"},
		{name: "valid simple", id: "alice", wantErr: false},
		{name: "exactly 256 chars", id: strings.Repeat("b", 256), wantErr: false},
		{name: "257 chars", id: strings.Repeat("b", 257), wantErr: true, errMsg: "exceeds maximum length"},
		{name: "null byte", id: "a\x00b", wantErr: true, errMsg: "control character"},
		{name: "BEL char", id: "a\x07b", wantErr: true, errMsg: "control character"},
		{name: "space is allowed", id: "a b", wantErr: false},
		{name: "DEL is allowed", id: "a\x7fb", wantErr: false},
		{name: "tilde is allowed", id: "user~1", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUserID(tt.id)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
