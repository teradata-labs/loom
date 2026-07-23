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
	"fmt"
	"sync"

	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ManagePatternsTool provides list/load/unload over the pattern library.
// Patterns have no risk classification and no load cap. Unlike skills,
// unload exists here: it only flips this tool's own per-session loaded-set
// (used by list) and never touches the context — a loaded pattern's body
// stays in L1 until the pressure pipeline reclaims it.
type ManagePatternsTool struct {
	orchestrator *patterns.Orchestrator

	mu     sync.Mutex
	active map[string]map[string]bool // sessionID -> pattern name -> loaded
}

// NewManagePatternsTool creates the manage_patterns builtin.
func NewManagePatternsTool(orchestrator *patterns.Orchestrator) *ManagePatternsTool {
	return &ManagePatternsTool{
		orchestrator: orchestrator,
		active:       make(map[string]map[string]bool),
	}
}

// Name returns the tool name.
func (t *ManagePatternsTool) Name() string { return "manage_patterns" }

// Backend returns the backend type this tool requires (empty = agnostic).
func (t *ManagePatternsTool) Backend() string { return "" }

// Description returns the tool description for the LLM.
func (t *ManagePatternsTool) Description() string {
	return `Manage the session's loaded pattern set: list available patterns, load one for its worked template, or unload one.

Three actions available:
1. list - List all available patterns, with which ones are loaded this session
2. load - Load a pattern by name and get its template/instructions
3. unload - Mark a pattern as no longer loaded for this session`
}

// InputSchema returns the JSON schema for tool parameters.
func (t *ManagePatternsTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action to perform: 'list', 'load', or 'unload'",
			},
			"name": {
				Type:        "string",
				Description: "(load/unload only) Pattern name",
			},
		},
		Required: []string{"action"},
	}
}

// Execute routes to the requested action.
func (t *ManagePatternsTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "MISSING_SESSION_ID", Message: "session ID not found in context"},
		}, nil
	}

	action, _ := input["action"].(string)
	name, _ := input["name"].(string)

	switch action {
	case "list":
		return t.executeList(sessionID)
	case "load":
		return t.executeLoad(sessionID, name)
	case "unload":
		return t.executeUnload(sessionID, name)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("unknown action %q; must be list, load, or unload", action),
			},
		}, nil
	}
}

// executeList returns every pattern the library knows about, annotated with
// whether it has been loaded this session.
func (t *ManagePatternsTool) executeList(sessionID string) (*shuttle.Result, error) {
	summaries := t.orchestrator.GetLibrary().ListAll()
	loaded := t.loadedSet(sessionID)

	items := make([]map[string]interface{}, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, map[string]interface{}{
			"name":        s.Name,
			"title":       s.Title,
			"description": s.Description,
			"category":    s.Category,
			"difficulty":  s.Difficulty,
			"loaded":      loaded[s.Name],
		})
	}

	data := map[string]interface{}{
		"action":       "list",
		"count":        len(items),
		"loaded_count": len(loaded),
		"patterns":     items,
	}
	return jsonResult(data)
}

// executeLoad loads a pattern by name (charter-classed per D-3's loaderTools
// whitelist) and marks it loaded for the session.
func (t *ManagePatternsTool) executeLoad(sessionID, name string) (*shuttle.Result, error) {
	if name == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "INVALID_PARAMETER", Message: "name is required for load"},
		}, nil
	}

	pattern, err := t.orchestrator.GetLibrary().Load(name)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "PATTERN_NOT_FOUND", Message: fmt.Sprintf("pattern not found: %s", name)},
		}, nil
	}

	t.mu.Lock()
	if t.active[sessionID] == nil {
		t.active[sessionID] = make(map[string]bool)
	}
	wasLoaded := t.active[sessionID][name]
	t.active[sessionID][name] = true
	loadedCount := len(t.active[sessionID])
	t.mu.Unlock()

	data := map[string]interface{}{
		"action":         "load",
		"status":         "loaded",
		"pattern":        pattern.Name,
		"title":          pattern.Title,
		"category":       pattern.Category,
		"use_cases":      pattern.UseCases,
		"already_loaded": wasLoaded,
		"loaded_count":   loadedCount,
	}
	return jsonResult(data)
}

// executeUnload marks a pattern as no longer loaded for the session.
// Unloading a pattern that isn't loaded is a no-op success (idempotent).
func (t *ManagePatternsTool) executeUnload(sessionID, name string) (*shuttle.Result, error) {
	if name == "" {
		return &shuttle.Result{
			Success: false,
			Error:   &shuttle.Error{Code: "INVALID_PARAMETER", Message: "name is required for unload"},
		}, nil
	}

	t.mu.Lock()
	wasLoaded := t.active[sessionID][name]
	delete(t.active[sessionID], name)
	loadedCount := len(t.active[sessionID])
	t.mu.Unlock()

	data := map[string]interface{}{
		"action":       "unload",
		"status":       "unloaded",
		"pattern":      name,
		"was_loaded":   wasLoaded,
		"loaded_count": loadedCount,
	}
	return jsonResult(data)
}

// loadedSet returns a snapshot of the pattern names loaded for a session.
func (t *ManagePatternsTool) loadedSet(sessionID string) map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	src := t.active[sessionID]
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// jsonResult and errorResult are shared with GraphMemoryTool (graph_memory_tool.go).

// Ensure ManagePatternsTool implements shuttle.Tool.
var _ shuttle.Tool = (*ManagePatternsTool)(nil)
