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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ============================================================================
// Interface compliance
// ============================================================================

func TestUIAppTools_ImplementsShuttleTool(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{},
		html:  map[string][]byte{},
	})

	require.Len(t, tools, 4)

	expectedNames := []string{"create_ui_app", "list_component_types", "update_ui_app", "delete_ui_app"}
	for i, tool := range tools {
		// Verify each implements shuttle.Tool
		var _ shuttle.Tool = tool
		assert.Equal(t, expectedNames[i], tool.Name())
		assert.NotEmpty(t, tool.Description())
		assert.NotNil(t, tool.InputSchema())
		assert.Empty(t, tool.Backend()) // All backend-agnostic
	}
}

// ============================================================================
// create_ui_app tests
// ============================================================================

func TestCreateUIAppTool_HappyPath(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	compiler := &mockAppCompiler{}
	tools := UIAppTools(compiler, provider)
	createTool := tools[0]

	result, err := createTool.Execute(context.Background(), map[string]interface{}{
		"name":         "revenue-dashboard",
		"display_name": "Revenue Dashboard",
		"description":  "Shows revenue metrics",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "Revenue Dashboard",
			"components": []interface{}{
				map[string]interface{}{
					"type": "stat-cards",
					"props": map[string]interface{}{
						"items": []interface{}{
							map[string]interface{}{"label": "Revenue", "value": "$1M"},
						},
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "revenue-dashboard", data["name"])
	assert.Equal(t, "ui://loom/revenue-dashboard", data["uri"])
	assert.True(t, data["dynamic"].(bool))
	assert.False(t, data["overwritten"].(bool))
	assert.Greater(t, data["html_bytes"].(int), 0)
	assert.Greater(t, result.ExecutionTimeMs, int64(-1))
}

func TestCreateUIAppTool_MissingName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})
	createTool := tools[0]

	result, err := createTool.Execute(context.Background(), map[string]interface{}{
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "name is required")
}

func TestCreateUIAppTool_InvalidName(t *testing.T) {
	tests := []struct {
		name    string
		appName string
	}{
		{"uppercase", "MyApp"},
		{"spaces", "my app"},
		{"starts with hyphen", "-app"},
		{"special chars", "app!@#"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
				infos: []apps.AppInfo{}, html: map[string][]byte{},
			})
			result, err := tools[0].Execute(context.Background(), map[string]interface{}{
				"name": tc.appName,
				"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
			})

			require.NoError(t, err)
			assert.False(t, result.Success)
			assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
			assert.Contains(t, result.Error.Message, "invalid app name")
		})
	}
}

func TestCreateUIAppTool_ReservedName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})
	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "component-types",
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "reserved")
}

func TestCreateUIAppTool_MissingSpec(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "spec is required")
}

func TestCreateUIAppTool_NilCompiler(t *testing.T) {
	tools := UIAppTools(nil, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "PRECONDITION_FAILED", result.Error.Code)
}

func TestCreateUIAppTool_NilProvider(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, nil)

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "PRECONDITION_FAILED", result.Error.Code)
}

func TestCreateUIAppTool_CompileError(t *testing.T) {
	compiler := &mockAppCompiler{
		compileFunc: func(spec *loomv1.UIAppSpec) ([]byte, error) {
			return nil, fmt.Errorf("invalid spec: unknown component type")
		},
	}
	tools := UIAppTools(compiler, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "Test",
			"components": []interface{}{
				map[string]interface{}{"type": "unknown"},
			},
		},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "COMPILE_ERROR", result.Error.Code)
	assert.Contains(t, result.Error.Message, "compile failed")
}

func TestCreateUIAppTool_DisplayNameFallsBackToSpecTitle(t *testing.T) {
	provider := &mockAppProvider{infos: []apps.AppInfo{}, html: map[string][]byte{}}
	tools := UIAppTools(&mockAppCompiler{}, provider)

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "Spec Title Fallback",
			"components": []interface{}{
				map[string]interface{}{"type": "text", "props": map[string]interface{}{"content": "hello"}},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "Spec Title Fallback", data["display_name"])
}

func TestCreateUIAppTool_AlreadyExists(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{
			{Name: "existing-app", URI: "ui://loom/existing-app", Dynamic: true},
		},
		html: map[string][]byte{"existing-app": []byte("<html></html>")},
	}
	tools := UIAppTools(&mockAppCompiler{}, provider)

	result, err := tools[0].Execute(context.Background(), map[string]interface{}{
		"name": "existing-app",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "New App",
			"components": []interface{}{
				map[string]interface{}{"type": "text", "props": map[string]interface{}{"content": "hello"}},
			},
		},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "CREATE_FAILED", result.Error.Code)
}

// ============================================================================
// list_component_types tests
// ============================================================================

func TestListComponentTypesTool_HappyPath(t *testing.T) {
	compiler := &mockAppCompiler{
		types: []*loomv1.ComponentType{
			{Type: "stat-cards", Description: "KPI cards", Category: "display", ExampleJson: `{"type":"stat-cards"}`},
			{Type: "chart", Description: "Charts", Category: "display"},
			{Type: "section", Description: "Layout section", Category: "layout"},
		},
	}
	tools := UIAppTools(compiler, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})
	listTool := tools[1]

	result, err := listTool.Execute(context.Background(), nil)

	require.NoError(t, err)
	assert.True(t, result.Success)

	data, ok := result.Data.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, data, 3)
	assert.Equal(t, "stat-cards", data[0]["type"])
	assert.Equal(t, "display", data[0]["category"])
	assert.Equal(t, `{"type":"stat-cards"}`, data[0]["example"])
	assert.Equal(t, "chart", data[1]["type"])
	assert.Equal(t, "section", data[2]["type"])
}

func TestListComponentTypesTool_NilCompiler(t *testing.T) {
	tools := UIAppTools(nil, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[1].Execute(context.Background(), nil)

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "PRECONDITION_FAILED", result.Error.Code)
}

// ============================================================================
// update_ui_app tests
// ============================================================================

func TestUpdateUIAppTool_HappyPath(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{
			{Name: "my-app", URI: "ui://loom/my-app", DisplayName: "My App", Dynamic: true},
		},
		html: map[string][]byte{"my-app": []byte("<html>v1</html>")},
	}
	tools := UIAppTools(&mockAppCompiler{}, provider)
	updateTool := tools[2]

	result, err := updateTool.Execute(context.Background(), map[string]interface{}{
		"name":         "my-app",
		"display_name": "My App v2",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "My App v2",
			"components": []interface{}{
				map[string]interface{}{"type": "text", "props": map[string]interface{}{"content": "updated"}},
			},
		},
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "my-app", data["name"])
	assert.Equal(t, "My App v2", data["display_name"])
	assert.Greater(t, data["html_bytes"].(int), 0)
}

func TestUpdateUIAppTool_MissingName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[2].Execute(context.Background(), map[string]interface{}{
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

func TestUpdateUIAppTool_InvalidName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[2].Execute(context.Background(), map[string]interface{}{
		"name": "INVALID_NAME!",
		"spec": map[string]interface{}{"version": "1.0", "title": "Test"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "invalid app name")
}

func TestUpdateUIAppTool_MissingSpec(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[2].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

func TestUpdateUIAppTool_NotFound(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[2].Execute(context.Background(), map[string]interface{}{
		"name": "nonexistent",
		"spec": map[string]interface{}{
			"version": "1.0",
			"title":   "Test",
			"components": []interface{}{
				map[string]interface{}{"type": "text", "props": map[string]interface{}{"content": "hello"}},
			},
		},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "UPDATE_FAILED", result.Error.Code)
}

func TestUpdateUIAppTool_NilCompiler(t *testing.T) {
	tools := UIAppTools(nil, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[2].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
		"spec": map[string]interface{}{"version": "1.0"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "PRECONDITION_FAILED", result.Error.Code)
}

// ============================================================================
// delete_ui_app tests
// ============================================================================

func TestDeleteUIAppTool_HappyPath(t *testing.T) {
	provider := &mockAppProvider{
		infos: []apps.AppInfo{
			{Name: "my-app", URI: "ui://loom/my-app", Dynamic: true},
		},
		html: map[string][]byte{"my-app": []byte("<html>app</html>")},
	}
	tools := UIAppTools(&mockAppCompiler{}, provider)
	deleteTool := tools[3]

	result, err := deleteTool.Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "my-app", data["name"])
	assert.True(t, data["deleted"].(bool))

	// Verify removed from provider
	assert.Empty(t, provider.infos)
}

func TestDeleteUIAppTool_MissingName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[3].Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

func TestDeleteUIAppTool_InvalidName(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[3].Execute(context.Background(), map[string]interface{}{
		"name": "Invalid Name!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "invalid app name")
}

func TestDeleteUIAppTool_NotFound(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, &mockAppProvider{
		infos: []apps.AppInfo{}, html: map[string][]byte{},
	})

	result, err := tools[3].Execute(context.Background(), map[string]interface{}{
		"name": "nonexistent",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "DELETE_FAILED", result.Error.Code)
}

func TestDeleteUIAppTool_NilProvider(t *testing.T) {
	tools := UIAppTools(&mockAppCompiler{}, nil)

	result, err := tools[3].Execute(context.Background(), map[string]interface{}{
		"name": "my-app",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "PRECONDITION_FAILED", result.Error.Code)
}

// ============================================================================
// mapToUIAppSpec tests
// ============================================================================

func TestMapToUIAppSpec_BasicSpec(t *testing.T) {
	m := map[string]interface{}{
		"version": "1.0",
		"title":   "Test Dashboard",
		"layout":  "grid-2",
		"components": []interface{}{
			map[string]interface{}{
				"type": "stat-cards",
				"props": map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{"label": "Users", "value": "100"},
					},
				},
			},
		},
	}

	spec, err := mapToUIAppSpec(m)
	require.NoError(t, err)
	assert.Equal(t, "1.0", spec.Version)
	assert.Equal(t, "Test Dashboard", spec.Title)
	assert.Equal(t, "grid-2", spec.Layout)
	require.Len(t, spec.Components, 1)
	assert.Equal(t, "stat-cards", spec.Components[0].Type)
}

func TestMapToUIAppSpec_UnknownFieldsTolerated(t *testing.T) {
	// LLMs may add extra fields; DiscardUnknown should tolerate them.
	m := map[string]interface{}{
		"version":      "1.0",
		"title":        "Test",
		"extra_field":  "should be ignored",
		"another_junk": 42,
		"components": []interface{}{
			map[string]interface{}{
				"type": "text",
				"props": map[string]interface{}{
					"content": "hello",
				},
			},
		},
	}

	spec, err := mapToUIAppSpec(m)
	require.NoError(t, err)
	assert.Equal(t, "1.0", spec.Version)
	assert.Equal(t, "Test", spec.Title)
}

func TestMapToUIAppSpec_EmptyMap(t *testing.T) {
	spec, err := mapToUIAppSpec(map[string]interface{}{})
	require.NoError(t, err)
	assert.NotNil(t, spec)
	assert.Empty(t, spec.Version)
	assert.Empty(t, spec.Title)
}

// ============================================================================
// Concurrent execution (race detector validation)
// ============================================================================

func TestUIAppTools_ConcurrentExecution(t *testing.T) {
	tsProvider := &threadSafeAppProvider{
		provider: &mockAppProvider{
			infos: []apps.AppInfo{
				{Name: "existing-app", URI: "ui://loom/existing-app", Dynamic: true},
			},
			html: map[string][]byte{
				"existing-app": []byte("<html>existing</html>"),
			},
		},
	}
	compiler := &mockAppCompiler{
		types: []*loomv1.ComponentType{
			{Type: "text", Description: "Text block", Category: "display"},
		},
	}

	tools := UIAppTools(compiler, tsProvider)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines * 4)

	specParams := map[string]interface{}{
		"version": "1.0",
		"title":   "Concurrent Test",
		"components": []interface{}{
			map[string]interface{}{
				"type":  "text",
				"props": map[string]interface{}{"content": "hello"},
			},
		},
	}

	// Concurrent creates
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, _ = tools[0].Execute(context.Background(), map[string]interface{}{
				"name":      fmt.Sprintf("app-%d", idx),
				"spec":      specParams,
				"overwrite": true,
			})
		}(i)
	}

	// Concurrent list_component_types
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = tools[1].Execute(context.Background(), nil)
		}()
	}

	// Concurrent updates
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = tools[2].Execute(context.Background(), map[string]interface{}{
				"name": "existing-app",
				"spec": specParams,
			})
		}()
	}

	// Concurrent deletes
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			_, _ = tools[3].Execute(context.Background(), map[string]interface{}{
				"name": fmt.Sprintf("app-%d", idx),
			})
		}(i)
	}

	wg.Wait()
	// Race detector is the primary assertion
}
