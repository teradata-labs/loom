// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package session

import "context"

// sessionIDKey is the context key for session IDs
type sessionIDKey struct{}

// WithSessionID injects a session ID into the context
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext extracts the session ID from the context
// Returns empty string if not found
func SessionIDFromContext(ctx context.Context) string {
	if sessionID, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return sessionID
	}
	return ""
}
