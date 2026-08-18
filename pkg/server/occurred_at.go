// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// occurredAtMaxSkew is the clock-skew allowance when validating that an
// occurred_at override is not in the future. Client and server clocks are
// rarely perfectly aligned; a few minutes of tolerance avoids rejecting
// legitimate "just now" imports without opening the door to future-dated rows.
const occurredAtMaxSkew = 5 * time.Minute

// applyOccurredAt validates WeaveRequest.occurred_at and, when present and
// allowed, threads it through the context so agent.appendMessage anchors every
// row persisted during the call at the conversation's historical time.
//
// Client-supplied timestamps can poison temporal grounding (compiled-view
// arrival stamps, graph-memory extraction anchoring), so the override is
// opt-in per server: requests carrying occurred_at are rejected with
// FAILED_PRECONDITION unless allow is true (server.allow_time_override).
// Future-dated values are rejected with INVALID_ARGUMENT regardless.
func applyOccurredAt(ctx context.Context, req *loomv1.WeaveRequest, allow bool) (context.Context, error) {
	if req.GetOccurredAt() == nil {
		return ctx, nil
	}
	if !allow {
		return nil, status.Error(codes.FailedPrecondition,
			"occurred_at override is disabled on this server (set server.allow_time_override: true to accept replayed/imported conversations)")
	}
	if err := req.GetOccurredAt().CheckValid(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid occurred_at: %v", err)
	}
	t := req.GetOccurredAt().AsTime()
	if t.After(time.Now().Add(occurredAtMaxSkew)) {
		return nil, status.Errorf(codes.InvalidArgument,
			"occurred_at is in the future: %s", t.Format(time.RFC3339))
	}
	return agent.WithOccurredAt(ctx, t), nil
}

// applyReplayAssistant validates WeaveRequest.replay_assistant_message and,
// when present and allowed, threads it through the context so
// runConversationLoop substitutes it for the provider call — recording the
// text verbatim as the assistant turn while still running the full memory
// pipeline (context compilation, compression, extraction, salience). See
// agent.WithScriptedResponse.
//
// Generation-free replay lets a caller supply the assistant's side of the
// conversation, so — like occurred_at — it is opt-in per server and gated by
// the same server.allow_time_override switch; requests carrying the field are
// rejected with FAILED_PRECONDITION unless allow is true.
func applyReplayAssistant(ctx context.Context, req *loomv1.WeaveRequest, allow bool) (context.Context, error) {
	msg := req.GetReplayAssistantMessage()
	if msg == "" {
		return ctx, nil
	}
	if !allow {
		return nil, status.Error(codes.FailedPrecondition,
			"replay_assistant_message override is disabled on this server (set server.allow_time_override: true to accept generation-free conversation replay)")
	}
	return agent.WithScriptedResponse(ctx, msg), nil
}
