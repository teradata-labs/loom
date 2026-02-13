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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"google.golang.org/protobuf/encoding/protojson"
)

// UIAppTools returns shuttle.Tool implementations that allow server-side agents
// to create, update, list, and delete MCP UI apps. These tools are auto-registered
// to all agents so any agent can create interactive visualizations.
func UIAppTools(compiler AppCompiler, provider AppProvider) []shuttle.Tool {
	return []shuttle.Tool{
		&createUIAppTool{compiler: compiler, provider: provider},
		&listComponentTypesTool{compiler: compiler},
		&updateUIAppTool{compiler: compiler, provider: provider},
		&deleteUIAppTool{provider: provider},
	}
}

// ============================================================================
// create_ui_app
// ============================================================================

type createUIAppTool struct {
	compiler AppCompiler
	provider AppProvider
}

func (t *createUIAppTool) Name() string { return "create_ui_app" }

func (t *createUIAppTool) Description() string {
	return `Create an interactive MCP UI app from a declarative JSON spec. The app is compiled to standalone HTML and served via the MCP resource system.

Use list_component_types to discover available component types and their prop schemas before building a spec.

Spec format:
{
  "version": "1.0",
  "title": "App Title",
  "layout": "stack",
  "components": [
    {"type": "header", "props": {"title": "Dashboard"}},
    {"type": "chart", "props": {"chartType": "bar", "labels": [...], "datasets": [...]}}
  ]
}

Layouts: stack (vertical), grid-2 (two columns), grid-3 (three columns).`
}

func (t *createUIAppTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for creating a UI app",
		map[string]*shuttle.JSONSchema{
			"name": shuttle.NewStringSchema(
				"URL-safe short name (lowercase alphanumeric and hyphens, e.g. 'revenue-analysis')",
			).WithPattern(`^[a-z0-9][a-z0-9-]{0,62}$`),
			"display_name": shuttle.NewStringSchema("Human-readable display name (optional, defaults to spec title)"),
			"description":  shuttle.NewStringSchema("Description of what this app shows (optional)"),
			"spec": shuttle.NewObjectSchema(
				"Declarative app spec: {version: '1.0', title, layout, components: [{type, props, children?, id?}]}",
				nil, nil,
			),
			"overwrite": shuttle.NewBooleanSchema("Overwrite an existing dynamic app with the same name (default: false)"),
		},
		[]string{"name", "spec"},
	)
}

func (t *createUIAppTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	if t.compiler == nil || t.provider == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "PRECONDITION_FAILED", Message: "UI app compiler or provider not configured"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract and validate name
	name, _ := params["name"].(string)
	if name == "" {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_PARAMS", Message: "name is required", Suggestion: "Provide a lowercase alphanumeric name with hyphens"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if !appNameRegex.MatchString(name) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    fmt.Sprintf("invalid app name %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", name),
				Suggestion: "Use lowercase letters, numbers, and hyphens (e.g. 'revenue-dashboard')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if reservedAppNames[name] {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    fmt.Sprintf("app name %q is reserved (collides with HTTP route)", name),
				Suggestion: "Choose a different name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract spec
	specMap, ok := params["spec"].(map[string]interface{})
	if !ok || specMap == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_PARAMS", Message: "spec is required and must be an object", Suggestion: "Provide a spec with version, title, and components"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert map to proto UIAppSpec
	spec, err := mapToUIAppSpec(specMap)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_SPEC", Message: fmt.Sprintf("failed to parse spec: %v", err), Suggestion: "Check spec format matches UIAppSpec schema"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Compile spec to HTML
	html, err := t.compiler.Compile(spec)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "COMPILE_ERROR", Message: fmt.Sprintf("compile failed: %v", err), Suggestion: "Use list_component_types to check valid component types and props"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract optional params
	displayName, _ := params["display_name"].(string)
	if displayName == "" {
		displayName = spec.Title
	}
	description, _ := params["description"].(string)
	overwrite, _ := params["overwrite"].(bool)

	// Create app
	info, overwritten, err := t.provider.CreateApp(name, displayName, description, html, overwrite)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "CREATE_FAILED", Message: fmt.Sprintf("create app failed: %v", err), Suggestion: "App may already exist; set overwrite=true to replace it"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"name":         info.Name,
			"uri":          info.URI,
			"display_name": info.DisplayName,
			"dynamic":      info.Dynamic,
			"overwritten":  overwritten,
			"html_bytes":   len(html),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *createUIAppTool) Backend() string { return "" }

// ============================================================================
// list_component_types
// ============================================================================

type listComponentTypesTool struct {
	compiler AppCompiler
}

func (t *listComponentTypesTool) Name() string { return "list_component_types" }

func (t *listComponentTypesTool) Description() string {
	return "List available UI component types for building dynamic MCP apps. Returns type names, descriptions, categories, prop schemas, and examples. Call this before create_ui_app to discover what components are available."
}

func (t *listComponentTypesTool) InputSchema() *shuttle.JSONSchema {
	// Explicit empty properties map (not nil) required for Azure OpenAI compatibility.
	// Azure rejects object schemas without a "properties" field.
	return shuttle.NewObjectSchema("No parameters required", map[string]*shuttle.JSONSchema{}, nil)
}

func (t *listComponentTypesTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	if t.compiler == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "PRECONDITION_FAILED", Message: "UI app compiler not configured"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	types := t.compiler.ListComponentTypes()
	result := make([]map[string]interface{}, 0, len(types))
	for _, ct := range types {
		entry := map[string]interface{}{
			"type":        ct.Type,
			"description": ct.Description,
			"category":    ct.Category,
		}
		if ct.PropsSchema != nil {
			entry["props_schema"] = ct.PropsSchema.AsMap()
		}
		if ct.ExampleJson != "" {
			entry["example"] = ct.ExampleJson
		}
		result = append(result, entry)
	}

	return &shuttle.Result{
		Success:         true,
		Data:            result,
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *listComponentTypesTool) Backend() string { return "" }

// ============================================================================
// update_ui_app
// ============================================================================

type updateUIAppTool struct {
	compiler AppCompiler
	provider AppProvider
}

func (t *updateUIAppTool) Name() string { return "update_ui_app" }

func (t *updateUIAppTool) Description() string {
	return "Update an existing dynamic MCP UI app's spec. Recompiles the spec to HTML and replaces the app content."
}

func (t *updateUIAppTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for updating a UI app",
		map[string]*shuttle.JSONSchema{
			"name":         shuttle.NewStringSchema("App name to update (required)"),
			"display_name": shuttle.NewStringSchema("New display name (empty = keep existing)"),
			"description":  shuttle.NewStringSchema("New description (empty = keep existing)"),
			"spec": shuttle.NewObjectSchema(
				"New declarative app spec",
				nil, nil,
			),
		},
		[]string{"name", "spec"},
	)
}

func (t *updateUIAppTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	if t.compiler == nil || t.provider == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "PRECONDITION_FAILED", Message: "UI app compiler or provider not configured"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	name, _ := params["name"].(string)
	if name == "" {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_PARAMS", Message: "name is required"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if !appNameRegex.MatchString(name) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    fmt.Sprintf("invalid app name %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", name),
				Suggestion: "Use lowercase letters, numbers, and hyphens (e.g. 'revenue-dashboard')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	specMap, ok := params["spec"].(map[string]interface{})
	if !ok || specMap == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_PARAMS", Message: "spec is required and must be an object"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	spec, err := mapToUIAppSpec(specMap)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_SPEC", Message: fmt.Sprintf("failed to parse spec: %v", err)},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	html, err := t.compiler.Compile(spec)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "COMPILE_ERROR", Message: fmt.Sprintf("compile failed: %v", err)},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	displayName, _ := params["display_name"].(string)
	description, _ := params["description"].(string)

	info, err := t.provider.UpdateApp(name, displayName, description, html)
	if err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "UPDATE_FAILED", Message: fmt.Sprintf("update failed: %v", err)},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"name":         info.Name,
			"uri":          info.URI,
			"display_name": info.DisplayName,
			"dynamic":      info.Dynamic,
			"html_bytes":   len(html),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *updateUIAppTool) Backend() string { return "" }

// ============================================================================
// delete_ui_app
// ============================================================================

type deleteUIAppTool struct {
	provider AppProvider
}

func (t *deleteUIAppTool) Name() string { return "delete_ui_app" }

func (t *deleteUIAppTool) Description() string {
	return "Delete a dynamic MCP UI app by name."
}

func (t *deleteUIAppTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for deleting a UI app",
		map[string]*shuttle.JSONSchema{
			"name": shuttle.NewStringSchema("App name to delete (required)"),
		},
		[]string{"name"},
	)
}

func (t *deleteUIAppTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	if t.provider == nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "PRECONDITION_FAILED", Message: "UI app provider not configured"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	name, _ := params["name"].(string)
	if name == "" {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "INVALID_PARAMS", Message: "name is required"},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if !appNameRegex.MatchString(name) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    fmt.Sprintf("invalid app name %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", name),
				Suggestion: "Use lowercase letters, numbers, and hyphens (e.g. 'revenue-dashboard')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if err := t.provider.DeleteApp(name); err != nil {
		return &shuttle.Result{
			Success:         false,
			Error:           &shuttle.Error{Code: "DELETE_FAILED", Message: fmt.Sprintf("delete failed: %v", err)},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"name":    name,
			"deleted": true,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *deleteUIAppTool) Backend() string { return "" }

// ============================================================================
// Helpers
// ============================================================================

// mapToUIAppSpec converts a map[string]interface{} (from LLM tool params) to a
// *loomv1.UIAppSpec proto message. Uses JSON round-trip with protojson for correct
// field name mapping. DiscardUnknown tolerates extra fields from the LLM.
func mapToUIAppSpec(m map[string]interface{}) (*loomv1.UIAppSpec, error) {
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal map to JSON: %w", err)
	}

	spec := &loomv1.UIAppSpec{}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(jsonBytes, spec); err != nil {
		return nil, fmt.Errorf("unmarshal JSON to UIAppSpec: %w", err)
	}

	return spec, nil
}
