// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// failingLLMResultLog fails every llm_call_result write and records the rest.
type failingLLMResultLog struct {
	*recordingEventLog
	llmResultWrites atomic.Int32
}

func (f *failingLLMResultLog) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	if eventType == eventlog.EventLLMCallResult {
		f.llmResultWrites.Add(1)
		return errors.New("event log unavailable")
	}
	return f.recordingEventLog.RecordResult(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

// execCountTool counts how often the loop actually executes it.
type execCountTool struct{ runs atomic.Int32 }

func (c *execCountTool) Name() string        { return "counter" }
func (c *execCountTool) Description() string { return "counts executions" }
func (c *execCountTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (c *execCountTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	c.runs.Add(1)
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (c *execCountTool) Backend() string { return "" }

func newLLMResultAgent(log eventlog.EventLogStore, responses []mockLLMResponse, tool *execCountTool) *Agent {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: responses}, WithConfig(cfg), WithEventLog(log))
	ag.tools.Register(tool)
	return ag
}

// A response's tool calls run only after its llm_call_result is durable. Without
// the result a crash would redo the model call and could pick different tool
// calls, so the turn stops instead and no tool runs.
func TestRunConversationLoop_LLMResultWriteFailureStopsBeforeTools(t *testing.T) {
	log := &failingLLMResultLog{recordingEventLog: &recordingEventLog{}}
	tool := &execCountTool{}
	ag := newLLMResultAgent(log, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "counter", Input: map[string]interface{}{}}}},
	}, tool)

	_, err := ag.Chat(context.Background(), "s-llm-result-fail", "do it")
	if err == nil {
		t.Fatal("Chat succeeded although the llm_call_result could not be recorded")
	}
	if got := tool.runs.Load(); got != 0 {
		t.Fatalf("tool ran %d times before its response was recorded, want 0", got)
	}
	if log.llmResultWrites.Load() < 2 {
		t.Fatalf("expected the write to be retried, got %d attempt(s)", log.llmResultWrites.Load())
	}
}

// A response with no tool calls has nothing to protect: its result stays best-effort.
func TestRunConversationLoop_LLMResultWriteFailureTextOnlyStillFinishes(t *testing.T) {
	log := &failingLLMResultLog{recordingEventLog: &recordingEventLog{}}
	ag := newLLMResultAgent(log, []mockLLMResponse{{content: "done"}}, &execCountTool{})

	if _, err := ag.Chat(context.Background(), "s-llm-result-text", "hello"); err != nil {
		t.Fatalf("a text-only turn must not fail on a lost llm_call_result: %v", err)
	}
}
