// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"

	"github.com/teradata-labs/loom/pkg/agent"
)

// chatWithAgentProgress runs one workflow agent turn. When ctx carries a
// progress callback (agent.ContextWithProgressCallback), the turn runs with
// ChatWithProgress, so tool events and streamed text reach the caller, and
// every event names agentID in ProgressEvent.AgentID (an event that already
// names an agent keeps it). Without a callback it is a plain Chat.
//
// The parallel, fork-join and swarm executors call this from one goroutine
// per agent, so a callback a caller puts in the context must be safe for
// concurrent use.
func chatWithAgentProgress(ctx context.Context, ag *agent.Agent, agentID, sessionID, prompt string) (*agent.Response, error) {
	cb := agent.ProgressCallbackFromContext(ctx)
	if cb == nil {
		return ag.Chat(ctx, sessionID, prompt)
	}
	return ag.ChatWithProgress(ctx, sessionID, prompt, nameAgentProgress(cb, agentID))
}

// nameAgentProgress returns a callback that sets ProgressEvent.AgentID to
// agentID on every event that names no agent yet, then calls cb.
func nameAgentProgress(cb agent.ProgressCallback, agentID string) agent.ProgressCallback {
	return func(ev agent.ProgressEvent) {
		if ev.AgentID == "" {
			ev.AgentID = agentID
		}
		cb(ev)
	}
}
