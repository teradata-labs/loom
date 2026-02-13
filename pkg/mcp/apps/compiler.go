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
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Structural limits for spec validation.
const (
	MaxComponents          = 50
	MaxNestingDepth        = 10
	MaxComponentPropsBytes = 64 * 1024  // 64 KB per component props
	MaxSpecBytes           = 512 * 1024 // 512 KB total spec
)

// Dangerous keys that are rejected at any nesting depth (prototype pollution prevention).
var dangerousKeys = map[string]bool{
	"__proto__":   true,
	"constructor": true,
	"prototype":   true,
}

// Dangerous string value prefixes.
var dangerousValuePrefixes = []string{
	"javascript:",
	"vbscript:",
	"data:text/html",
}

// Dangerous CSS value patterns.
var dangerousCSSPatterns = []string{
	"url(",
	"expression(",
	"@import",
}

//go:embed html/app-template.html
var appTemplateHTML []byte

//go:embed html/runtime.js
var runtimeJS []byte

// Compiler validates UIAppSpec and compiles it to standalone HTML.
type Compiler struct {
	catalog *ComponentCatalog
	tmpl    *template.Template
	runtime string
}

// templateData is passed to the HTML template during execution.
type templateData struct {
	Title    string
	SpecJSON template.JS // #nosec -- sanitized protojson output, </ escaped to prevent script tag injection
	Runtime  template.JS // #nosec -- runtime.js is embedded at build time, not user input
}

// NewCompiler creates a new compiler with the embedded template and runtime.
func NewCompiler() (*Compiler, error) {
	tmpl, err := template.New("app").Parse(string(appTemplateHTML))
	if err != nil {
		return nil, fmt.Errorf("parse app template: %w", err)
	}

	return &Compiler{
		catalog: NewComponentCatalog(),
		tmpl:    tmpl,
		runtime: string(runtimeJS),
	}, nil
}

// Compile validates the spec and compiles it to a standalone HTML document.
func (c *Compiler) Compile(spec *loomv1.UIAppSpec) ([]byte, error) {
	if err := c.Validate(spec); err != nil {
		return nil, fmt.Errorf("invalid spec: %w", err)
	}

	// Marshal spec to JSON using protojson for correct field naming
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}

	title := spec.Title
	if title == "" {
		title = "Loom App"
	}

	// Sanitize JSON for safe embedding inside <script type="application/json">.
	// Escape <, >, & to their JSON unicode equivalents (\u003c, \u003e, \u0026)
	// to prevent: (1) premature </script> tag closing, (2) HTML injection in
	// defense-in-depth. JSON.parse handles these unicode escapes transparently.
	// This matches Go's encoding/json.Marshal HTML-safe escaping behavior.
	safeJSON := strings.ReplaceAll(string(specJSON), "<", `\u003c`)
	safeJSON = strings.ReplaceAll(safeJSON, ">", `\u003e`)
	safeJSON = strings.ReplaceAll(safeJSON, "&", `\u0026`)

	data := templateData{
		Title:    title,
		SpecJSON: template.JS(safeJSON),  // #nosec -- sanitized protojson output with </ escaped
		Runtime:  template.JS(c.runtime), // #nosec -- embedded build-time JS, not user content
	}

	var buf bytes.Buffer
	if err := c.tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return buf.Bytes(), nil
}

// Validate checks a spec against structural limits, component type validity,
// and security constraints (dangerous keys/values).
func (c *Compiler) Validate(spec *loomv1.UIAppSpec) error {
	if spec == nil {
		return fmt.Errorf("spec is nil")
	}

	if spec.Version != "1.0" {
		return fmt.Errorf("unsupported spec version %q (expected \"1.0\")", spec.Version)
	}

	// Check total spec size
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal spec for size check: %w", err)
	}
	if len(specJSON) > MaxSpecBytes {
		return fmt.Errorf("spec exceeds maximum size (%d bytes > %d bytes)", len(specJSON), MaxSpecBytes)
	}

	// Validate layout
	switch spec.Layout {
	case "", "stack", "grid-2", "grid-3":
		// valid
	default:
		return fmt.Errorf("invalid layout %q (valid: stack, grid-2, grid-3)", spec.Layout)
	}

	// Validate components
	if len(spec.Components) == 0 {
		return fmt.Errorf("spec must have at least one component")
	}

	componentCount := 0
	if err := c.validateComponents(spec.Components, 0, &componentCount); err != nil {
		return err
	}

	return nil
}

// validateComponents recursively validates a list of components.
func (c *Compiler) validateComponents(components []*loomv1.UIComponent, depth int, count *int) error {
	if depth > MaxNestingDepth {
		return fmt.Errorf("component nesting depth exceeds maximum (%d)", MaxNestingDepth)
	}

	for i, comp := range components {
		*count++
		if *count > MaxComponents {
			return fmt.Errorf("component count exceeds maximum (%d)", MaxComponents)
		}

		if comp.Type == "" {
			return fmt.Errorf("component[%d] at depth %d: type is required", i, depth)
		}

		if !c.catalog.IsValidType(comp.Type) {
			return fmt.Errorf("component[%d]: unknown type %q (valid types: %s)",
				i, comp.Type, strings.Join(c.catalog.ValidTypes(), ", "))
		}

		// Check props size
		if comp.Props != nil {
			propsJSON, err := comp.Props.MarshalJSON()
			if err != nil {
				return fmt.Errorf("component[%d] %q: failed to marshal props: %w", i, comp.Type, err)
			}
			if len(propsJSON) > MaxComponentPropsBytes {
				return fmt.Errorf("component[%d] %q: props exceed maximum size (%d bytes > %d bytes)",
					i, comp.Type, len(propsJSON), MaxComponentPropsBytes)
			}

			// Sanitize props for dangerous keys and values
			if err := sanitizeStruct(comp.Props.AsMap()); err != nil {
				return fmt.Errorf("component[%d] %q props: %w", i, comp.Type, err)
			}
		}

		// Validate children
		if len(comp.Children) > 0 {
			if !c.catalog.HasChildren(comp.Type) {
				return fmt.Errorf("component[%d] %q does not support children", i, comp.Type)
			}
			if err := c.validateComponents(comp.Children, depth+1, count); err != nil {
				return err
			}
		}
	}

	return nil
}

// sanitizeStruct recursively checks a map for dangerous keys and values.
func sanitizeStruct(m map[string]interface{}) error {
	for key, val := range m {
		if dangerousKeys[key] {
			return fmt.Errorf("dangerous key %q rejected (prototype pollution prevention)", key)
		}

		switch v := val.(type) {
		case string:
			lower := strings.ToLower(v)
			for _, prefix := range dangerousValuePrefixes {
				if strings.HasPrefix(lower, prefix) {
					return fmt.Errorf("dangerous value rejected in key %q: %q", key, prefix+"...")
				}
			}
			for _, pattern := range dangerousCSSPatterns {
				if strings.Contains(lower, pattern) {
					return fmt.Errorf("dangerous CSS pattern rejected in key %q: %q", key, pattern)
				}
			}
		case map[string]interface{}:
			if err := sanitizeStruct(v); err != nil {
				return err
			}
		case []interface{}:
			if err := sanitizeSlice(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// sanitizeSlice recursively checks a slice for dangerous keys and values.
func sanitizeSlice(s []interface{}) error {
	for _, val := range s {
		switch v := val.(type) {
		case string:
			lower := strings.ToLower(v)
			for _, prefix := range dangerousValuePrefixes {
				if strings.HasPrefix(lower, prefix) {
					return fmt.Errorf("dangerous value rejected: %q", prefix+"...")
				}
			}
			for _, pattern := range dangerousCSSPatterns {
				if strings.Contains(lower, pattern) {
					return fmt.Errorf("dangerous CSS pattern rejected in array element: %q", pattern)
				}
			}
		case map[string]interface{}:
			if err := sanitizeStruct(v); err != nil {
				return err
			}
		case []interface{}:
			if err := sanitizeSlice(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Catalog returns the compiler's component catalog.
func (c *Compiler) Catalog() *ComponentCatalog {
	return c.catalog
}

// ListComponentTypes returns proto ComponentType messages for the discovery RPC.
func (c *Compiler) ListComponentTypes() []*loomv1.ComponentType {
	return c.catalog.ToProto()
}
