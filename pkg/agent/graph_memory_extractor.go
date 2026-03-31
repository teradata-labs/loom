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
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/types"
)

// ExtractedGraphData is the top-level JSON response from the LLM extraction.
type ExtractedGraphData struct {
	Entities      []ExtractedEntity       `json:"entities"`
	Relationships []ExtractedRelationship `json:"relationships"`
	Memories      []ExtractedMemory       `json:"memories"`
}

// ExtractedEntity represents an entity extracted from conversation context.
type ExtractedEntity struct {
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	Properties string `json:"properties"`
	IsUser     bool   `json:"is_user"` // true if this person entity IS the human speaking
}

// ExtractedEntityRole pairs an entity name with its role in a memory.
type ExtractedEntityRole struct {
	Name string `json:"name"`
	Role string `json:"role"` // "about" = primary subject, "mentions" = referenced
}

// ExtractedRelationship represents a relationship between two entities.
type ExtractedRelationship struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Relation string `json:"relation"`
}

// ExtractedMemory represents a memory worth remembering.
type ExtractedMemory struct {
	Content    string                `json:"content"`
	Summary    string                `json:"summary"`
	MemoryType string                `json:"memory_type"`
	Tags       []string              `json:"tags"`
	Salience   float64               `json:"salience"`
	Entities   []ExtractedEntityRole `json:"entities"`
}

// buildGraphMemoryExtractionPrompt creates a prompt for the LLM to extract
// entities, relationships, and memories from recent conversation turns.
func buildGraphMemoryExtractionPrompt(messages []types.Message, maxEntities int) string {
	var sb strings.Builder
	sb.WriteString("Extract entities, relationships, and memories from this conversation for a knowledge graph.\n\n")
	sb.WriteString("Conversation:\n")

	for i, msg := range messages {
		preview := msg.Content
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		role := msg.Role
		if role == "tool" && len(msg.ToolCalls) > 0 {
			role = "tool:" + msg.ToolCalls[0].Name
		}
		sb.WriteString(fmt.Sprintf("%d. [%s]: %s\n", i+1, role, preview))
	}

	sb.WriteString(fmt.Sprintf("\nExtract up to %d entities. Return a single JSON object:\n", maxEntities))
	sb.WriteString(`{
  "entities": [{"name": "lowercase_name", "entity_type": "person|tool|project|concept|organization|dataset|system", "properties": "{}", "is_user": false}],
  "relationships": [{"source": "entity_name", "target": "entity_name", "relation": "USES|WORKS_ON|KNOWS_ABOUT|CREATED|DEPENDS_ON|RELATED_TO|CONTAINS|PRODUCES"}],
  "memories": [{"content": "factual statement", "summary": "short summary", "memory_type": "fact|preference|decision|experience|failure|observation", "tags": ["tag1"], "salience": 0.5, "entities": [{"name": "entity_name", "role": "about|mentions"}]}]
}

Rules:
- Only extract information explicitly stated or clearly implied in the conversation
- Normalize entity names to lowercase
- Keep memory content concise and factual (one sentence)
- Keep summary under 50 characters
- Set salience proportional to importance: critical decisions 0.8-1.0, routine facts 0.3-0.5
- Skip redundant or trivial information
- Return ONLY the JSON object, no explanation
- If nothing worth extracting, return {"entities":[],"relationships":[],"memories":[]}

Entity roles:
- Each memory entity has a role: "about" = the entity is the primary subject of the memory, "mentions" = the entity is referenced but not the subject
- Messages from [user] are from the human speaking. If they reveal their identity ("I am X", "my name is X"), mark that person entity with "is_user": true
- People the user references ("my colleague Y", "Y told me") are separate entities with "is_user": false
- Non-person entities (datasets, tools, systems) always have "is_user": false
- A memory like "Ilsun works on Team Phoenix" has ilsun as "about" and team_phoenix as "mentions"
- A memory like "Marcus focuses on fraud detection" has marcus as "about", not the user
`)

	return sb.String()
}

// extractGraphMemoryAsync extracts entities, relationships, and memories from
// recent conversation turns and stores them in graph memory. This function is
// called asynchronously after N tool executions (cadence).
func (a *Agent) extractGraphMemoryAsync(ctx context.Context, sessionID string) {
	if !a.enableGraphMemoryExtraction || a.graphMemoryStore == nil {
		return
	}

	// Create a timeout context for extraction (5 seconds max).
	// Derive from caller's context to propagate RLS user_id and other values.
	extractCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	session, ok := a.memory.GetSession(sessionID)
	if !ok {
		return
	}

	segmentedMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok || segmentedMem == nil {
		return
	}

	// Get recent conversation turns (all roles, not just tool results).
	// Use cadence * 3 to cover roughly cadence exchanges of user/assistant/tool.
	messageCount := a.graphExtractionCadence * 3
	if messageCount <= 0 {
		messageCount = 15
	}
	recentMessages := segmentedMem.GetRecentConversationTurns(messageCount)
	if len(recentMessages) == 0 {
		return
	}

	// Determine max entities from config.
	maxEntities := 10
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.MaxEntitiesPerExtraction > 0 {
		maxEntities = int(a.graphMemoryConfig.MaxEntitiesPerExtraction)
	}

	prompt := buildGraphMemoryExtractionPrompt(recentMessages, maxEntities)

	messages := []types.Message{
		{Role: "user", Content: prompt},
	}

	// Use compressorLLM for extraction (cheaper/smaller model), fall back to main LLM.
	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}

	response, err := llmProvider.Chat(extractCtx, messages, nil)
	if err != nil {
		return
	}

	// Parse JSON response.
	content := response.Content
	content = strings.TrimPrefix(content, "```json\n")
	content = strings.TrimPrefix(content, "```\n")
	content = strings.TrimSuffix(content, "\n```")
	content = strings.TrimSpace(content)

	var data ExtractedGraphData
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return
	}

	agentID := a.config.Name

	// Store entities (get-or-create pattern).
	entityMap := make(map[string]*memory.Entity) // name → entity for relationship resolution
	for _, e := range data.Entities {
		name := normalizeEntityName(e.Name)
		if name == "" {
			continue
		}
		entityType := e.EntityType
		if entityType == "" {
			entityType = "concept"
		}
		// Set properties for user-identity entities.
		props := e.Properties
		if e.IsUser {
			props = `{"is_user":true}`
		}
		entity, err := a.getOrCreateEntity(extractCtx, agentID, name, entityType, props)
		if err != nil {
			continue
		}
		entityMap[name] = entity
	}

	// Store relationships.
	for _, r := range data.Relationships {
		sourceName := normalizeEntityName(r.Source)
		targetName := normalizeEntityName(r.Target)
		relation := strings.ToUpper(strings.TrimSpace(r.Relation))
		if sourceName == "" || targetName == "" || relation == "" {
			continue
		}

		source, ok := entityMap[sourceName]
		if !ok {
			// Entity not extracted this cycle; try to get-or-create.
			var err error
			source, err = a.getOrCreateEntity(extractCtx, agentID, sourceName, "concept", "")
			if err != nil {
				continue
			}
			entityMap[sourceName] = source
		}

		target, ok := entityMap[targetName]
		if !ok {
			var err error
			target, err = a.getOrCreateEntity(extractCtx, agentID, targetName, "concept", "")
			if err != nil {
				continue
			}
			entityMap[targetName] = target
		}

		_, err := a.graphMemoryStore.Relate(extractCtx, &memory.Edge{
			AgentID:  agentID,
			SourceID: source.ID,
			TargetID: target.ID,
			Relation: relation,
		})
		if err != nil {
			zap.L().Debug("graph memory extraction: failed to create relationship",
				zap.String("source", sourceName),
				zap.String("target", targetName),
				zap.String("relation", relation),
				zap.Error(err))
		}
	}

	// Store memories.
	for _, m := range data.Memories {
		if m.Content == "" {
			continue
		}
		memoryType := m.MemoryType
		if !isValidMemoryType(memoryType) {
			memoryType = memory.MemoryTypeFact
		}
		salience := m.Salience
		if salience <= 0 || salience > 1 {
			salience = 0.5
		}

		// Resolve entity names to IDs with roles.
		var entityRoles []memory.EntityIDRole
		for _, er := range m.Entities {
			name := normalizeEntityName(er.Name)
			if e, ok := entityMap[name]; ok {
				role := er.Role
				if role != memory.RoleAbout && role != memory.RoleMentions &&
					role != memory.RoleBy && role != memory.RoleFor {
					role = memory.RoleAbout
				}
				entityRoles = append(entityRoles, memory.EntityIDRole{
					ID:   e.ID,
					Role: role,
				})
			}
		}

		mem := &memory.Memory{
			AgentID:       agentID,
			Content:       m.Content,
			Summary:       m.Summary,
			MemoryType:    memoryType,
			Source:        "auto_extracted",
			MemoryAgentID: agentID,
			Tags:          m.Tags,
			Salience:      salience,
			EntityRoles:   entityRoles,
		}
		_, err := a.graphMemoryStore.Remember(extractCtx, mem)
		if err != nil {
			zap.L().Debug("graph memory extraction: failed to store memory",
				zap.String("content_preview", truncate(m.Content, 80)),
				zap.Error(err))
		}
	}
}

// getOrCreateEntity retrieves an entity by name, creating it if it doesn't exist.
// If propertiesJSON is non-empty and the entity already exists, it updates properties
// (used to mark is_user on existing entities).
func (a *Agent) getOrCreateEntity(ctx context.Context, agentID, name, entityType, propertiesJSON string) (*memory.Entity, error) {
	entity, err := a.graphMemoryStore.GetEntity(ctx, agentID, name)
	if err == nil {
		// If is_user property needs to be set on an existing entity, update it.
		if propertiesJSON != "" && propertiesJSON != "{}" && entity.PropertiesJSON != propertiesJSON {
			entity.PropertiesJSON = propertiesJSON
			if updated, err := a.graphMemoryStore.UpdateEntity(ctx, entity); err == nil {
				return updated, nil
			}
		}
		return entity, nil
	}

	// Entity doesn't exist, create it.
	entity, err = a.graphMemoryStore.CreateEntity(ctx, &memory.Entity{
		AgentID:        agentID,
		Name:           name,
		EntityType:     entityType,
		PropertiesJSON: propertiesJSON,
	})
	if err != nil {
		// Possible race: another goroutine created it between our Get and Create.
		// Retry the Get.
		entity, err2 := a.graphMemoryStore.GetEntity(ctx, agentID, name)
		if err2 == nil {
			return entity, nil
		}
		return nil, fmt.Errorf("create entity %q: %w", name, err)
	}

	return entity, nil
}

// normalizeEntityName lowercases and trims entity names for consistent deduplication.
func normalizeEntityName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// isValidMemoryType checks if a memory type is one of the known types.
func isValidMemoryType(t string) bool {
	switch t {
	case memory.MemoryTypeFact,
		memory.MemoryTypePreference,
		memory.MemoryTypeDecision,
		memory.MemoryTypeExperience,
		memory.MemoryTypeFailure,
		memory.MemoryTypeObservation,
		memory.MemoryTypeConsolidation:
		return true
	}
	return false
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
