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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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

// Naming the exempt tools replaces the set, and an empty name exempts nothing:
// a tool named before and not named now is bounded again.
func TestTheExemptSetIsReplacedNotAdded(t *testing.T) {
	ag, sess, ctx := payloadAgent(t)
	topic := strings.Repeat("SELECT … syntax line\n", 2100)
	ag.SetOffloadExemptTools([]string{"", "get_syntax_help"})
	asked(ctx, ag, sess, "tu-a", "get_syntax_help", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: topic, ToolUseID: "tu-a"}, false)
	if lastTool(sess).Content != topic {
		t.Fatal("a named tool was bounded")
	}
	ag.SetOffloadExemptTools([]string{"other_tool"})
	asked(ctx, ag, sess, "tu-b", "get_syntax_help", false)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: topic, ToolUseID: "tu-b"}, false)
	if len(lastTool(sess).Content) > 2048 {
		t.Fatal("a tool no longer named still entered whole")
	}
}

// tailUseID is the id a pageable tail names.
var tailUseID = regexp.MustCompile(`query_tool_result tool_use_id=(\S+) \(offset, limit\)`)

// A cut result's tail names the id that pages it whole, and the tool it names
// is registered: the model can read every byte this turn from what it was
// shown. On the next turn the same id says re-run.
func TestACutResultIsPagedWholeByTheIDInItsTail(t *testing.T) {
	ag, sess, ctx := payloadAgent(t)
	if ag.tools.IsRegistered("query_tool_result") {
		t.Fatal("query_tool_result is registered before any result is cut")
	}
	lines := make([]string, 3000)
	for i := range lines {
		lines[i] = "row " + strconv.Itoa(i)
	}
	whole := strings.Join(lines, "\n")
	asked(ctx, ag, sess, "toolu_01AbC-xyz", "execute_query", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: whole, ToolUseID: "toolu_01AbC-xyz"}, false)

	row := lastTool(sess)
	if len(row.Content) > 2048 {
		t.Fatalf("the model would see %d bytes; the bound is 2048", len(row.Content))
	}
	m := tailUseID.FindStringSubmatch(row.Content)
	if m == nil {
		t.Fatalf("the tail names no id to page by: %q", row.Content[len(row.Content)-300:])
	}
	if !ag.tools.IsRegistered("query_tool_result") {
		t.Fatal("the tail names query_tool_result but the agent cannot call it")
	}

	tool := NewQueryToolResultTool(ag)
	qctx := session.WithSessionID(ctx, sess.ID)
	res, err := tool.Execute(qctx, map[string]interface{}{"tool_use_id": m[1], "offset": float64(2990), "limit": float64(10)})
	if err != nil || !res.Success {
		t.Fatalf("paging by the tail's id: %v %+v", err, res)
	}
	data := res.Data.(map[string]interface{})
	page := data["lines"].([]string)
	if len(page) != 10 || page[9] != "row 2999" || data["total_count"] != 3000 {
		t.Fatalf("the last page should end at row 2999 of 3000, got %v (total %v)", page, data["total_count"])
	}

	asked(ctx, ag, sess, "tu-next", "execute_query", true)
	res, _ = tool.Execute(qctx, map[string]interface{}{"tool_use_id": m[1]})
	if res.Success || res.Error == nil || res.Error.Code != "not_this_turn" {
		t.Fatalf("an earlier turn's id says re-run, got %+v", res)
	}
}

// A session with no store has no message ids; its cut results page by
// tool_use_id all the same.
func TestAStorelessSessionPagesItsCutResult(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg), WithMemory(NewMemory()))
	ag.SetSharedMemoryThreshold(2048)
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-storeless", ag.config.Name, "")

	whole := strings.Repeat("row,value\n", 3000)
	asked(ctx, ag, sess, "tu-mem", "execute_query", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: whole, ToolUseID: "tu-mem"}, false)
	if m := tailUseID.FindStringSubmatch(lastTool(sess).Content); m == nil || m[1] != "tu-mem" {
		t.Fatalf("the tail names no id to page by: %v", m)
	}
	got, err := ag.InTurnPayloadByToolUseID(sess.ID, "tu-mem")
	if err != nil || got != whole {
		t.Fatalf("the storeless result reads back at %d of %d bytes: %v", len(got), len(whole), err)
	}
}

// An agent that suppresses query_tool_result is never told to call it.
func TestASuppressedQueryToolIsNeverNamed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg), WithMemory(NewMemory()), WithoutBuiltinTool("query_tool_result"))
	ag.SetSharedMemoryThreshold(2048)
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-suppressed", ag.config.Name, "")

	asked(ctx, ag, sess, "tu-s", "execute_query", true)
	ag.appendMessage(ctx, sess, Message{Role: "tool", Content: strings.Repeat("x", 30000), ToolUseID: "tu-s"}, false)
	row := lastTool(sess).Content
	if strings.Contains(row, "query_tool_result") || ag.tools.IsRegistered("query_tool_result") {
		t.Fatalf("a suppressed query_tool_result is named or registered: %q", row[len(row)-250:])
	}
	if !strings.Contains(row, "Narrow the tool call") {
		t.Fatalf("the plain tail is missing: %q", row[len(row)-250:])
	}
}

// The tail that names an id holds the bound at every threshold; where it
// cannot fit, the plain tail stands.
func TestThePageableTailHoldsTheBound(t *testing.T) {
	id := strings.Repeat("u", 64)
	content := strings.Repeat("é", 40000) // multi-byte: cuts land on rune boundaries
	for threshold := minThreshold; threshold <= 4096; threshold += 37 {
		got := boundToolRow(content, threshold, id)
		if len(got) > threshold {
			t.Fatalf("threshold %d: row is %d bytes", threshold, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("threshold %d: cut inside a rune", threshold)
		}
		named := strings.Contains(got, "tool_use_id="+id)
		plain := strings.Contains(got, "Narrow the tool call (range")
		if named == plain {
			t.Fatalf("threshold %d: exactly one tail expected, named=%v plain=%v", threshold, named, plain)
		}
	}
	if !strings.Contains(boundToolRow(content, 16384, id), "tool_use_id="+id) {
		t.Fatal("at the default bound the tail names the id")
	}
}
