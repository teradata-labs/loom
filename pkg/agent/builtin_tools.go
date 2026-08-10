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
instead. (message_id, offset, limit) returns bounded pages; (message_id, sql)
queries tabular data as table ` + "`results`" + `.`

// sqlTruncationTailFormat is the normative truncate-and-advise tail of HLD §7.1.
const sqlTruncationTailFormat = "[result truncated at %d bytes — narrow with LIMIT / aggregation and re-query]"

// QueryToolResultTool serves this turn's memory, nothing else (HLD §7.1): pages
// (offset, limit) or SQL over one L1 message's full in-memory payload, addressed
// by message_id — the id printed on the offload stub. Every return is bounded at
// the threshold — pages and SQL alike. Valid only within the producing turn:
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
				Description: "The message_id printed on the offload stub of a result from THIS turn.",
			},
			"sql": {
				Type:        "string",
				Description: "SQL over the payload as table 'results' (tabular payloads only).",
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

// Execute serves pages or SQL over the addressed message's in-memory payload.
func (t *QueryToolResultTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	messageID, ok := parseMessageID(input["message_id"])
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_input",
				Message: "message_id must be an integer — the id printed on the offload stub of a result from this turn",
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
		return t.querySQL(sessionID, messageID, payload, sqlQuery, threshold)
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

// querySQL lazily parses the payload into the in-turn SQLite (dropped at turn
// end) and runs the query against table `results`. The serialized result is
// bounded at the threshold, cut at a result-row boundary, ending with the
// normative truncate-and-advise tail (§7.1).
func (t *QueryToolResultTool) querySQL(sessionID string, messageID int64, payload, sqlQuery string, threshold int) (*shuttle.Result, error) {
	db, err := t.agent.inTurnSQLite(sessionID, messageID, payload)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "not_tabular",
				Message:    fmt.Sprintf("payload of message %d is not tabular (%v)", messageID, err),
				Suggestion: "Use (message_id, offset, limit) paging for non-rectangular payloads.",
			},
		}, nil
	}

	rows, err := db.Query(sqlQuery)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "query_failed",
				Message:    fmt.Sprintf("Query failed: %v", err),
				Suggestion: "Table name is 'results'. Check your SQL syntax.",
			},
		}, nil
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "query_failed", Message: err.Error()},
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("[")
	truncated := false
	first := true
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return &shuttle.Result{
				Success: false,
				Error:   &shuttle.Error{Code: "query_failed", Message: err.Error()},
			}, nil
		}
		rowObj := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				rowObj[c] = string(b)
			} else {
				rowObj[c] = vals[i]
			}
		}
		rowJSON, err := json.Marshal(rowObj)
		if err != nil {
			continue
		}
		sep := 0
		if !first {
			sep = 1
		}
		// Cut at a result-row boundary: stop before the row that would push the
		// serialized return past the threshold (leaving room for the tail).
		tail := "\n" + fmt.Sprintf(sqlTruncationTailFormat, threshold)
		if sb.Len()+sep+len(rowJSON)+1+len(tail) > threshold {
			truncated = true
			break
		}
		if !first {
			sb.WriteString(",")
		}
		sb.Write(rowJSON)
		first = false
	}
	if err := rows.Err(); err != nil {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "query_failed", Message: err.Error()},
		}, nil
	}
	sb.WriteString("]")
	out := sb.String()
	if truncated {
		out += "\n" + fmt.Sprintf(sqlTruncationTailFormat, threshold)
	}

	return &shuttle.Result{Success: true, Data: out}, nil
}

// paginate serves bounded pages over the payload: items for a JSON array,
// lines otherwise. The serialized return is bounded at the threshold — a page
// must never stub itself (§5.2 renders at-threshold results full).
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
