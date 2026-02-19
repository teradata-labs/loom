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

	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// UserIDHeader is the gRPC metadata key for the user ID.
	UserIDHeader = "x-user-id"
)

// UserIDConfig controls the behavior of the user ID interceptors.
type UserIDConfig struct {
	// RequireUserID when true returns Unauthenticated if X-User-ID is missing.
	RequireUserID bool

	// DefaultUserID is used when RequireUserID is false and no header is present.
	// Falls back to "default-user" if empty.
	DefaultUserID string
}

// UserIDUnaryInterceptor extracts X-User-ID from gRPC metadata and injects
// into context via ContextWithUserID. Returns codes.Unauthenticated if missing
// and RequireUserID is true.
func UserIDUnaryInterceptor(cfg UserIDConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := extractUserID(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// UserIDStreamInterceptor extracts X-User-ID from gRPC metadata and injects
// into context for streaming RPCs.
func UserIDStreamInterceptor(cfg UserIDConfig) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := extractUserID(ss.Context(), cfg)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// extractUserID extracts user ID from gRPC metadata or applies defaults.
func extractUserID(ctx context.Context, cfg UserIDConfig) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		vals := md.Get(UserIDHeader)
		if len(vals) > 0 && vals[0] != "" {
			return postgres.ContextWithUserID(ctx, vals[0]), nil
		}
	}

	// No user ID in metadata
	if cfg.RequireUserID {
		return nil, status.Error(codes.Unauthenticated, "x-user-id header required")
	}

	// Use default
	defaultID := cfg.DefaultUserID
	if defaultID == "" {
		defaultID = "default-user"
	}
	return postgres.ContextWithUserID(ctx, defaultID), nil
}

// wrappedServerStream wraps a grpc.ServerStream with a custom context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapped context.
func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}
