// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
)

// scriptedResponseKey carries a generation-free assistant turn through the
// conversation call chain (server Weave handler → Chat entry points →
// runConversationLoop). Follows the same context-key pattern as occurredAtKey.
type scriptedResponseKey struct{}

// WithScriptedResponse returns a context that makes the next conversation turn
// generation-free: instead of calling the LLM, runConversationLoop uses this
// text verbatim as the turn's terminal assistant response. Everything else in
// the turn pipeline still runs — context compilation, memory compression
// (relief/fold), graph-memory extraction, and salience updates — so a
// pre-written conversation can be replayed turn-by-turn while preserving the
// ground-truth assistant content and exercising the memory system under
// realistic accumulation.
//
// It exists for the LongMemEval conversation-replay harness
// (WeaveRequest.replay_assistant_message) and is typically paired with
// WithOccurredAt to anchor the turn at its historical time. Live conversations
// must not use this.
func WithScriptedResponse(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, scriptedResponseKey{}, text)
}

// scriptedResponseFromContext reports the generation-free assistant turn, if
// any. The second return is false when unset, so an intentional empty override
// is distinguishable from absence (though the loop only substitutes non-empty
// text — an empty scripted turn falls through to normal generation).
func scriptedResponseFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(scriptedResponseKey{}).(string)
	return t, ok
}
