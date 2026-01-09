// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
	"time"
)

// StructuredContext represents the accumulated context across workflow stages.
// This prevents agent hallucinations by enforcing structured data passing.
type StructuredContext struct {
	WorkflowContext ContextMetadata        `json:"workflow_context"`
	StageOutputs    map[string]StageOutput `json:"stage_outputs"`
}

// ContextMetadata contains global workflow runtime information
type ContextMetadata struct {
	WorkflowID   string    `json:"workflow_id"`
	WorkflowType string    `json:"workflow_type"`
	SchemaVer    string    `json:"schema_version"`
	StartedAt    time.Time `json:"started_at"`
}

// StageOutput represents the output from a single workflow stage
type StageOutput struct {
	StageID     string                 `json:"stage_id"`
	Status      string                 `json:"status"` // "completed", "failed", "skipped"
	StartedAt   time.Time              `json:"started_at"`
	CompletedAt time.Time              `json:"completed_at"`
	Inputs      map[string]interface{} `json:"inputs"`
	Outputs     map[string]interface{} `json:"outputs"`
	Evidence    Evidence               `json:"evidence"`
}

// Evidence provides proof of how outputs were derived
type Evidence struct {
	ToolCalls       []ToolCall `json:"tool_calls"`
	QueriesExecuted []string   `json:"queries_executed"`
}

// ToolCall records a single tool execution
type ToolCall struct {
	ToolName      string                 `json:"tool_name"`
	Parameters    map[string]interface{} `json:"parameters"`
	ResultSummary string                 `json:"result_summary"`
}

// NewStructuredContext creates a new structured context for a workflow
func NewStructuredContext(workflowID, workflowType string) *StructuredContext {
	return &StructuredContext{
		WorkflowContext: ContextMetadata{
			WorkflowID:   workflowID,
			WorkflowType: workflowType,
			SchemaVer:    "1.0",
			StartedAt:    time.Now(),
		},
		StageOutputs: make(map[string]StageOutput),
	}
}

// AddStageOutput adds an output from a completed stage
func (ctx *StructuredContext) AddStageOutput(stageKey string, output StageOutput) error {
	if output.StageID == "" {
		return fmt.Errorf("stage_id is required")
	}
	if output.Status == "" {
		return fmt.Errorf("status is required")
	}

	ctx.StageOutputs[stageKey] = output
	return nil
}

// GetStageOutput retrieves output from a specific stage
func (ctx *StructuredContext) GetStageOutput(stageKey string) (StageOutput, bool) {
	output, exists := ctx.StageOutputs[stageKey]
	return output, exists
}

// ValidateTableReference checks if a table reference exists in a previous stage
// This prevents agents from hallucinating non-existent tables
func (ctx *StructuredContext) ValidateTableReference(
	currentStageKey string,
	database string,
	table string,
	sourceStageKey string,
) error {
	// Get the source stage output
	sourceOutput, exists := ctx.StageOutputs[sourceStageKey]
	if !exists {
		return fmt.Errorf("source stage '%s' not found in context", sourceStageKey)
	}

	// Check if the stage completed successfully
	if sourceOutput.Status != "completed" {
		return fmt.Errorf("source stage '%s' did not complete successfully (status: %s)",
			sourceStageKey, sourceOutput.Status)
	}

	// Navigate to recommended_table in outputs
	outputs := sourceOutput.Outputs
	if outputs == nil {
		return fmt.Errorf("source stage '%s' has no outputs", sourceStageKey)
	}

	// Extract recommended_table
	recommendedTableRaw, ok := outputs["recommended_table"]
	if !ok {
		return fmt.Errorf("source stage '%s' has no 'recommended_table' in outputs", sourceStageKey)
	}

	recommendedTable, ok := recommendedTableRaw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("recommended_table is not a valid object")
	}

	// Validate database matches
	sourceDB, ok := recommendedTable["database"].(string)
	if !ok {
		return fmt.Errorf("recommended_table.database is missing or not a string")
	}
	if sourceDB != database {
		return fmt.Errorf(
			"database mismatch: current stage references '%s' but source stage '%s' recommended '%s'",
			database, sourceStageKey, sourceDB,
		)
	}

	// Validate table matches
	sourceTable, ok := recommendedTable["table"].(string)
	if !ok {
		return fmt.Errorf("recommended_table.table is missing or not a string")
	}
	if sourceTable != table {
		return fmt.Errorf(
			"table mismatch: current stage references '%s' but source stage '%s' recommended '%s'",
			table, sourceStageKey, sourceTable,
		)
	}

	return nil
}

// ValidateDatabaseList checks if a database exists in the discovery stage
func (ctx *StructuredContext) ValidateDatabaseList(
	database string,
	sourceStageKey string,
) error {
	sourceOutput, exists := ctx.StageOutputs[sourceStageKey]
	if !exists {
		return fmt.Errorf("source stage '%s' not found in context", sourceStageKey)
	}

	if sourceOutput.Status != "completed" {
		return fmt.Errorf("source stage '%s' did not complete successfully", sourceStageKey)
	}

	outputs := sourceOutput.Outputs
	if outputs == nil {
		return fmt.Errorf("source stage '%s' has no outputs", sourceStageKey)
	}

	// Extract discovered_databases list
	databasesRaw, ok := outputs["discovered_databases"]
	if !ok {
		return fmt.Errorf("source stage '%s' has no 'discovered_databases' in outputs", sourceStageKey)
	}

	databasesList, ok := databasesRaw.([]interface{})
	if !ok {
		return fmt.Errorf("discovered_databases is not a valid array")
	}

	// Check if the database exists in the list
	for _, dbRaw := range databasesList {
		dbName, ok := dbRaw.(string)
		if !ok {
			continue
		}
		if dbName == database {
			return nil // Found it!
		}
	}

	return fmt.Errorf(
		"database '%s' not found in source stage '%s' discovered_databases list",
		database, sourceStageKey,
	)
}

// ToJSON serializes the context to JSON for injection into agent prompts
func (ctx *StructuredContext) ToJSON() (string, error) {
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal context to JSON: %w", err)
	}
	return string(data), nil
}

// FromJSON deserializes context from JSON (for parsing agent outputs)
func (ctx *StructuredContext) FromJSON(jsonStr string) error {
	if err := json.Unmarshal([]byte(jsonStr), ctx); err != nil {
		return fmt.Errorf("failed to unmarshal context from JSON: %w", err)
	}
	return nil
}

// GetTargetTable extracts the recommended table from a specific stage (typically stage-2)
func (ctx *StructuredContext) GetTargetTable(sourceStageKey string) (database, table string, err error) {
	sourceOutput, exists := ctx.StageOutputs[sourceStageKey]
	if !exists {
		return "", "", fmt.Errorf("source stage '%s' not found", sourceStageKey)
	}

	outputs := sourceOutput.Outputs
	if outputs == nil {
		return "", "", fmt.Errorf("source stage '%s' has no outputs", sourceStageKey)
	}

	recommendedTableRaw, ok := outputs["recommended_table"]
	if !ok {
		return "", "", fmt.Errorf("source stage '%s' has no 'recommended_table'", sourceStageKey)
	}

	recommendedTable, ok := recommendedTableRaw.(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("recommended_table is not a valid object")
	}

	database, ok = recommendedTable["database"].(string)
	if !ok {
		return "", "", fmt.Errorf("recommended_table.database is missing")
	}

	table, ok = recommendedTable["table"].(string)
	if !ok {
		return "", "", fmt.Errorf("recommended_table.table is missing")
	}

	return database, table, nil
}

// ValidateToolExecutions ensures required tools were actually executed (prevents action hallucination)
func (ctx *StructuredContext) ValidateToolExecutions(stageKey string, requiredTools []string) error {
	stageOutput, exists := ctx.StageOutputs[stageKey]
	if !exists {
		return fmt.Errorf("stage '%s' output not found", stageKey)
	}

	evidence := stageOutput.Evidence
	if len(evidence.ToolCalls) == 0 {
		return fmt.Errorf("stage '%s' executed zero tools - possible action hallucination", stageKey)
	}

	// Build map of executed tools
	executedTools := make(map[string]bool)
	for _, toolCall := range evidence.ToolCalls {
		executedTools[toolCall.ToolName] = true
	}

	// Check each required tool was executed
	var missingTools []string
	for _, requiredTool := range requiredTools {
		if !executedTools[requiredTool] {
			missingTools = append(missingTools, requiredTool)
		}
	}

	if len(missingTools) > 0 {
		return fmt.Errorf("stage '%s' missing required tool executions: %v (executed: %v)",
			stageKey, missingTools, getToolNames(evidence.ToolCalls))
	}

	return nil
}

// ValidateFileCreation ensures a file output was actually created on disk
func (ctx *StructuredContext) ValidateFileCreation(stageKey string, filePathKey string) error {
	stageOutput, exists := ctx.StageOutputs[stageKey]
	if !exists {
		return fmt.Errorf("stage '%s' output not found", stageKey)
	}

	outputs := stageOutput.Outputs
	if outputs == nil {
		return fmt.Errorf("stage '%s' has no outputs", stageKey)
	}

	filePathRaw, ok := outputs[filePathKey]
	if !ok {
		return fmt.Errorf("stage '%s' output missing '%s' field", stageKey, filePathKey)
	}

	filePath, ok := filePathRaw.(string)
	if !ok {
		return fmt.Errorf("stage '%s' output '%s' is not a string", stageKey, filePathKey)
	}

	// Check if file actually exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("stage '%s' claimed to create '%s' but file does not exist - action hallucination detected",
			stageKey, filePath)
	}

	return nil
}

// getToolNames extracts tool names from ToolCall slice
func getToolNames(toolCalls []ToolCall) []string {
	names := make([]string, len(toolCalls))
	for i, tc := range toolCalls {
		names[i] = tc.ToolName
	}
	return names
}

// parseXMLToMap converts XML stage output to a map for validation
// Handles nested <outputs> structure by parsing child elements
func parseXMLToMap(xmlStr string, result map[string]interface{}) error {
	// Simple XML parser for <stage_output> structure
	decoder := xml.NewDecoder(strings.NewReader(xmlStr))

	var currentField string
	var outputsMap map[string]interface{}
	inOutputs := false

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := token.(type) {
		case xml.StartElement:
			currentField = t.Name.Local
			if currentField == "outputs" {
				inOutputs = true
				outputsMap = make(map[string]interface{})
			}
		case xml.CharData:
			if currentField != "" && currentField != "stage_output" {
				content := strings.TrimSpace(string(t))
				if content != "" {
					if inOutputs && currentField != "outputs" {
						// Parse as appropriate type
						if content == "true" || content == "false" {
							outputsMap[currentField] = (content == "true")
						} else if num := strings.TrimSpace(content); strings.ContainsAny(num, "0123456789") && !strings.ContainsAny(num, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
							// Try parsing as number
							var intVal int
							if _, err := fmt.Sscanf(content, "%d", &intVal); err == nil {
								outputsMap[currentField] = intVal
							} else {
								outputsMap[currentField] = content
							}
						} else {
							outputsMap[currentField] = content
						}
					} else if !inOutputs {
						result[currentField] = content
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "outputs" {
				inOutputs = false
				result["outputs"] = outputsMap
			}
			currentField = ""
		}
	}

	return nil
}

// ValidateOutputStructure performs deterministic validation of stage output structure
// Supports both JSON (v3.9) and XML (v3.10) formats
// Returns detailed error if structure is invalid
func ValidateOutputStructure(output string) error {
	// Step 1: Strip thinking tags if present
	cleanedOutput := output
	if strings.Contains(output, "<thinking>") {
		// Remove everything between <thinking> and </thinking>
		thinkingStart := strings.Index(output, "<thinking>")
		thinkingEnd := strings.Index(output, "</thinking>")
		if thinkingStart != -1 && thinkingEnd != -1 && thinkingEnd > thinkingStart {
			cleanedOutput = output[:thinkingStart] + output[thinkingEnd+11:]
		}
	}

	// Step 2: Check for XML format (v3.10) or JSON format (v3.9)
	var parsed map[string]interface{}

	if strings.Contains(cleanedOutput, "<stage_output>") {
		// XML FORMAT (v3.10): Extract and parse XML
		startIdx := strings.Index(cleanedOutput, "<stage_output>")
		endIdx := strings.Index(cleanedOutput, "</stage_output>")
		if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
			return fmt.Errorf("no valid XML <stage_output> structure found in output")
		}
		xmlStr := cleanedOutput[startIdx : endIdx+15] // +15 for "</stage_output>"

		// Parse XML to map (simple XML to JSON conversion)
		parsed = make(map[string]interface{})
		if err := parseXMLToMap(xmlStr, parsed); err != nil {
			return fmt.Errorf("invalid XML: %w", err)
		}
	} else {
		// JSON FORMAT (v3.9): Extract and parse JSON
		var jsonStr string
		if strings.Contains(cleanedOutput, "```json") {
			startIdx := strings.Index(cleanedOutput, "```json")
			if startIdx != -1 {
				startIdx += 7
				endIdx := strings.Index(cleanedOutput[startIdx:], "```")
				if endIdx != -1 {
					jsonStr = cleanedOutput[startIdx : startIdx+endIdx]
				}
			}
		}
		if jsonStr == "" {
			startIdx := strings.Index(cleanedOutput, "{")
			endIdx := strings.LastIndex(cleanedOutput, "}")
			if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
				jsonStr = cleanedOutput[startIdx : endIdx+1]
			} else {
				return fmt.Errorf("no JSON object found in output")
			}
		}

		// Parse JSON
		if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	}

	// Step 4: Validate structure - support both old and new formats
	// Check if this is new flat format (v3.9) or old nested format (v3.8)
	if _, hasStageOutputs := parsed["stage_outputs"]; hasStageOutputs {
		// OLD FORMAT (v3.8): {"stage_outputs": {"stage-N": {...}}}
		return validateOldFormat(parsed)
	} else {
		// NEW FORMAT (v3.9): {"stage_id": "...", "status": "...", "outputs": {...}}
		return validateFlatFormat(parsed)
	}
}

// validateOldFormat validates v3.8 nested structure
func validateOldFormat(parsed map[string]interface{}) error {
	stageOutputsRaw, ok := parsed["stage_outputs"]
	if !ok {
		return fmt.Errorf("missing required field: 'stage_outputs'")
	}

	stageOutputsMap, ok := stageOutputsRaw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("'stage_outputs' must be an object")
	}

	if len(stageOutputsMap) == 0 {
		return fmt.Errorf("'stage_outputs' is empty")
	}

	// Validate each stage output
	for stageKey, stageDataRaw := range stageOutputsMap {
		stageData, ok := stageDataRaw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("stage_outputs['%s'] must be an object", stageKey)
		}

		// Check required fields
		if _, ok := stageData["stage_id"]; !ok {
			return fmt.Errorf("stage_outputs['%s'] missing required field: 'stage_id'", stageKey)
		}
		if _, ok := stageData["status"]; !ok {
			return fmt.Errorf("stage_outputs['%s'] missing required field: 'status'", stageKey)
		}
		if _, ok := stageData["outputs"]; !ok {
			return fmt.Errorf("stage_outputs['%s'] missing required field: 'outputs'", stageKey)
		}

		// Evidence is optional but if present, validate structure
		if evidenceRaw, ok := stageData["evidence"]; ok {
			evidence, ok := evidenceRaw.(map[string]interface{})
			if !ok {
				return fmt.Errorf("stage_outputs['%s'].evidence must be an object", stageKey)
			}
			if toolCallsRaw, ok := evidence["tool_calls"]; ok {
				if _, ok := toolCallsRaw.([]interface{}); !ok {
					return fmt.Errorf("stage_outputs['%s'].evidence.tool_calls must be an array", stageKey)
				}
			}
		}
	}

	return nil
}

// validateFlatFormat validates v3.9 flat structure
func validateFlatFormat(parsed map[string]interface{}) error {
	// Check required top-level fields
	if _, ok := parsed["stage_id"]; !ok {
		return fmt.Errorf("missing required field: 'stage_id'")
	}
	// Note: "status" field is optional - not required by YAML schemas
	// Agents may include it for clarity, but validation doesn't enforce it
	if _, ok := parsed["outputs"]; !ok {
		return fmt.Errorf("missing required field: 'outputs'")
	}

	// Outputs must be an object
	if _, ok := parsed["outputs"].(map[string]interface{}); !ok {
		return fmt.Errorf("'outputs' must be an object")
	}

	// Evidence is optional but if present, validate structure
	if evidenceRaw, ok := parsed["evidence"]; ok {
		evidence, ok := evidenceRaw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("'evidence' must be an object")
		}
		if toolCallsRaw, ok := evidence["tool_calls"]; ok {
			if _, ok := toolCallsRaw.([]interface{}); !ok {
				return fmt.Errorf("'evidence.tool_calls' must be an array")
			}
		}
	}

	return nil
}
