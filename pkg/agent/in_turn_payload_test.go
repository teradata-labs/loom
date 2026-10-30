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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
)

func payloadAgent(t *testing.T) (*Agent, *Session, context.Context) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	ag.SetSharedMemoryThreshold(2048)
	ctx := context.Background()
	return ag, ag.memory.GetOrCreateSessionWithAgent(ctx, "s-payload", ag.config.Name, ""), ctx
}

// asked appends the assistant row that calls a tool, so the result that
// follows answers a named call.
func asked(ctx context.Context, ag *Agent, sess *Session, id, tool string, turnStart bool) {
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "go"}, turnStart)
	ag.appendMessage(ctx, sess, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: tool}}}, false)
}

func lastTool(sess *Session) Message {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "tool" {
			return sess.Messages[i]
		}
	}
	return Message{}
}

// A tool whose whole result is the point of the call — a syntax reference read
// inline in the turn that asked — is named exempt, and its result enters whole.
func TestAnExemptToolsResultEntersWhole(t *testing.T) {
	ag, sess, ctx := payloadAgent(t)
	ag.SetOffloadExemptTools([]string{"get_syntax_help"})

	topic := strings.Repeat("SELECT … syntax line\n", 2100) // ~44KB
	asked(ctx, ag, sess, "tu-syntax", "get_syntax_help", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: topic, ToolUseID: "tu-syntax"}, false)
	if got := lastTool(sess).Content; got != topic {
		t.Fatalf("an exempt tool's result was cut to %d of %d bytes", len(got), len(topic))
	}

	asked(ctx, ag, sess, "tu-other", "file_read", false)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: topic, ToolUseID: "tu-other"}, false)
	if got := lastTool(sess).Content; len(got) > 2048 {
		t.Fatalf("a tool not named exempt entered at %d bytes; the bound is 2048", len(got))
	}
}

// A host tool can stage a current-turn result whole by its message id, though
// the model sees it bounded. A result from an earlier turn is not there.
func TestACurrentTurnResultIsReadableWholeByItsID(t *testing.T) {
	ag, sess, ctx := payloadAgent(t)

	whole := strings.Repeat("row,value\n", 3000) // 30KB
	asked(ctx, ag, sess, "tu-big", "execute_query", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: whole, ToolUseID: "tu-big"}, false)
	row := lastTool(sess)
	if len(row.Content) > 2048 {
		t.Fatalf("the model would see %d bytes; the bound is 2048", len(row.Content))
	}
	id, err := strconv.ParseInt(row.ID, 10, 64)
	if err != nil {
		t.Fatalf("the row has no message id: %q", row.ID)
	}

	got, err := ag.InTurnPayload(sess.ID, id)
	if err != nil || got != whole {
		t.Fatalf("the current turn's result reads back at %d of %d bytes: %v", len(got), len(whole), err)
	}

	asked(ctx, ag, sess, "tu-next", "execute_query", true)
	if _, err := ag.InTurnPayload(sess.ID, id); !errors.Is(err, ErrNotThisTurn) {
		t.Fatalf("a result from an earlier turn reads as not this turn's, got %v", err)
	}
}

// query_tool_result pages through a current-turn result whole, though the
// model saw it bounded; sql is not offered and says so.
func TestQueryToolResultPagesTheWholeOfThisTurnsResult(t *testing.T) {
	ag, sess, ctx := payloadAgent(t)
	lines := make([]string, 3000)
	for i := range lines {
		lines[i] = "row " + strconv.Itoa(i)
	}
	whole := strings.Join(lines, "\n")
	asked(ctx, ag, sess, "tu-q", "execute_query", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: whole, ToolUseID: "tu-q"}, false)
	id, _ := strconv.ParseInt(lastTool(sess).ID, 10, 64)

	tool := NewQueryToolResultTool(ag)
	qctx := session.WithSessionID(ctx, sess.ID)
	res, err := tool.Execute(qctx, map[string]interface{}{"message_id": float64(id), "offset": float64(2990), "limit": float64(10)})
	if err != nil || !res.Success {
		t.Fatalf("a page of this turn's result: %v %+v", err, res)
	}
	data := res.Data.(map[string]interface{})
	page := data["lines"].([]string)
	if len(page) != 10 || page[9] != "row 2999" || data["total_count"] != 3000 {
		t.Fatalf("the last page should end at row 2999 of 3000, got %v (total %v)", page, data["total_count"])
	}

	res, _ = tool.Execute(qctx, map[string]interface{}{"message_id": float64(id), "sql": "SELECT 1"})
	if res.Success || res.Error == nil || res.Error.Code != "unsupported" {
		t.Fatalf("sql is refused as unsupported, got %+v", res)
	}
}
