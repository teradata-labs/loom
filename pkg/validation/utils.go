// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

// detectWorkflowType determines if workflow is orchestration or multi-agent.
// Returns "orchestration" for pattern/type-based workflows, "multi-agent" for entrypoint-based, "" if ambiguous.
func detectWorkflowType(spec map[string]interface{}) string {
	// Check for orchestration indicators (pattern or type field)
	if pattern, ok := spec["pattern"].(string); ok && pattern != "" {
		return "orchestration"
	}
	if workflowType, ok := spec["type"].(string); ok && workflowType != "" {
		return "orchestration"
	}

	// Check for deprecated workflow_type field (treat as orchestration so we can show deprecation errors)
	if workflowType, ok := spec["workflow_type"].(string); ok && workflowType != "" {
		return "orchestration" // Will be caught as deprecated in validateOrchestrationWorkflowStructure
	}

	// Check for multi-agent indicator (entrypoint field)
	if entrypoint, ok := spec["entrypoint"].(string); ok && entrypoint != "" {
		return "multi-agent"
	}

	// Ambiguous
	return ""
}
