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

//go:build fts5

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// overflowTool always fails with the numeric-overflow error the lesson
// fixtures are keyed on.
type overflowTool struct{ calls int }

func (o *overflowTool) Name() string        { return "overflowy" }
func (o *overflowTool) Description() string { return "always fails with a numeric overflow" }
func (o *overflowTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (o *overflowTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	o.calls++
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "db_error",
			Message: "Error 2616: numeric overflow occurred during computation of CC_Number",
		},
	}, nil
}
func (o *overflowTool) Backend() string { return "" }

// A crash-resumed batch dispatches its never-attempted calls through
// dispatchOneCall, which buffers tool-error text in st.errTexts. ResumeSession
// must drain that buffer into the error-triggered lesson lane exactly as the
// conversation loop and ResumeChat do, or the lesson is silently dropped.
func TestResumeSession_FailedDispatchInjectsErrorLesson(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       fleetLessonAgentID,
		Content:       "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	recorder := &recordingEventLog{}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithEventLog(recorder),
		WithGraphMemoryStore(store, &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true}))
	tool := &overflowTool{}
	ag.tools.Register(tool)

	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-resume-lesson", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run overflowy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []types.ToolCall{{ID: "c1", Name: "overflowy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	_, err = ag.ResumeSession(ctx, "s-resume-lesson")
	require.NoError(t, err)
	require.Equal(t, 1, tool.calls, "the never-attempted call is dispatched exactly once")

	var lessonMsgs int
	for _, m := range sess.GetMessages() {
		if m.Role == roleLessonReminder && strings.Contains(m.Content, "matched to the error just hit") {
			lessonMsgs++
			assert.Contains(t, m.Content, "BIGINT")
		}
	}
	assert.Equal(t, 1, lessonMsgs, "the crash-resumed batch's failure must reach the error-lesson lane once")
}
