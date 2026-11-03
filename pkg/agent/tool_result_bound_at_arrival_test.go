// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"strings"
	"testing"
)

// A tool result is bounded at the door. Above the threshold it enters the
// session as its first bytes plus a tail that says how much was cut and to
// narrow the call; the model is sent exactly those bytes. Nothing later has to
// cut, stub or re-render it.
func TestAToolResultIsBoundedAsItArrives(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg))
	ag.SetSharedMemoryThreshold(2048)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-bound", ag.config.Name, "")
	whole := strings.Repeat("a line of a very wide read\n", 20000) // ~540KB
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: whole, ToolUseID: "toolu_wide", AgentID: ag.id}, false)

	msgs := sess.GetMessages()
	got := msgs[len(msgs)-1]
	if got.Role != "tool" {
		t.Fatalf("the last message is %q, want the tool result", got.Role)
	}
	if len(got.Content) > 2048 {
		t.Fatalf("the result entered the session at %d bytes; the threshold is 2048", len(got.Content))
	}
	if !strings.Contains(got.Content, "Narrow the tool call") {
		t.Errorf("the result's tail does not say how to see more:\n%s", got.Content[len(got.Content)-300:])
	}
	if !strings.HasPrefix(got.Content, "a line of a very wide read\n") {
		t.Errorf("the result lost its own first bytes:\n%.120s", got.Content)
	}

	// What the model is sent is what entered: the same bytes, no re-render.
	sm, ok := sess.SegmentedMem.(*SegmentedMemory)
	if !ok {
		t.Fatal("no segmented memory on the session")
	}
	compiled := sm.GetMessagesForLLM()
	last := compiled[len(compiled)-1]
	if last.Role != "tool" || last.Content != got.Content {
		t.Errorf("the model is sent %d bytes of role %q; the session holds %d", len(last.Content), last.Role, len(got.Content))
	}
}
