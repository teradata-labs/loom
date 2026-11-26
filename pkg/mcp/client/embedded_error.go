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

package client

import (
	"encoding/json"
	"strings"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// detectEmbeddedError inspects an otherwise-successful tool result for an
// application-level failure encoded in its payload. Some MCP servers report
// tool failures as a successful CallToolResult whose content is JSON like
//
//	{"ok": false, "error": "..."}
//
// instead of setting isError (the Teradata MCP server does this). The MCP
// recommendation is to set isError=true, but loom cannot assume every server
// complies, so this opt-in heuristic (ServerConfig.DetectEmbeddedErrors)
// promotes such results to a tool error, allowing failure-recovery and other
// error-aware machinery to react.
//
// It returns the extracted error message and true when a failure shape is
// detected. It is deliberately conservative: only an explicit falsy success
// signal (ok/success == false, status == error/failed, or a non-empty
// error/errors field) counts as a failure, so tools that legitimately return
// truthy payloads are unaffected.
func detectEmbeddedError(result *protocol.CallToolResult) (string, bool) {
	if result == nil {
		return "", false
	}

	// Prefer structuredContent (2025-03-26): it is already the machine-
	// readable payload, so it is the least ambiguous place to look.
	if len(result.StructuredContent) > 0 {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			if msg, ok := probeEmbeddedError(raw); ok {
				return msg, true
			}
		}
	}

	// Fall back to text content blocks parsed as JSON. Only text blocks that
	// parse as a JSON object are considered; free-form text is never treated
	// as an error here (that is the LLM's job to read).
	for _, content := range result.Content {
		if content.Type != "text" {
			continue
		}
		if msg, ok := probeEmbeddedError([]byte(content.Text)); ok {
			return msg, true
		}
	}

	return "", false
}

// embeddedErrorProbe is the conventional error shape scanned for in a tool
// payload. json.RawMessage is used for error/errors so both a string
// ("error": "msg") and a structured value ("errors": [...]) are handled.
type embeddedErrorProbe struct {
	OK      *bool           `json:"ok"`
	Success *bool           `json:"success"`
	Status  string          `json:"status"`
	Error   json.RawMessage `json:"error"`
	Errors  json.RawMessage `json:"errors"`
}

// probeEmbeddedError parses raw as a JSON object and reports whether it
// carries a falsy success signal, returning a human-readable message.
func probeEmbeddedError(raw []byte) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	// Must be a JSON object to carry these fields; skip arrays/scalars.
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}

	var p embeddedErrorProbe
	if err := json.Unmarshal([]byte(trimmed), &p); err != nil {
		return "", false
	}

	failed := false
	if p.OK != nil && !*p.OK {
		failed = true
	}
	if p.Success != nil && !*p.Success {
		failed = true
	}
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "error", "fail", "failed", "failure":
		failed = true
	}
	if jsonHasContent(p.Error) || jsonHasContent(p.Errors) {
		failed = true
	}
	if !failed {
		return "", false
	}

	return embeddedErrorMessage(p, trimmed), true
}

// embeddedErrorMessage extracts the best available message describing the
// failure, preferring a string error, then a structured error/errors value,
// then the raw payload.
func embeddedErrorMessage(p embeddedErrorProbe, raw string) string {
	if s := rawJSONString(p.Error); s != "" {
		return s
	}
	if jsonHasContent(p.Error) {
		return strings.TrimSpace(string(p.Error))
	}
	if s := rawJSONString(p.Errors); s != "" {
		return s
	}
	if jsonHasContent(p.Errors) {
		return strings.TrimSpace(string(p.Errors))
	}
	return raw
}

// rawJSONString returns the decoded string if raw is a JSON string, else "".
func rawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// jsonHasContent reports whether raw is a meaningful (non-empty, non-falsy)
// JSON value. Empty strings, null, empty arrays/objects, 0, and false are
// treated as "no error present".
func jsonHasContent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", `""`, "[]", "{}", "0", "false":
		return false
	}
	return true
}
