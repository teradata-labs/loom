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
// This file holds the caller-identity context plumbing. It lives in the leaf
// types package so both storage backends can read it: pkg/storage/postgres
// imports pkg/agent, so the SQLite store (in pkg/agent) cannot import the
// postgres package's accessor without a cycle. postgres re-exports these for
// its existing callers.
package types

import "context"

// DefaultUserID is the single-tenant owner recorded when no caller identity
// is present (local SQLite deployments, auth disabled).
const DefaultUserID = "default-user"

// userIDKey is the context key for the authenticated caller's user ID.
type userIDKey struct{}

// ContextWithUserID returns a new context with the given user ID attached.
func ContextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserIDFromContext extracts the user ID from the context, if present.
// Returns empty string if no user ID is set.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey{}).(string); ok {
		return v
	}
	return ""
}
