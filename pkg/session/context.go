// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package session

import "context"

// sessionIDKey is the context key for session IDs
type sessionIDKey struct{}

// agentIDKey is the context key for agent IDs
type agentIDKey struct{}

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

// WithAgentID injects an agent ID into the context
func WithAgentID(ctx context.Context, agentID string) context.Context {
	if agentID == "" {
		return ctx
	}
	return context.WithValue(ctx, agentIDKey{}, agentID)
}

// AgentIDFromContext extracts the agent ID from the context
// Returns empty string if not found
// Supports both typed key (agentIDKey{}) and string key ("agent_id") for backward compatibility
func AgentIDFromContext(ctx context.Context) string {
	// Try typed key first
	if agentID, ok := ctx.Value(agentIDKey{}).(string); ok {
		return agentID
	}
	// Fallback to string key for backward compatibility with contextWithValue wrappers
	if agentID, ok := ctx.Value("agent_id").(string); ok {
		return agentID
	}
	return ""
}
