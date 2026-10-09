// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package llm

import "context"

// suppressThinkingKey marks a request that must be sent with extended thinking
// off, whatever the client is configured for.
type suppressThinkingKey struct{}

// ContextSuppressThinking returns a context that tells the provider clients to
// send this ONE request with thinking off.
//
// The reason is a history the provider will not accept with thinking on.
// Thinking blocks are turn-scoped replay material and are never persisted, so
// a turn rebuilt from the session store — a HITL park and resume, mid tool
// loop — replays its assistant tool_use rows without the thinking blocks that
// accompanied them. Anthropic rejects that pairing outright, so the turn would
// die on resume rather than continue.
//
// Scoped to the request, not the client: the next turn builds its history in
// memory again and thinking returns on its own.
func ContextSuppressThinking(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressThinkingKey{}, true)
}

// ThinkingSuppressed reports whether this request must go out with thinking
// off. Clients consult it where they build the thinking field.
func ThinkingSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressThinkingKey{}).(bool)
	return v
}
