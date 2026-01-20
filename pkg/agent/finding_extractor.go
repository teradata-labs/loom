// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/types"
)

// ExtractedFinding represents a finding extracted by the LLM from tool results.
type ExtractedFinding struct {
	Path     string      `json:"path"`
	Value    interface{} `json:"value"`
	Category string      `json:"category"`
	Note     string      `json:"note"`
}

// buildExtractionPrompt creates a prompt for the LLM to extract findings from tool results.
func buildExtractionPrompt(messages []types.Message) string {
	var sb strings.Builder
	sb.WriteString("Given these tool execution results, extract structured findings.\n\n")
	sb.WriteString("Tool Results:\n")

	for i, msg := range messages {
		if msg.Role == "tool" {
			// Extract a preview of the tool result
			preview := msg.Content
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("%d. Tool result: %s\n", i+1, preview))
		}
	}

	sb.WriteString("\n")
	sb.WriteString("Extract findings as JSON array:\n")
	sb.WriteString("[\n")
	sb.WriteString("  {\n")
	sb.WriteString("    \"path\": \"table_name.metric\",\n")
	sb.WriteString("    \"value\": <any JSON value>,\n")
	sb.WriteString("    \"category\": \"statistic|schema|observation|distribution\",\n")
	sb.WriteString("    \"note\": \"Brief explanation\"\n")
	sb.WriteString("  }\n")
	sb.WriteString("]\n\n")
	sb.WriteString("Categories:\n")
	sb.WriteString("- statistic: Numeric measurements (row counts, null rates, averages)\n")
	sb.WriteString("- schema: Column names, data types, table structures\n")
	sb.WriteString("- observation: Patterns, anomalies, uniqueness, constraints\n")
	sb.WriteString("- distribution: Value distributions, frequencies, ranges\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Only extract verified, evidence-based findings\n")
	sb.WriteString("- Skip speculative insights\n")
	sb.WriteString("- Use hierarchical paths (e.g., 'database.table.column.metric')\n")
	sb.WriteString("- Keep notes concise (under 50 characters)\n\n")
	sb.WriteString("Return ONLY the JSON array, no explanation.\n")

	return sb.String()
}

// extractFindingsAsync extracts findings from recent tool results in the background.
// This function is called asynchronously after N tool executions (cadence).
func (a *Agent) extractFindingsAsync(ctx context.Context, sessionID string) {
	// Skip if extraction is disabled
	if !a.enableFindingExtraction {
		return
	}

	// Create a timeout context for extraction (5 seconds max)
	extractCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get the session to access recent messages
	session, ok := a.memory.GetSession(sessionID)
	if !ok {
		// Session not found, skip extraction
		return
	}

	// Get recent tool results (last N messages)
	cadence := a.extractionCadence
	if cadence <= 0 {
		cadence = 3 // Default to 3 if not configured
	}

	// Cast SegmentedMem to *SegmentedMemory
	segmentedMem, ok := session.SegmentedMem.(*SegmentedMemory)
	if !ok || segmentedMem == nil {
		// No segmented memory, skip extraction
		return
	}

	recentMessages := segmentedMem.GetRecentToolResults(cadence)
	if len(recentMessages) == 0 {
		// No tool results to extract from
		return
	}

	// Build extraction prompt
	prompt := buildExtractionPrompt(recentMessages)

	// Call LLM to extract findings
	messages := []types.Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}

	response, err := a.llm.Chat(extractCtx, messages, nil)
	if err != nil {
		// Silently fail - extraction is best-effort
		return
	}

	// Parse JSON response
	content := response.Content
	// Remove markdown code blocks if present
	content = strings.TrimPrefix(content, "```json\n")
	content = strings.TrimPrefix(content, "```\n")
	content = strings.TrimSuffix(content, "\n```")
	content = strings.TrimSpace(content)

	var findings []ExtractedFinding
	if err := json.Unmarshal([]byte(content), &findings); err != nil {
		// Silently fail - extraction is best-effort
		return
	}

	// Add findings to session's memory
	for _, finding := range findings {
		segmentedMem.RecordFinding(finding.Path, finding.Value, finding.Category, finding.Note, "auto_extracted")
	}
}

// GetRecentToolResults retrieves the last N tool result messages from L1 cache.
func (sm *SegmentedMemory) GetRecentToolResults(n int) []types.Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var toolMessages []types.Message
	count := 0

	// Iterate backwards through L1 to get most recent tool results
	for i := len(sm.l1Messages) - 1; i >= 0 && count < n; i-- {
		msg := sm.l1Messages[i]
		if msg.Role == "tool" {
			toolMessages = append([]types.Message{msg}, toolMessages...) // Prepend to maintain order
			count++
		}
	}

	return toolMessages
}
