// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"time"
)

// occurredAtKey carries a historical arrival-time override through the
// conversation call chain (server Weave handler → Chat entry points →
// appendMessage). Follows the same context-key pattern as progressCallbackKey.
type occurredAtKey struct{}

// WithOccurredAt returns a context that overrides the arrival timestamp of
// every message persisted during the conversation call — the user turn, the
// assistant reply, and tool rows. It exists for replayed or imported
// conversations (WeaveRequest.occurred_at), where the wall clock at ingestion
// is not when the conversation happened: temporal grounding (compiled-view
// arrival stamps, graph-memory extraction anchoring) reads Message.Timestamp,
// so the override keeps all of those signals anchored to the conversation's
// real time. Live conversations must not use this.
func WithOccurredAt(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, occurredAtKey{}, t)
}

// occurredAtFromContext reports the arrival-time override, if any.
func occurredAtFromContext(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(occurredAtKey{}).(time.Time)
	return t, ok
}
