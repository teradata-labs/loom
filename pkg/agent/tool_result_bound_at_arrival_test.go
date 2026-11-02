// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A tool result is bounded at the door. Above the threshold it enters the
// session as its first bytes plus the tail naming the file that holds the
// whole of it; the model is sent exactly those bytes; the file holds the rest.
// Nothing later has to cut, stub or re-render it, so a result of any size can
// never outgrow the window.
func TestAToolResultIsBoundedAsItArrives(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg))
	dir := t.TempDir()
	ag.SetToolSnapshotDir(dir)
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
	path := ToolSnapshotPath(dir, "toolu_wide")
	if !strings.Contains(got.Content, path) {
		t.Errorf("the result does not name the file holding the whole of it:\n%s", got.Content[len(got.Content)-300:])
	}
	if !strings.HasPrefix(got.Content, "a line of a very wide read\n") {
		t.Errorf("the result lost its own first bytes:\n%.120s", got.Content)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != whole {
		t.Fatalf("the file does not hold the whole result: err=%v len=%d want %d", err, len(saved), len(whole))
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
