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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// recallDescription is the normative tool description of HLD §6.
const recallDescription = `Retrieve conversation that is no longer shown, by an address cited in the
session summary: recall("msg:41-78") returns those messages as stored;
recall("msg:41") is the single-message form. Tool results are not included —
re-run the producing call for data. Returns are bounded; a cut range prints
the exact next call.`

// recallTruncationTailFormat is the normative continuation line of HLD §6.
const recallTruncationTailFormat = `[range truncated — continue: recall("msg:%d-%d")]`

// RecallTool retrieves conversation that is no longer shown, by an address
// cited in the session summary (HLD §6). It returns the span's user and
// assistant rows as stored — call signatures included, since they live inside
// the assistant row and are what makes "re-run" literal — and omits role='tool'
// rows entirely; a result's only door is re-running its call.
type RecallTool struct {
	agent *Agent
}

// NewRecallTool creates the recall tool (HLD §6). Registered always.
func NewRecallTool(a *Agent) *RecallTool {
	return &RecallTool{agent: a}
}

// Name returns the tool name.
func (t *RecallTool) Name() string {
	return "recall"
}

// Description returns the normative §6 tool description.
func (t *RecallTool) Description() string {
	return recallDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *RecallTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"range": {
				Type:        "string",
				Description: `The address cited in the session summary: "msg:41-78" for a span, "msg:41" for a single message.`,
			},
		},
		Required: []string{"range"},
	}
}

// Execute reads the span's user and assistant rows as stored (session-filtered)
// and returns them bounded at the threshold, cut at a row boundary with the
// normative continuation line.
func (t *RecallTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	rangeArg, _ := input["range"].(string)
	lo, hi, err := parseMsgRange(rangeArg)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: fmt.Sprintf(`range must be "msg:A-B" or "msg:N": %v`, err),
			},
		}, nil
	}

	store := t.agent.memory.GetStore()
	if store == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "store_not_available",
				Message: "no session store configured — nothing to recall from",
			},
		}, nil
	}

	sessionID := session.SessionIDFromContext(ctx)
	messages, err := store.ListMessagesBySeqRange(ctx, sessionID, lo, hi)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "read_failed",
				Message: fmt.Sprintf("failed to read msg:%d-%d: %v", lo, hi, err),
			},
		}, nil
	}

	threshold := t.agent.contextThreshold()

	var sb strings.Builder
	truncatedFrom := int64(0)
	for _, m := range messages {
		// Omit role='tool' rows entirely — a result's only door is re-running
		// its call (HLD §6).
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		row := renderRecalledRow(m)
		tail := "\n" + fmt.Sprintf(recallTruncationTailFormat, lo, hi)
		if sb.Len() > 0 && sb.Len()+1+len(row)+len(tail) > threshold {
			seq, _ := strconv.ParseInt(m.ID, 10, 64)
			truncatedFrom = seq
			break
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(row)
	}

	out := sb.String()
	if truncatedFrom > 0 {
		out += "\n" + fmt.Sprintf(recallTruncationTailFormat, truncatedFrom, hi)
	}
	if out == "" {
		out = fmt.Sprintf("msg:%d-%d holds no user or assistant rows in this session.", lo, hi)
	}

	return &shuttle.Result{Success: true, Data: out}, nil
}

// renderRecalledRow renders one stored row: its address, role, content, and —
// for assistant rows — the complete call signatures.
func renderRecalledRow(m Message) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "msg:%s %s: %s", m.ID, m.Role, m.Content)
	if len(m.ToolCalls) > 0 {
		if calls, err := json.Marshal(m.ToolCalls); err == nil {
			sb.WriteString("\n  tool_calls: ")
			sb.Write(calls)
		}
	}
	return sb.String()
}

// parseMsgRange parses "msg:A-B" and the single-message form "msg:N"
// (equivalent to a span of one).
func parseMsgRange(s string) (int64, int64, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "msg:") {
		return 0, 0, fmt.Errorf("missing msg: prefix in %q", s)
	}
	body := strings.TrimPrefix(s, "msg:")
	if lo, hi, found := strings.Cut(body, "-"); found {
		loN, err := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bad span start %q", lo)
		}
		hiN, err := strconv.ParseInt(strings.TrimSpace(hi), 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("bad span end %q", hi)
		}
		if hiN < loN {
			return 0, 0, fmt.Errorf("span end %d before start %d", hiN, loN)
		}
		return loN, hiN, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(body), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad message number %q", body)
	}
	return n, n, nil
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *RecallTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure RecallTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*RecallTool)(nil)
