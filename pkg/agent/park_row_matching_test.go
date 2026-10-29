// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"reflect"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// TestLocateParkedBatch_MatchesRowsToCallsByPosition pins how
// locateParkedBatch decides which calls of the tail batch already have a tool
// row. Providers really do emit duplicate IDs (Gemini reuses the function name
// for parallel calls to one tool) and empty IDs (Ollama), so a row cannot be
// matched to its call by ID alone: the first N calls carrying an ID own that
// ID's N rows, because rows are appended in dispatch order.
func TestLocateParkedBatch_MatchesRowsToCallsByPosition(t *testing.T) {
	call := func(id, q string) llmtypes.ToolCall {
		return llmtypes.ToolCall{ID: id, Name: "search", Input: map[string]interface{}{"q": q}}
	}
	tests := []struct {
		name         string
		calls        []llmtypes.ToolCall
		rowIDs       []string // ToolUseID of each tool row, in append order
		wantPosition []int    // true batch positions of the rowless calls
	}{
		{
			name:         "duplicate IDs, first call has its row",
			calls:        []llmtypes.ToolCall{call("search", "a"), call("search", "b")},
			rowIDs:       []string{"search"},
			wantPosition: []int{1},
		},
		{
			name:         "duplicate IDs, both have rows",
			calls:        []llmtypes.ToolCall{call("search", "a"), call("search", "b")},
			rowIDs:       []string{"search", "search"},
			wantPosition: nil,
		},
		{
			name:         "duplicate IDs, no rows",
			calls:        []llmtypes.ToolCall{call("search", "a"), call("search", "b")},
			rowIDs:       nil,
			wantPosition: []int{0, 1},
		},
		{
			name:         "empty IDs, two of three have rows",
			calls:        []llmtypes.ToolCall{call("", "a"), call("", "b"), call("", "c")},
			rowIDs:       []string{"", ""},
			wantPosition: []int{2},
		},
		{
			name:         "mixed unique, duplicate and empty IDs",
			calls:        []llmtypes.ToolCall{call("u", "a"), call("search", "b"), call("search", "c"), call("", "d")},
			rowIDs:       []string{"u", "search", ""},
			wantPosition: []int{2},
		},
		{
			name:         "a row for an ID no call carries is ignored",
			calls:        []llmtypes.ToolCall{call("x", "a"), call("y", "b")},
			rowIDs:       []string{"stray", "x"},
			wantPosition: []int{1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PatternConfig = DefaultPatternConfig()
			cfg.PatternConfig.UseLLMClassifier = false
			ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg))

			ctx := context.Background()
			sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-row-match-"+tc.name, ag.config.Name, "")
			ag.appendMessage(ctx, sess, Message{Role: "user", Content: "go"}, true)
			ag.appendMessage(ctx, sess, Message{Role: "assistant", ToolCalls: tc.calls}, false)
			for _, id := range tc.rowIDs {
				ag.appendMessage(ctx, sess, Message{Role: "tool", ToolUseID: id, Content: "ok"}, false)
			}

			_, rowless, positions, err := locateParkedBatch(sess, nil)
			if err != nil {
				t.Fatalf("locateParkedBatch: %v", err)
			}
			if !reflect.DeepEqual(positions, tc.wantPosition) {
				t.Errorf("rowless positions = %v, want %v", positions, tc.wantPosition)
			}
			if len(rowless) != len(positions) {
				t.Fatalf("rowless (%d) and positions (%d) must stay parallel", len(rowless), len(positions))
			}
			for j, c := range rowless {
				if want := tc.calls[positions[j]]; c.ID != want.ID || !reflect.DeepEqual(c.Input, want.Input) {
					t.Errorf("rowless[%d] = %+v, want the call at batch position %d (%+v)", j, c, positions[j], want)
				}
			}
		})
	}
}
