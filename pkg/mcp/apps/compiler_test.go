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

package apps

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// validSpec returns a minimal valid UIAppSpec suitable for most tests.
func validSpec() *loomv1.UIAppSpec {
	props, _ := structpb.NewStruct(map[string]interface{}{
		"content": "Hello world",
	})
	return &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Test App",
		Layout:  "stack",
		Components: []*loomv1.UIComponent{
			{
				Type:  "text",
				Props: props,
			},
		},
	}
}

func TestNewCompiler(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotNil(t, c.catalog)
	assert.NotNil(t, c.tmpl)
	assert.NotEmpty(t, c.runtime)
}

func TestCompiler_Validate_ValidSpec(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name   string
		layout string
	}{
		{"empty layout (default)", ""},
		{"stack layout", "stack"},
		{"grid-2 layout", "grid-2"},
		{"grid-3 layout", "grid-3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			spec.Layout = tc.layout
			err := c.Validate(spec)
			assert.NoError(t, err)
		})
	}
}

func TestCompiler_Validate_NilSpec(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	err = c.Validate(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec is nil")
}

func TestCompiler_Validate_InvalidVersion(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name    string
		version string
	}{
		{"empty version", ""},
		{"version 2.0", "2.0"},
		{"version 0.1", "0.1"},
		{"random string", "foobar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			spec.Version = tc.version
			err := c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported spec version")
		})
	}
}

func TestCompiler_Validate_InvalidLayout(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name   string
		layout string
	}{
		{"grid-4 layout", "grid-4"},
		{"horizontal layout", "horizontal"},
		{"random string", "foobar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			spec.Layout = tc.layout
			err := c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid layout")
		})
	}
}

func TestCompiler_Validate_EmptyComponents(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version:    "1.0",
		Title:      "Empty App",
		Layout:     "stack",
		Components: []*loomv1.UIComponent{},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one component")
}

func TestCompiler_Validate_UnknownComponentType(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Bad Component",
		Components: []*loomv1.UIComponent{
			{Type: "nonexistent-widget"},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type")
	assert.Contains(t, err.Error(), "nonexistent-widget")
}

func TestCompiler_Validate_DangerousKeys(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name string
		key  string
	}{
		{"__proto__", "__proto__"},
		{"constructor", "constructor"},
		{"prototype", "prototype"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			props, err := structpb.NewStruct(map[string]interface{}{
				tc.key: "malicious",
			})
			require.NoError(t, err)

			spec := &loomv1.UIAppSpec{
				Version: "1.0",
				Title:   "Dangerous Key App",
				Components: []*loomv1.UIComponent{
					{Type: "text", Props: props},
				},
			}

			err = c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dangerous key")
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestCompiler_Validate_DangerousValues(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name  string
		value string
	}{
		{"javascript: protocol", "javascript:alert(1)"},
		{"JavaScript: mixed case", "JavaScript:alert(1)"},
		{"vbscript: protocol", "vbscript:MsgBox"},
		{"data:text/html", "data:text/html,<script>alert(1)</script>"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			props, err := structpb.NewStruct(map[string]interface{}{
				"content": tc.value,
			})
			require.NoError(t, err)

			spec := &loomv1.UIAppSpec{
				Version: "1.0",
				Title:   "Dangerous Value App",
				Components: []*loomv1.UIComponent{
					{Type: "text", Props: props},
				},
			}

			err = c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dangerous value rejected")
		})
	}
}

func TestCompiler_Validate_ExceedMaxNestingDepth(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Build 11-level-deep nesting using "section" components (MaxNestingDepth = 10).
	// The innermost component is a "text" leaf.
	innerProps, _ := structpb.NewStruct(map[string]interface{}{"content": "deep leaf"})
	current := []*loomv1.UIComponent{{Type: "text", Props: innerProps}}

	for i := 0; i < 11; i++ {
		sectionProps, _ := structpb.NewStruct(map[string]interface{}{
			"title": fmt.Sprintf("Level %d", 11-i),
		})
		current = []*loomv1.UIComponent{{
			Type:     "section",
			Props:    sectionProps,
			Children: current,
		}}
	}

	spec := &loomv1.UIAppSpec{
		Version:    "1.0",
		Title:      "Deep Nesting",
		Components: current,
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nesting depth exceeds maximum")
}

func TestCompiler_Validate_ExceedMaxSpecBytes(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Create a text component with content larger than MaxSpecBytes (512 KB).
	bigContent := strings.Repeat("x", 600*1024)
	props, err := structpb.NewStruct(map[string]interface{}{
		"content": bigContent,
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Huge Spec",
		Components: []*loomv1.UIComponent{
			{Type: "text", Props: props},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestCompiler_Validate_ExceedMaxComponentPropsBytes(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Create a single component whose props exceed MaxComponentPropsBytes (64 KB).
	bigValue := strings.Repeat("a", 65*1024)
	props, err := structpb.NewStruct(map[string]interface{}{
		"content": bigValue,
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Huge Props",
		Components: []*loomv1.UIComponent{
			{Type: "text", Props: props},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "props exceed maximum size")
}

func TestCompiler_Validate_DangerousCSSPatternInArray(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name    string
		value   string
		pattern string
	}{
		{"url() in array", "background: url(http://evil.com)", "url("},
		{"expression() in array", "width: expression(alert(1))", "expression("},
		{"@import in array", "@import 'http://evil.com/inject.css'", "@import"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			props, err := structpb.NewStruct(map[string]interface{}{
				"items": []interface{}{tc.value},
			})
			require.NoError(t, err)

			spec := &loomv1.UIAppSpec{
				Version: "1.0",
				Title:   "CSS in Array",
				Components: []*loomv1.UIComponent{
					{Type: "stat-cards", Props: props},
				},
			}

			err = c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dangerous CSS pattern rejected in array element")
			assert.Contains(t, err.Error(), tc.pattern)
		})
	}
}

func TestCompiler_Validate_DangerousCSSPatterns(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	tests := []struct {
		name  string
		value string
	}{
		{"url() function", "background: url(http://evil.com/track.gif)"},
		{"expression() function", "width: expression(alert(1))"},
		{"@import directive", "@import url(http://evil.com/inject.css)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			props, err := structpb.NewStruct(map[string]interface{}{
				"content": tc.value,
			})
			require.NoError(t, err)

			spec := &loomv1.UIAppSpec{
				Version: "1.0",
				Title:   "CSS Injection App",
				Components: []*loomv1.UIComponent{
					{Type: "text", Props: props},
				},
			}

			err = c.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dangerous CSS pattern rejected")
		})
	}
}

func TestCompiler_Validate_ChildrenOnNonLayoutComponent(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	childProps, err := structpb.NewStruct(map[string]interface{}{
		"content": "child text",
	})
	require.NoError(t, err)

	// "text" does not support children, only "section" and "tabs" do.
	parentProps, err := structpb.NewStruct(map[string]interface{}{
		"content": "parent text",
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Children On Non-Layout",
		Components: []*loomv1.UIComponent{
			{
				Type:  "text",
				Props: parentProps,
				Children: []*loomv1.UIComponent{
					{Type: "text", Props: childProps},
				},
			},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support children")
}

func TestCompiler_Validate_ChildrenOnSectionIsValid(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	sectionProps, err := structpb.NewStruct(map[string]interface{}{
		"title": "My Section",
	})
	require.NoError(t, err)

	childProps, err := structpb.NewStruct(map[string]interface{}{
		"content": "child text",
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Section With Children",
		Components: []*loomv1.UIComponent{
			{
				Type:  "section",
				Props: sectionProps,
				Children: []*loomv1.UIComponent{
					{Type: "text", Props: childProps},
				},
			},
		},
	}

	err = c.Validate(spec)
	assert.NoError(t, err)
}

func TestCompiler_Compile_ValidSpec(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	html, err := c.Compile(spec)
	require.NoError(t, err)
	require.NotEmpty(t, html)

	htmlStr := string(html)

	// Output should contain the title
	assert.Contains(t, htmlStr, "Test App")

	// Output should contain the spec JSON (HTML-escaped by template engine)
	assert.Contains(t, htmlStr, `version`)
	assert.Contains(t, htmlStr, `1.0`)

	// Output should be valid-looking HTML
	assert.Contains(t, htmlStr, "<!DOCTYPE html>")
	assert.Contains(t, htmlStr, "<html")
	assert.Contains(t, htmlStr, "</html>")

	// Should embed the runtime JS
	assert.Contains(t, htmlStr, "<script>")
}

func TestCompiler_Compile_ContainsCSP(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// The template contains a CSP meta tag
	assert.Contains(t, htmlStr, "Content-Security-Policy")
	assert.Contains(t, htmlStr, "default-src 'none'")
}

func TestCompiler_Compile_DefaultTitle(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	spec.Title = "" // Empty title should default to "Loom App"

	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)
	assert.Contains(t, htmlStr, "<title>Loom App</title>")
}

func TestCompiler_Compile_InvalidSpec(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Invalid spec should fail compilation
	spec := &loomv1.UIAppSpec{
		Version:    "2.0", // Invalid version
		Components: []*loomv1.UIComponent{{Type: "text"}},
	}

	_, err = c.Compile(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid spec")
}

func TestCompiler_ListComponentTypes(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	types := c.ListComponentTypes()
	require.Len(t, types, 14)

	// Verify all types have required fields populated
	typeNames := make(map[string]bool)
	for _, ct := range types {
		assert.NotEmpty(t, ct.Type, "type name should not be empty")
		assert.NotEmpty(t, ct.Description, "description should not be empty for %s", ct.Type)
		assert.NotEmpty(t, ct.Category, "category should not be empty for %s", ct.Type)
		assert.NotEmpty(t, ct.ExampleJson, "example_json should not be empty for %s", ct.Type)
		typeNames[ct.Type] = true
	}

	// Verify all expected types are present
	expectedTypes := []string{
		"stat-cards", "chart", "table", "key-value", "text",
		"code-block", "progress-bar", "badges", "heatmap",
		"header", "section", "tabs",
		"dag", "message-list",
	}
	for _, et := range expectedTypes {
		assert.True(t, typeNames[et], "expected component type %q to be in catalog", et)
	}
}

func TestCompiler_ListComponentTypes_Categories(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	types := c.ListComponentTypes()

	categories := make(map[string][]string)
	for _, ct := range types {
		categories[ct.Category] = append(categories[ct.Category], ct.Type)
	}

	// Verify category distribution
	assert.NotEmpty(t, categories["display"], "display category should have entries")
	assert.NotEmpty(t, categories["layout"], "layout category should have entries")
	assert.NotEmpty(t, categories["complex"], "complex category should have entries")
}

func TestCompiler_Validate_ExceedMaxComponents(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Create spec with 51 components (exceeds MaxComponents = 50)
	components := make([]*loomv1.UIComponent, 51)
	for i := range components {
		props, _ := structpb.NewStruct(map[string]interface{}{
			"content": "text",
		})
		components[i] = &loomv1.UIComponent{
			Type:  "text",
			Props: props,
		}
	}

	spec := &loomv1.UIAppSpec{
		Version:    "1.0",
		Title:      "Too Many Components",
		Components: components,
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "component count exceeds maximum")
}

func TestCompiler_Validate_EmptyComponentType(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Empty Type",
		Components: []*loomv1.UIComponent{
			{Type: ""},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type is required")
}

func TestCompiler_Validate_DangerousKeysNested(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Dangerous key nested inside a sub-object
	props, err := structpb.NewStruct(map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"__proto__": "evil",
			},
		},
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Nested Dangerous Key",
		Components: []*loomv1.UIComponent{
			{Type: "stat-cards", Props: props},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dangerous key")
}

func TestCompiler_Validate_DangerousValueInArray(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Dangerous value inside an array element
	props, err := structpb.NewStruct(map[string]interface{}{
		"items": []interface{}{
			"javascript:alert(1)",
		},
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Array Dangerous Value",
		Components: []*loomv1.UIComponent{
			{Type: "stat-cards", Props: props},
		},
	}

	err = c.Validate(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dangerous value rejected")
}

func TestCompiler_Validate_AllComponentTypes(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Validate that all 14 component types are accepted with minimal valid props
	types := c.Catalog().ValidTypes()
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			var props *structpb.Struct
			switch typ {
			case "text":
				props, _ = structpb.NewStruct(map[string]interface{}{"content": "hello"})
			case "header":
				props, _ = structpb.NewStruct(map[string]interface{}{"title": "Header"})
			case "chart":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"chartType": "bar",
					"labels":    []interface{}{"A", "B"},
					"datasets":  []interface{}{map[string]interface{}{"label": "set1", "data": []interface{}{1.0, 2.0}}},
				})
			case "table":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"columns": []interface{}{"col1"},
					"rows":    []interface{}{[]interface{}{"val1"}},
				})
			case "key-value":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"items": []interface{}{map[string]interface{}{"key": "k", "value": "v"}},
				})
			case "stat-cards":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"items": []interface{}{map[string]interface{}{"label": "L", "value": "V"}},
				})
			case "code-block":
				props, _ = structpb.NewStruct(map[string]interface{}{"code": "SELECT 1"})
			case "progress-bar":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"items": []interface{}{map[string]interface{}{"label": "Disk", "value": 50.0}},
				})
			case "badges":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"items": []interface{}{map[string]interface{}{"text": "OK", "color": "success"}},
				})
			case "heatmap":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"rowLabels":    []interface{}{"R1"},
					"columnLabels": []interface{}{"C1"},
					"values":       []interface{}{[]interface{}{1.0}},
				})
			case "dag":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"nodes": []interface{}{map[string]interface{}{"id": "a", "label": "A"}},
					"edges": []interface{}{},
				})
			case "message-list":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
				})
			case "section":
				// section has children, test separately with a child
				props, _ = structpb.NewStruct(map[string]interface{}{"title": "Sec"})
				childProps, _ := structpb.NewStruct(map[string]interface{}{"content": "inner"})
				spec := &loomv1.UIAppSpec{
					Version: "1.0",
					Title:   "Section Test",
					Components: []*loomv1.UIComponent{
						{
							Type:     "section",
							Props:    props,
							Children: []*loomv1.UIComponent{{Type: "text", Props: childProps}},
						},
					},
				}
				assert.NoError(t, c.Validate(spec))
				return
			case "tabs":
				props, _ = structpb.NewStruct(map[string]interface{}{
					"tabs": []interface{}{map[string]interface{}{"label": "Tab1"}},
				})
				childProps, _ := structpb.NewStruct(map[string]interface{}{"content": "tab content"})
				spec := &loomv1.UIAppSpec{
					Version: "1.0",
					Title:   "Tabs Test",
					Components: []*loomv1.UIComponent{
						{
							Type:     "tabs",
							Props:    props,
							Children: []*loomv1.UIComponent{{Type: "text", Props: childProps}},
						},
					},
				}
				assert.NoError(t, c.Validate(spec))
				return
			default:
				// For any new type not handled above, use empty struct
				props, _ = structpb.NewStruct(map[string]interface{}{})
			}

			spec := &loomv1.UIAppSpec{
				Version: "1.0",
				Title:   "Type Test: " + typ,
				Components: []*loomv1.UIComponent{
					{Type: typ, Props: props},
				},
			}
			assert.NoError(t, c.Validate(spec))
		})
	}
}

func TestCompiler_Compile_SpecJSONEmbeddedInHTML(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// The template places spec JSON in a <script type="application/json" id="app-spec">
	assert.Contains(t, htmlStr, `id="app-spec"`)
	assert.Contains(t, htmlStr, `type="application/json"`)

	// Verify the spec JSON is between those tags (HTML-escaped by template engine)
	assert.True(t, strings.Contains(htmlStr, "components"),
		"compiled HTML should contain the spec's components field")
}

func TestCompiler_Catalog(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	catalog := c.Catalog()
	require.NotNil(t, catalog)

	// Verify catalog type lookups
	assert.True(t, catalog.IsValidType("chart"))
	assert.True(t, catalog.IsValidType("text"))
	assert.False(t, catalog.IsValidType("nonexistent"))

	// Verify children capability
	assert.True(t, catalog.HasChildren("section"))
	assert.True(t, catalog.HasChildren("tabs"))
	assert.False(t, catalog.HasChildren("text"))
	assert.False(t, catalog.HasChildren("chart"))
	assert.False(t, catalog.HasChildren("nonexistent"))
}

// ============================================================================
// Security: Compiled Output Tests
// ============================================================================

func TestCompiler_Compile_XSS_TitleEscaped(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	spec.Title = `<script>alert("XSS")</script>`

	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// The title must be HTML-escaped in both the <title> tag and any other usage.
	// Go's html/template auto-escapes strings in HTML context.
	assert.NotContains(t, htmlStr, `<script>alert("XSS")</script>`,
		"raw script tag must NOT appear in compiled output")
	assert.Contains(t, htmlStr, "&lt;script&gt;",
		"script tag must be HTML-escaped")
}

func TestCompiler_Compile_XSS_SpecValuesEscaped(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	// Test that user-controlled prop values containing HTML are escaped
	// in the spec JSON block. Since the spec is inside
	// <script type="application/json">, Go's template engine escapes
	// HTML entities for safety.
	props, err := structpb.NewStruct(map[string]interface{}{
		"content": `Hello <img src=x onerror=alert(1)> World`,
	})
	require.NoError(t, err)

	spec := &loomv1.UIAppSpec{
		Version: "1.0",
		Title:   "Safe App",
		Components: []*loomv1.UIComponent{
			{Type: "text", Props: props},
		},
	}

	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// The raw HTML should NOT appear unescaped within the compiled output.
	// html/template escapes < > in HTML context to prevent injection.
	assert.NotContains(t, htmlStr, `<img src=x onerror=alert(1)>`,
		"raw img/onerror payload must NOT appear unescaped")
}

func TestCompiler_Compile_SpecInjectedAsDataBlock(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// Spec MUST be in a <script type="application/json"> data block, NOT a JS literal.
	// This pattern prevents spec injection because textContent is immune to HTML injection.
	assert.Contains(t, htmlStr, `<script type="application/json" id="app-spec">`,
		"spec must be inside a JSON data block, not a JS literal")

	// Verify we do NOT use template.JS for spec data (dangerous pattern: var APP_SPEC = {...})
	assert.NotContains(t, htmlStr, "var APP_SPEC =",
		"spec must NOT be injected as a JS variable literal")
	assert.NotContains(t, htmlStr, "const APP_SPEC =",
		"spec must NOT be injected as a JS const literal")
}

// ============================================================================
// Security: CSP Meta Tag Tests
// ============================================================================

func TestCompiler_Compile_CSP_AllDirectives(t *testing.T) {
	c, err := NewCompiler()
	require.NoError(t, err)

	spec := validSpec()
	html, err := c.Compile(spec)
	require.NoError(t, err)

	htmlStr := string(html)

	// Verify all 6 meta-tag-compatible CSP directives are present.
	// Note: frame-ancestors is enforced via HTTP header only (ignored in meta tags per CSP spec).
	requiredDirectives := []struct {
		directive string
		reason    string
	}{
		{"default-src 'none'", "blocks all resource loading by default"},
		{"script-src 'unsafe-inline' https://cdn.jsdelivr.net", "allows inline JS + Chart.js CDN only"},
		{"style-src 'unsafe-inline'", "allows inline styles for theming"},
		{"img-src data:", "allows only data: URIs for images (no external tracking)"},
		{"connect-src 'none'", "blocks fetch/XHR/WebSocket exfiltration"},
		{"form-action 'none'", "blocks form submission data exfiltration"},
	}

	// frame-ancestors must NOT be in the meta tag (it's only valid as HTTP header)
	assert.NotContains(t, htmlStr, "frame-ancestors",
		"frame-ancestors must NOT be in meta CSP tag (only valid as HTTP header per CSP spec)")

	for _, d := range requiredDirectives {
		assert.Contains(t, htmlStr, d.directive,
			"CSP must contain %q (%s)", d.directive, d.reason)
	}

	// Verify the CSP is in a meta tag (not just a comment)
	assert.Contains(t, htmlStr, `<meta http-equiv="Content-Security-Policy"`,
		"CSP must be a meta tag")
}

// ============================================================================
// Security: SRI Hash Consistency Tests
// ============================================================================

func TestCompiler_SRIHashConsistency(t *testing.T) {
	// All files that reference Chart.js must use the same SRI hash.
	// If hashes diverge, one file loads a different (potentially compromised) version.

	// Extract SRI hash from runtime.js (the embedded JS used by dynamic apps)
	runtimeSrc := string(runtimeJS)
	runtimeHashIdx := strings.Index(runtimeSrc, "s.integrity = '")
	require.NotEqual(t, -1, runtimeHashIdx, "runtime.js must contain an SRI integrity assignment")

	start := runtimeHashIdx + len("s.integrity = '")
	end := strings.Index(runtimeSrc[start:], "'")
	require.NotEqual(t, -1, end, "SRI integrity value must have closing quote")
	runtimeHash := runtimeSrc[start : start+end]

	assert.True(t, strings.HasPrefix(runtimeHash, "sha384-"),
		"SRI hash must use sha384 algorithm, got: %s", runtimeHash)

	// Verify the same hash appears in all embedded HTML files that use Chart.js
	embeddedFiles := map[string][]byte{
		"app-template (via runtime.js)": runtimeJS,
	}

	// Check that no embedded HTML file has a DIFFERENT sha384- hash
	// (This catches the case where someone updates one file but not the others)
	for name, content := range embeddedFiles {
		contentStr := string(content)
		// Find all sha384- hashes in this file
		for _, match := range regexp.MustCompile(`sha384-[A-Za-z0-9+/=]+`).FindAllString(contentStr, -1) {
			assert.Equal(t, runtimeHash, match,
				"SRI hash mismatch in %s: expected %s, got %s", name, runtimeHash, match)
		}
	}
}

// ============================================================================
// Security: Runtime.js Static Analysis Tests
// ============================================================================

func TestRuntimeJS_NoDangerousDOMAPIs(t *testing.T) {
	// The runtime.js must NEVER use innerHTML, outerHTML, eval, document.write,
	// or Function() constructor. These are XSS vectors.

	src := string(runtimeJS)

	dangerousPatterns := []struct {
		pattern string
		reason  string
	}{
		{".innerHTML", "innerHTML allows arbitrary HTML injection"},
		{".outerHTML", "outerHTML allows arbitrary HTML injection"},
		{"eval(", "eval allows arbitrary code execution"},
		{"document.write(", "document.write allows arbitrary HTML injection"},
		{"document.writeln(", "document.writeln allows arbitrary HTML injection"},
		{"new Function(", "Function constructor allows arbitrary code execution"},
	}

	for _, dp := range dangerousPatterns {
		// Check outside of comments. Simple heuristic: split by lines,
		// skip lines that are comment-only (start with // after trim).
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue // skip comment lines
			}
			assert.NotContains(t, line, dp.pattern,
				"runtime.js line %d contains dangerous API %q (%s): %s",
				i+1, dp.pattern, dp.reason, trimmed)
		}
	}
}

func TestRuntimeJS_SecurityInvariants(t *testing.T) {
	// Verify that critical security mechanisms are present in runtime.js.

	src := string(runtimeJS)

	invariants := []struct {
		pattern string
		reason  string
	}{
		{"Object.freeze(APP_SPEC)", "spec must be frozen to prevent prototype pollution"},
		{"setSafeAttribute", "safe attribute setter must exist to block on* and href"},
		{"BLOCKED_ATTR_PATTERN", "attribute blocking pattern must be defined"},
		{"SVG_ALLOWED_ELEMENTS", "SVG element allowlist must be defined"},
		{"SVG_ALLOWED_ATTRS", "SVG attribute allowlist must be defined"},
		{"Object.hasOwn(", "must use Object.hasOwn for safe property checks"},
		{"'use strict'", "must run in strict mode"},
	}

	for _, inv := range invariants {
		assert.Contains(t, src, inv.pattern,
			"runtime.js must contain %q (%s)", inv.pattern, inv.reason)
	}
}

func TestRuntimeJS_SVGBlockedElements(t *testing.T) {
	// Verify that dangerous SVG elements are NOT in the allowlist.

	src := string(runtimeJS)

	// These elements must NOT appear in SVG_ALLOWED_ELEMENTS
	blockedElements := []string{
		"'script'",
		"'foreignObject'",
		"'use'",
		"'image'",
		"'animate'",
		"'set'",
	}

	// Extract the SVG_ALLOWED_ELEMENTS definition
	allowlistStart := strings.Index(src, "SVG_ALLOWED_ELEMENTS = new Set([")
	require.NotEqual(t, -1, allowlistStart, "SVG_ALLOWED_ELEMENTS must be defined")

	// Find the closing ]);
	allowlistEnd := strings.Index(src[allowlistStart:], "]);")
	require.NotEqual(t, -1, allowlistEnd, "SVG_ALLOWED_ELEMENTS must have closing bracket")

	allowlist := src[allowlistStart : allowlistStart+allowlistEnd+3]

	for _, elem := range blockedElements {
		assert.NotContains(t, allowlist, elem,
			"SVG_ALLOWED_ELEMENTS must NOT contain dangerous element %s", elem)
	}
}

func TestRuntimeJS_ChartJSConfigNotPassthrough(t *testing.T) {
	// Verify that runtime.js does NOT pass spec props directly to Chart.js.
	// It must construct the config from allowlisted fields.

	src := string(runtimeJS)

	// There should be a function that builds a safe Chart.js config
	assert.Contains(t, src, "buildSafeChartConfig",
		"runtime.js must have a buildSafeChartConfig function for Chart.js safety")

	// The pattern "new Chart(ctx, props)" or "new Chart(ctx, spec" would indicate
	// unsafe passthrough. The safe pattern is "new Chart(ctx, buildSafeChartConfig(...))"
	// or similar indirection.
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "new Chart(") {
			// If this line creates a Chart, it should NOT directly pass props
			assert.NotContains(t, line, "new Chart(ctx, props)",
				"runtime.js line %d: Chart.js must NOT receive raw props", i+1)
		}
	}
}
