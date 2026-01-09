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
package shuttle

import (
	"context"

	"github.com/teradata-labs/loom/pkg/prompts"
)

// PromptAwareTool wraps a tool and loads descriptions from PromptRegistry.
// Falls back to tool's native Description() if prompt not found.
// This enables externalized tool descriptions while maintaining backward compatibility.
type PromptAwareTool struct {
	tool      Tool
	registry  prompts.PromptRegistry
	promptKey string
}

// NewPromptAwareTool wraps a tool with PromptRegistry-based descriptions.
// If registry is nil, returns the original tool unchanged (no wrapping overhead).
//
// Example:
//
//	registry := prompts.NewFileRegistry("./prompts")
//	tool := builtin.NewHTTPClientTool()
//	wrapped := shuttle.NewPromptAwareTool(tool, registry, "tools.http_request")
//	description := wrapped.Description() // Loads from prompts/tools/http_request.yaml
func NewPromptAwareTool(tool Tool, registry prompts.PromptRegistry, promptKey string) Tool {
	if registry == nil {
		return tool // No registry, use tool as-is (no wrapping)
	}
	return &PromptAwareTool{
		tool:      tool,
		registry:  registry,
		promptKey: promptKey,
	}
}

// Name returns the tool's unique identifier (delegates to wrapped tool).
func (p *PromptAwareTool) Name() string {
	return p.tool.Name()
}

// Description loads the tool description from PromptRegistry.
// Falls back to the wrapped tool's native Description() if:
// - PromptRegistry lookup fails
// - Prompt is not found
// - Prompt is empty
//
// This ensures tools always have a valid description, even if prompts are not configured.
func (p *PromptAwareTool) Description() string {
	// Try loading from registry first
	if p.registry != nil {
		desc, err := p.registry.Get(context.Background(), p.promptKey, nil)
		if err == nil && desc != "" {
			return desc
		}
		// If lookup failed, fall through to native description
	}

	// Fall back to tool's native description
	return p.tool.Description()
}

// InputSchema returns the JSON Schema for tool parameters (delegates to wrapped tool).
func (p *PromptAwareTool) InputSchema() *JSONSchema {
	return p.tool.InputSchema()
}

// Execute runs the tool with given parameters (delegates to wrapped tool).
func (p *PromptAwareTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	return p.tool.Execute(ctx, params)
}

// Backend returns the backend type this tool requires (delegates to wrapped tool).
func (p *PromptAwareTool) Backend() string {
	return p.tool.Backend()
}
