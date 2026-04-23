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
	"regexp"
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
	// EventDate is the absolute ISO date (YYYY-MM-DD) for when the fact
	// described by this memory occurred. Produced by anchoring any relative
	// time phrase in the conversation against the current_date the extractor
	// was given. Empty when no temporal cue is present or when the cue cannot
	// be resolved to a specific date.
	EventDate string `json:"event_date,omitempty"`
	// EventDateConfidence is "exact" | "approximate" | "ambiguous" | "".
	// "ambiguous" is reserved for cases where the extractor saw a time cue
	// like "a while back" and declined to fabricate a date.
	EventDateConfidence string `json:"event_date_confidence,omitempty"`
}

// extractionContext holds shared context passed to both extraction passes.
type extractionContext struct {
	messages         []types.Message
	maxEntities      int
	l2Summaries      []string
	existingEntities []string
	currentDate      string // ISO date for anchoring relative references
}

// buildConversationBlock renders the shared conversation + context sections
// used by both extraction passes.
func buildConversationBlock(ec extractionContext) string {
	var sb strings.Builder

	if ec.currentDate != "" {
		fmt.Fprintf(&sb, "Current date: %s\n\n", ec.currentDate)
	}

	if len(ec.l2Summaries) > 0 {
		sb.WriteString("Previous conversation context (compressed summaries of earlier exchanges):\n")
		for i, summary := range ec.l2Summaries {
			fmt.Fprintf(&sb, "  [Summary %d]: %s\n", i+1, summary)
		}
		sb.WriteString("\n")
	}

	if len(ec.existingEntities) > 0 {
		sb.WriteString("Existing entities in the knowledge graph (reuse these names when referencing the same thing):\n")
		sb.WriteString("  " + strings.Join(ec.existingEntities, ", ") + "\n\n")
	}

	sb.WriteString("Conversation:\n")
	for i, msg := range ec.messages {
		if msg.Role == "tool" {
			continue
		}
		fmt.Fprintf(&sb, "%d. [%s]: %s\n", i+1, msg.Role, msg.Content)
	}

	return sb.String()
}

// jsonSchema is the shared output schema for both extraction passes.
const jsonSchema = `{
  "entities": [{"name": "lowercase_name", "entity_type": "person|tool|project|concept|organization|dataset|system|event|place", "properties": "{}", "is_user": false}],
  "relationships": [{"source": "entity_name", "target": "entity_name", "relation": "USES|WORKS_ON|KNOWS_ABOUT|CREATED|DEPENDS_ON|RELATED_TO|CONTAINS|PRODUCES|ATTENDED|PURCHASED|VISITED|MEMBER_OF"}],
  "memories": [{"content": "factual statement", "summary": "short summary", "memory_type": "fact|preference|decision|experience|failure|observation", "tags": ["tag1"], "salience": 0.5, "entities": [{"name": "entity_name", "role": "about|mentions"}], "event_date": "YYYY-MM-DD or empty", "event_date_confidence": "exact|approximate|ambiguous or empty"}]
}`

// buildGraphMemoryExtractionPrompt creates PASS 1: main topic extraction.
// Extracts entities, relationships, and memories from the primary conversation content.
func buildGraphMemoryExtractionPrompt(messages []types.Message, maxEntities int, l2Summaries []string, existingEntities []string) string {
	return buildGraphMemoryExtractionPromptWithDate(messages, maxEntities, l2Summaries, existingEntities, "")
}

// buildGraphMemoryExtractionPromptWithDate creates PASS 1 with date anchoring.
func buildGraphMemoryExtractionPromptWithDate(messages []types.Message, maxEntities int, l2Summaries []string, existingEntities []string, currentDate string) string {
	ec := extractionContext{
		messages:         messages,
		maxEntities:      maxEntities,
		l2Summaries:      l2Summaries,
		existingEntities: existingEntities,
		currentDate:      currentDate,
	}

	var sb strings.Builder
	sb.WriteString("Extract entities, relationships, and memories from this conversation for a knowledge graph.\n\n")
	sb.WriteString("IMPORTANT: Focus on factual content from the USER's messages — names, dates, events, ")
	sb.WriteString("preferences, and real-world facts. IGNORE the assistant's process notes, tool usage ")
	sb.WriteString("descriptions, error messages, and self-referential observations about its own behavior.\n\n")

	sb.WriteString(buildConversationBlock(ec))

	fmt.Fprintf(&sb, "\nExtract up to %d entities. Return a single JSON object:\n", maxEntities)
	sb.WriteString(jsonSchema)
	sb.WriteString(`

Rules:
- Only extract information explicitly stated or clearly implied in the conversation
- Normalize entity names to lowercase
- Keep memory content concise and factual (one sentence)
- Keep summary under 50 characters
- Set salience proportional to importance: critical decisions 0.8-1.0, routine facts 0.3-0.5
- CRITICAL: Convert ALL relative dates to absolute dates using the current date as anchor.
  "last week" → specific date. "three weeks ago" → specific date. "yesterday" → specific date.
  If current date is not provided, keep the relative reference but note the context.
- For each memory describing a time-bound event or state change, also populate
  "event_date" with the absolute ISO date (YYYY-MM-DD) computed by subtracting
  the relative phrase from the current date, and set "event_date_confidence":
    * "exact" when the user gave a specific date ("on January 15th")
    * "approximate" when the user gave a relative phrase you anchored ("about two months ago")
    * "ambiguous" when the user mentioned a time cue you cannot resolve
      ("a while back", "recently", "at some point") — in this case leave
      "event_date" empty. Do NOT fabricate a date.
  If the memory has no temporal dimension at all, leave both fields empty.
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

// buildIncidentalFactsPrompt creates PASS 2: personal context extraction.
// While PASS 1 extracts what the conversation is about, PASS 2 extracts what
// the user revealed about themselves — biographical facts, personal timeline,
// relationships, habits, and preferences that build a long-term user model.
func buildIncidentalFactsPrompt(ec extractionContext) string {
	var sb strings.Builder
	sb.WriteString(`Extract what the USER revealed about themselves in this conversation.

PASS 1 already captured the main discussion topic. Your job is to capture the user's personal context:

- Timeline: when things happened, how long ago, durations, sequences of events
- Relationships: people mentioned, their roles (colleague, friend, family, etc.)
- Habits and routines: schedules, recurring activities, regular practices
- Preferences: likes, dislikes, choices, opinions expressed
- Affiliations: groups, clubs, teams, organizations, employers
- Possessions: things owned, purchased, subscribed to
- Experiences: places visited, events attended, activities participated in

Each fact should be a standalone statement that would be useful if recalled months later.

`)

	sb.WriteString(buildConversationBlock(ec))

	sb.WriteString("\nReturn a single JSON object with the same schema:\n")
	sb.WriteString(jsonSchema)
	sb.WriteString(`

Rules:
- Extract facts about the USER, not about the topic being discussed
- Convert ALL relative dates to absolute dates using the current date as anchor
- For each memory with a time dimension, populate "event_date" (YYYY-MM-DD)
  and "event_date_confidence" ("exact" | "approximate" | "ambiguous").
  If the time cue is too vague to anchor (e.g., "a while back"), leave
  "event_date" empty and set confidence to "ambiguous" — do NOT fabricate.
- Each memory should be a single specific fact (not a summary of the conversation)
- Set salience 0.6-0.9 for personal facts
- Return ONLY the JSON object, no explanation
- If nothing about the user was revealed, return {"entities":[],"relationships":[],"memories":[]}
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

	// Create a timeout context for extraction.
	// Derive from caller's context to propagate RLS user_id and other values.
	timeout := 30 * time.Second // default
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.ExtractionTimeoutSeconds > 0 {
		timeout = time.Duration(a.graphMemoryConfig.ExtractionTimeoutSeconds) * time.Second
	}
	extractCtx, cancel := context.WithTimeout(ctx, timeout)
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
	// Use configured window size, or fall back to cadence * 3 with a minimum of 15.
	messageCount := 15
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.ExtractionWindowMessages > 0 {
		messageCount = int(a.graphMemoryConfig.ExtractionWindowMessages)
	} else if a.graphExtractionCadence*3 > messageCount {
		messageCount = a.graphExtractionCadence * 3
	}
	recentMessages := segmentedMem.GetRecentConversationTurns(messageCount)
	if len(recentMessages) == 0 {
		return
	}

	agentID := a.config.Name

	// Determine max entities from config.
	maxEntities := 10
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.MaxEntitiesPerExtraction > 0 {
		maxEntities = int(a.graphMemoryConfig.MaxEntitiesPerExtraction)
	}

	// Gather L2 summaries for narrative context.
	var l2Summaries []string
	if currentL2 := segmentedMem.GetL2Summary(); currentL2 != "" {
		if snapshots, err := segmentedMem.RetrieveL2Snapshots(extractCtx, 3); err == nil && len(snapshots) > 0 {
			l2Summaries = append(l2Summaries, snapshots...)
		}
		l2Summaries = append(l2Summaries, currentL2)
	}

	// Gather existing entity names so the extractor can link to known nodes.
	var existingEntities []string
	if entities, _, err := a.graphMemoryStore.ListEntities(extractCtx, agentID, "", 50, 0); err == nil {
		for _, e := range entities {
			existingEntities = append(existingEntities, e.Name)
		}
	}

	// Determine the current date for anchoring relative time references.
	// Try to extract from conversation content (e.g., "session that took place on 2023/05/24"),
	// fall back to the current wall clock time.
	currentDate := extractDateFromMessages(recentMessages)
	if currentDate == "" {
		currentDate = time.Now().Format("2006-01-02")
	}

	// Use compressorLLM for extraction (cheaper/smaller model), fall back to main LLM.
	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}

	// Single-pass extraction with date anchoring.
	prompt := buildGraphMemoryExtractionPromptWithDate(
		recentMessages, maxEntities, l2Summaries, existingEntities, currentDate)

	response, err := llmProvider.Chat(extractCtx, []types.Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return
	}

	data, ok := parseExtractionResponse(response.Content)
	if !ok {
		return
	}

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

	// Batch-embed memory contents if embedder is available.
	var embeddings [][]float32
	if a.embedder != nil && len(data.Memories) > 0 {
		var texts []string
		for _, m := range data.Memories {
			if m.Content != "" {
				texts = append(texts, m.Content)
			}
		}
		if len(texts) > 0 {
			embs, err := a.embedder.EmbedBatch(extractCtx, texts)
			if err == nil && len(embs) == len(texts) {
				embeddings = embs
			}
		}
	}

	// Store memories.
	embIdx := 0
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

		eventDate, eventConfidence := sanitizeEventDate(m.EventDate, m.EventDateConfidence)

		mem := &memory.Memory{
			AgentID:             agentID,
			Content:             m.Content,
			Summary:             m.Summary,
			MemoryType:          memoryType,
			Source:              "auto_extracted",
			MemoryAgentID:       agentID,
			Tags:                m.Tags,
			Salience:            salience,
			EntityRoles:         entityRoles,
			EventDate:           eventDate,
			EventDateConfidence: eventConfidence,
		}
		// Attach embedding if available.
		if embIdx < len(embeddings) {
			mem.Embedding = embeddings[embIdx]
			mem.EmbeddingModel = a.embedder.Model()
			embIdx++
		}

		_, err := a.graphMemoryStore.Remember(extractCtx, mem)
		if err != nil {
			zap.L().Debug("graph memory extraction: failed to store memory",
				zap.String("content_preview", truncate(m.Content, 80)),
				zap.Error(err))
		}
	}
}

// parseExtractionResponse parses an LLM response into ExtractedGraphData.
// Handles JSON wrapped in markdown code fences.
func parseExtractionResponse(content string) (ExtractedGraphData, bool) {
	content = strings.TrimPrefix(content, "```json\n")
	content = strings.TrimPrefix(content, "```\n")
	content = strings.TrimSuffix(content, "\n```")
	content = strings.TrimSpace(content)

	var data ExtractedGraphData
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return data, false
	}
	return data, true
}

// extractDateFromMessages scans recent messages for date context.
// Looks for patterns like "took place on 2023/05/24" or "Current date: 2023-05-24"
// that the benchmark runner or user might include.
func extractDateFromMessages(messages []types.Message) string {
	for _, msg := range messages {
		// Try common date patterns in message content.
		for _, pattern := range datePatterns {
			if loc := pattern.FindStringIndex(msg.Content); loc != nil {
				match := pattern.FindString(msg.Content)
				// Normalize separators to dash format.
				match = strings.ReplaceAll(match, "/", "-")
				return match
			}
		}
	}
	return ""
}

// datePatterns matches common date formats in conversation messages.
var datePatterns = func() []*regexp.Regexp {
	patterns := []string{
		`\d{4}[/-]\d{2}[/-]\d{2}`, // 2023-05-24 or 2023/05/24
	}
	compiled := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		compiled[i] = regexp.MustCompile(p)
	}
	return compiled
}()

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

// sanitizeEventDate validates the (date, confidence) pair the extractor
// returned and normalizes it for storage.
//
// A date is only accepted if it parses as YYYY-MM-DD. A non-empty date with
// confidence "ambiguous" is treated as a contract violation and the date is
// dropped (the prompt forbids this combination). An empty date is always
// allowed; confidence in that case collapses to "ambiguous" or empty
// depending on what the extractor emitted.
func sanitizeEventDate(date, confidence string) (string, string) {
	date = strings.TrimSpace(date)
	confidence = strings.ToLower(strings.TrimSpace(confidence))

	switch confidence {
	case "exact", "approximate", "ambiguous", "":
		// allowed
	default:
		confidence = ""
	}

	if date == "" {
		return "", confidence
	}

	if _, err := time.Parse("2006-01-02", date); err != nil {
		// Drop malformed dates; keep confidence so telemetry can flag this.
		return "", confidence
	}

	if confidence == "ambiguous" {
		// Protocol violation: the extractor emitted a date despite saying it
		// could not resolve one. Prefer trusting the self-reported confidence
		// and drop the date.
		return "", confidence
	}

	if confidence == "" {
		confidence = "approximate"
	}
	return date, confidence
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
