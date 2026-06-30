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

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// outgoingAuth carries the caller's identity from the HTTP-MCP edge to the looms
// gRPC server. The loom-mcp HTTP entrypoint validates the inbound Supabase JWT,
// then stashes it here; the bridge's client interceptors forward it as gRPC
// metadata so looms (and its RLS) see the authenticated user.
type outgoingAuth struct {
	bearer string // raw JWT (forwarded as "authorization: Bearer ..."), looms re-validates
	userID string // token subject (forwarded as x-user-id) for when looms auth is disabled
}

type outgoingAuthKey struct{}

// ContextWithOutgoingAuth attaches the caller's bearer token and user id so the
// bridge forwards them to looms on subsequent gRPC calls made with this context.
func ContextWithOutgoingAuth(ctx context.Context, bearer, userID string) context.Context {
	return context.WithValue(ctx, outgoingAuthKey{}, outgoingAuth{bearer: bearer, userID: userID})
}

func outgoingAuthFromContext(ctx context.Context) (outgoingAuth, bool) {
	a, ok := ctx.Value(outgoingAuthKey{}).(outgoingAuth)
	return a, ok
}

// appendOutgoingAuth adds the forwarded identity to the outgoing gRPC metadata.
func appendOutgoingAuth(ctx context.Context) context.Context {
	a, ok := outgoingAuthFromContext(ctx)
	if !ok {
		return ctx
	}
	// Header names must match pkg/server: authMetadataKey ("authorization") and
	// UserIDHeader ("x-user-id"). Hardcoded here to avoid importing pkg/server
	// (a different "server" package) from the bridge.
	var kv []string
	if a.bearer != "" {
		kv = append(kv, "authorization", "Bearer "+a.bearer)
	}
	if a.userID != "" {
		kv = append(kv, "x-user-id", a.userID)
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// bearerForwardUnaryInterceptor forwards the edge-validated identity on unary RPCs.
func bearerForwardUnaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoker(appendOutgoingAuth(ctx), method, req, reply, cc, opts...)
}

// bearerForwardStreamInterceptor forwards the edge-validated identity on streaming RPCs.
func bearerForwardStreamInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return streamer(appendOutgoingAuth(ctx), desc, cc, method, opts...)
}
