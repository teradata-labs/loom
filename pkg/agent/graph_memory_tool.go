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

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// GraphMemoryTool provides agent-facing graph memory operations.
// Actions: remember, recall, forget, supersede, consolidate, context_for, entities, relate.
type GraphMemoryTool struct {
	store   memory.GraphMemoryStore
	agentID string
}

// NewGraphMemoryTool creates a new graph memory tool.
func NewGraphMemoryTool(store memory.GraphMemoryStore, agentID string) *GraphMemoryTool {
	return &GraphMemoryTool{store: store, agentID: agentID}
}

func (t *GraphMemoryTool) Name() string    { return "graph_memory" }
func (t *GraphMemoryTool) Backend() string { return "" }
func (t *GraphMemoryTool) Description() string {
	return `Manage graph-backed episodic memory with salience-driven retrieval.

Actions:
1. remember - Store an immutable memory with entity links and salience
2. recall - Search memories by content (FTS) with salience ranking and token budget
3. forget - Soft-delete a memory (set expiration to now)
4. supersede - Replace a memory with a corrected version (creates SUPERSEDES lineage)
5. consolidate - Merge multiple related memories into a summary (creates CONSOLIDATES lineage)
6. context_for - Get entity profile + graph neighborhood + ranked memories within budget
7. entities - Search or list entities in the knowledge graph
8. relate - Create a directed relationship between two entities

Use this to build and query long-term knowledge about users, tools, patterns, and decisions.`
}

func (t *GraphMemoryTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"action": {
				Type:        "string",
				Description: "Action: remember, recall, forget, supersede, consolidate, context_for, entities, relate",
			},
			"content": {
				Type:        "string",
				Description: "(remember/supersede/consolidate) Memory content",
			},
			"summary": {
				Type:        "string",
				Description: "(remember/supersede/consolidate) Short summary for budget-constrained retrieval",
			},
			"memory_type": {
				Type:        "string",
				Description: "(remember/recall) Type: fact, preference, decision, experience, failure, observation",
			},
			"tags": {
				Type:        "array",
				Description: "(remember/recall/supersede/consolidate) Tags for filtering",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"entity_ids": {
				Type:        "array",
				Description: "(remember/recall) Entity IDs to link or scope to",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"salience": {
				Type:        "number",
				Description: "(remember/consolidate) Initial salience 0.0-1.0 (default 0.5)",
			},
			"properties_json": {
				Type:        "string",
				Description: "(remember/supersede) Type-specific structured data as JSON",
			},
			"query": {
				Type:        "string",
				Description: "(recall/entities) Search query",
			},
			"max_tokens": {
				Type:        "integer",
				Description: "(recall/context_for) Token budget limit",
			},
			"min_salience": {
				Type:        "number",
				Description: "(recall) Minimum salience threshold (default 0.1)",
			},
			"memory_id": {
				Type:        "string",
				Description: "(forget/supersede) Memory ID to operate on",
			},
			"memory_ids": {
				Type:        "array",
				Description: "(consolidate) Memory IDs to merge",
				Items:       &shuttle.JSONSchema{Type: "string"},
			},
			"entity_name": {
				Type:        "string",
				Description: "(context_for) Entity name to get context for",
			},
			"topic": {
				Type:        "string",
				Description: "(context_for) Topic for relevant memory search",
			},
			"entity_type": {
				Type:        "string",
				Description: "(entities) Filter by entity type",
			},
			"limit": {
				Type:        "integer",
				Description: "(recall/entities) Max results",
			},
			"source_name": {
				Type:        "string",
				Description: "(relate) Source entity name",
			},
			"target_name": {
				Type:        "string",
				Description: "(relate) Target entity name",
			},
			"relation": {
				Type:        "string",
				Description: "(relate) Relationship type (e.g. USES, WORKS_ON, KNOWS_ABOUT)",
			},
		},
		Required: []string{"action"},
	}
}

func (t *GraphMemoryTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	action, ok := input["action"].(string)
	if !ok || action == "" {
		return errorResult("INVALID_PARAMETER", "action is required"), nil
	}

	switch action {
	case "remember":
		return t.executeRemember(ctx, input)
	case "recall":
		return t.executeRecall(ctx, input)
	case "forget":
		return t.executeForget(ctx, input)
	case "supersede":
		return t.executeSupersede(ctx, input)
	case "consolidate":
		return t.executeConsolidate(ctx, input)
	case "context_for":
		return t.executeContextFor(ctx, input)
	case "entities":
		return t.executeEntities(ctx, input)
	case "relate":
		return t.executeRelate(ctx, input)
	default:
		return errorResult("INVALID_ACTION",
			"unknown action: "+action+". Valid: remember, recall, forget, supersede, consolidate, context_for, entities, relate"), nil
	}
}

func (t *GraphMemoryTool) executeRemember(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	content, _ := input["content"].(string)
	if content == "" {
		return errorResult("INVALID_PARAMETER", "content is required for remember"), nil
	}

	mem := &memory.Memory{
		AgentID:        t.agentID,
		Content:        content,
		Summary:        getStr(input, "summary"),
		MemoryType:     getStr(input, "memory_type"),
		Source:         "agent",
		MemoryAgentID:  t.agentID,
		Tags:           getStrSlice(input, "tags"),
		Salience:       getFloat(input, "salience"),
		EntityIDs:      getStrSlice(input, "entity_ids"),
		PropertiesJSON: getStr(input, "properties_json"),
	}
	if mem.MemoryType == "" {
		mem.MemoryType = memory.MemoryTypeFact
	}

	created, err := t.store.Remember(ctx, mem)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":    "remember",
		"success":   true,
		"memory_id": created.ID,
		"tokens":    created.TokenCount,
		"salience":  created.Salience,
	})
}

func (t *GraphMemoryTool) executeRecall(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	opts := memory.RecallOpts{
		AgentID:     t.agentID,
		Query:       getStr(input, "query"),
		MemoryType:  getStr(input, "memory_type"),
		EntityIDs:   getStrSlice(input, "entity_ids"),
		Tags:        getStrSlice(input, "tags"),
		MinSalience: getFloat(input, "min_salience"),
		MaxTokens:   getInt(input, "max_tokens"),
		Limit:       getInt(input, "limit"),
	}

	memories, err := t.store.Recall(ctx, opts)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	results := make([]map[string]interface{}, len(memories))
	for i, m := range memories {
		results[i] = map[string]interface{}{
			"id":          m.ID,
			"content":     m.Content,
			"summary":     m.Summary,
			"memory_type": m.MemoryType,
			"salience":    m.Salience,
			"tags":        m.Tags,
			"created_at":  m.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return jsonResult(map[string]interface{}{
		"action":  "recall",
		"success": true,
		"count":   len(memories),
		"results": results,
	})
}

func (t *GraphMemoryTool) executeForget(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	memoryID, _ := input["memory_id"].(string)
	if memoryID == "" {
		return errorResult("INVALID_PARAMETER", "memory_id is required for forget"), nil
	}

	if err := t.store.Forget(ctx, memoryID); err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":    "forget",
		"success":   true,
		"memory_id": memoryID,
	})
}

func (t *GraphMemoryTool) executeSupersede(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	oldMemoryID, _ := input["memory_id"].(string)
	if oldMemoryID == "" {
		return errorResult("INVALID_PARAMETER", "memory_id is required for supersede"), nil
	}
	content, _ := input["content"].(string)
	if content == "" {
		return errorResult("INVALID_PARAMETER", "content is required for supersede"), nil
	}

	newMem := &memory.Memory{
		AgentID:        t.agentID,
		Content:        content,
		Summary:        getStr(input, "summary"),
		MemoryType:     memory.MemoryTypeFact,
		Source:         "agent",
		MemoryAgentID:  t.agentID,
		Tags:           getStrSlice(input, "tags"),
		PropertiesJSON: getStr(input, "properties_json"),
	}

	created, err := t.store.Supersede(ctx, oldMemoryID, newMem)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":        "supersede",
		"success":       true,
		"new_memory_id": created.ID,
		"old_memory_id": oldMemoryID,
		"salience":      created.Salience,
	})
}

func (t *GraphMemoryTool) executeConsolidate(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	memoryIDs := getStrSlice(input, "memory_ids")
	if len(memoryIDs) < 2 {
		return errorResult("INVALID_PARAMETER", "at least 2 memory_ids required for consolidate"), nil
	}
	content, _ := input["content"].(string)
	if content == "" {
		return errorResult("INVALID_PARAMETER", "content is required for consolidate"), nil
	}

	consolidated := &memory.Memory{
		AgentID:       t.agentID,
		Content:       content,
		Summary:       getStr(input, "summary"),
		Tags:          getStrSlice(input, "tags"),
		Salience:      getFloat(input, "salience"),
		Source:        "agent",
		MemoryAgentID: t.agentID,
	}

	created, err := t.store.Consolidate(ctx, memoryIDs, consolidated)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":          "consolidate",
		"success":         true,
		"consolidated_id": created.ID,
		"source_count":    len(memoryIDs),
		"salience":        created.Salience,
	})
}

func (t *GraphMemoryTool) executeContextFor(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	entityName, _ := input["entity_name"].(string)
	if entityName == "" {
		return errorResult("INVALID_PARAMETER", "entity_name is required for context_for"), nil
	}

	opts := memory.ContextForOpts{
		AgentID:    t.agentID,
		EntityName: entityName,
		Topic:      getStr(input, "topic"),
		MaxTokens:  getInt(input, "max_tokens"),
	}

	recall, err := t.store.ContextFor(ctx, opts)
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	result := map[string]interface{}{
		"action":           "context_for",
		"success":          true,
		"entity_name":      entityName,
		"total_tokens":     recall.TotalTokensUsed,
		"total_candidates": recall.TotalCandidates,
		"memory_count":     len(recall.Memories),
		"context":          recall.Format(),
	}

	return jsonResult(result)
}

func (t *GraphMemoryTool) executeEntities(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	query := getStr(input, "query")
	entityType := getStr(input, "entity_type")
	limit := getInt(input, "limit")
	if limit <= 0 {
		limit = 20
	}

	var entities []*memory.Entity
	var err error

	if query != "" {
		entities, err = t.store.SearchEntities(ctx, t.agentID, query, limit)
	} else {
		entities, _, err = t.store.ListEntities(ctx, t.agentID, entityType, limit, 0)
	}
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	results := make([]map[string]interface{}, len(entities))
	for i, e := range entities {
		results[i] = map[string]interface{}{
			"id":          e.ID,
			"name":        e.Name,
			"entity_type": e.EntityType,
			"properties":  e.PropertiesJSON,
		}
	}

	return jsonResult(map[string]interface{}{
		"action":  "entities",
		"success": true,
		"count":   len(entities),
		"results": results,
	})
}

func (t *GraphMemoryTool) executeRelate(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
	sourceName := getStr(input, "source_name")
	targetName := getStr(input, "target_name")
	relation := getStr(input, "relation")

	if sourceName == "" || targetName == "" || relation == "" {
		return errorResult("INVALID_PARAMETER", "source_name, target_name, and relation are required for relate"), nil
	}

	// Look up entities by name.
	source, err := t.store.GetEntity(ctx, t.agentID, sourceName)
	if err != nil {
		// Auto-create source entity.
		source, err = t.store.CreateEntity(ctx, &memory.Entity{
			AgentID: t.agentID, Name: sourceName, EntityType: "concept",
		})
		if err != nil {
			return errorResult("STORE_ERROR", "failed to create source entity: "+err.Error()), nil
		}
	}

	target, err := t.store.GetEntity(ctx, t.agentID, targetName)
	if err != nil {
		// Auto-create target entity.
		target, err = t.store.CreateEntity(ctx, &memory.Entity{
			AgentID: t.agentID, Name: targetName, EntityType: "concept",
		})
		if err != nil {
			return errorResult("STORE_ERROR", "failed to create target entity: "+err.Error()), nil
		}
	}

	edge, err := t.store.Relate(ctx, &memory.Edge{
		AgentID:  t.agentID,
		SourceID: source.ID,
		TargetID: target.ID,
		Relation: relation,
	})
	if err != nil {
		return errorResult("STORE_ERROR", err.Error()), nil
	}

	return jsonResult(map[string]interface{}{
		"action":      "relate",
		"success":     true,
		"edge_id":     edge.ID,
		"source_name": sourceName,
		"relation":    relation,
		"target_name": targetName,
	})
}

// =============================================================================
// Helpers
// =============================================================================

func errorResult(code, message string) *shuttle.Result {
	return &shuttle.Result{
		Success: false,
		Error:   &shuttle.Error{Code: code, Message: message},
	}
}

func jsonResult(data map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: true,
		Data:    data,
	}, nil
}

func getStr(input map[string]interface{}, key string) string {
	v, _ := input[key].(string)
	return v
}

func getFloat(input map[string]interface{}, key string) float64 {
	switch v := input[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func getInt(input map[string]interface{}, key string) int {
	switch v := input[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

func getStrSlice(input map[string]interface{}, key string) []string {
	raw, ok := input[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// Compile-time check.
var _ shuttle.Tool = (*GraphMemoryTool)(nil)
