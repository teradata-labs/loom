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
	"unicode/utf8"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// queryToolResultDescription is the normative tool description of HLD §7.1.
const queryToolResultDescription = `Read the full data of a large tool result FROM THIS TURN, by the message_id shown
on the result. Data is held in memory only within the turn that produced it —
never use a message_id from conversation history; re-run the producing tool
instead. (message_id, offset, limit) returns bounded pages.`

// QueryToolResultTool serves this turn's memory, nothing else (HLD §7.1): pages
// (offset, limit) over one tool result's whole payload, addressed by its
// message_id. Every page is bounded at the threshold. Valid only within the
// producing turn:
// memory is per turn, large data is not persisted, and there is no cross-turn
// data door except re-run.
type QueryToolResultTool struct {
	agent *Agent
}

// NewQueryToolResultTool creates the message_id-addressed query tool (HLD §7.1).
func NewQueryToolResultTool(a *Agent) *QueryToolResultTool {
	return &QueryToolResultTool{agent: a}
}

// Name returns the tool name.
func (t *QueryToolResultTool) Name() string {
	return "query_tool_result"
}

// Description returns the normative §7.1 tool description.
func (t *QueryToolResultTool) Description() string {
	return queryToolResultDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *QueryToolResultTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"message_id": {
				Type:        "integer",
				Description: "The message_id of a tool result from THIS turn.",
			},
			"offset": {
				Type:        "integer",
				Description: "Skip first N items/lines (for pagination). Use with limit.",
			},
			"limit": {
				Type:        "integer",
				Description: "Return at most N items/lines (for pagination). Use with offset.",
			},
		},
		Required: []string{"message_id"},
	}
}

// Execute serves a page of the addressed message's whole payload.
func (t *QueryToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	messageID, ok := parseMessageID(input["message_id"])
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "message_id must be an integer — the id of a tool result from this turn",
			},
		}, nil
	}

	sessionID := session.SessionIDFromContext(ctx)
	payload, err := t.agent.InTurnPayload(sessionID, messageID)
	if err != nil {
		// The only honest door for anything not in this turn's memory is re-run.
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "not_this_turn",
				Message: fmt.Sprintf("message_id %d does not resolve to a current-turn in-memory payload — data is held in memory only within the turn that produced it. Re-run the producing call for fresh data.", messageID),
			},
		}, nil
	}

	threshold := t.agent.contextThreshold()

	if sqlQuery, ok := input["sql"].(string); ok && sqlQuery != "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "unsupported",
				Message: "sql over a tool result is not supported; page with offset and limit, or narrow the producing call",
			},
		}, nil
	}

	return t.paginate(payload, input, threshold)
}

// parseMessageID accepts the JSON number (float64) and integer-string forms.
func parseMessageID(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}

// paginate returns one bounded page of the payload: JSON array items when it is
// an array, else lines.
func (t *QueryToolResultTool) paginate(payload string, input map[string]interface{}, threshold int) (*shuttle.Result, error) {
	offset := 0
	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}
	limit := 100
	if l, ok := input["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if offset < 0 {
		offset = 0
	}

	var units []string
	kind := "lines"
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(payload), &items); err == nil {
		kind = "items"
		units = make([]string, len(items))
		for i, it := range items {
			units[i] = string(it)
		}
	} else {
		units = strings.Split(payload, "\n")
	}

	budget := threshold - 512 // envelope headroom for the wrapper fields
	if budget < 1024 {
		budget = 1024
	}

	// A unit larger than the whole page budget is SPLIT into budget-sized
	// pieces, not truncated: the page bound is the same §5.1 threshold as every
	// other bound, but cutting a unit and dropping the remainder would make it
	// unreachable — offset addresses units, so nothing could ask for the rest,
	// and re-running the producing call regenerates the same oversized unit.
	// Splitting keeps every byte reachable by paging on.
	units = splitOversizedUnits(units, budget)

	if offset >= len(units) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_offset",
				Message: fmt.Sprintf("Offset %d out of range (0-%d)", offset, len(units)-1),
			},
		}, nil
	}

	end := offset + limit
	if end > len(units) {
		end = len(units)
	}

	// Bound the serialized page at the threshold, cut at a unit boundary.
	page := make([]string, 0, end-offset)
	used := 0
	for i := offset; i < end; i++ {
		u := units[i]
		if used > 0 && used+len(u) > budget {
			end = i
			break
		}
		page = append(page, u)
		used += len(u) + 1
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			kind:             page,
			"offset":         offset,
			"limit":          limit,
			"returned_count": len(page),
			"total_count":    len(units),
			"has_more":       offset+len(page) < len(units),
		},
	}, nil
}

// splitOversizedUnits replaces any unit longer than budget with consecutive
// budget-sized pieces, cut on UTF-8 rune boundaries. Paging then walks the
// pieces like any other units, so an oversized unit stays fully reachable and
// total_count / has_more describe what is actually retrievable.
func splitOversizedUnits(units []string, budget int) []string {
	oversized := false
	for _, u := range units {
		if len(u) > budget {
			oversized = true
			break
		}
	}
	if !oversized {
		return units
	}
	out := make([]string, 0, len(units))
	for _, u := range units {
		for len(u) > budget {
			cut := budget
			for cut > 0 && !utf8.RuneStart(u[cut]) {
				cut--
			}
			if cut == 0 {
				cut = budget // pathological: no rune boundary in range
			}
			out = append(out, u[:cut])
			u = u[cut:]
		}
		out = append(out, u)
	}
	return out
}

// Backend returns the backend type this tool requires.
// Empty string means backend-agnostic (works with any agent).
func (t *QueryToolResultTool) Backend() string {
	return "" // Backend-agnostic built-in tool
}

// Ensure QueryToolResultTool implements shuttle.Tool interface.
var _ shuttle.Tool = (*QueryToolResultTool)(nil)
