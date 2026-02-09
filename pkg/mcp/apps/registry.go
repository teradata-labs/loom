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

// Package apps provides the MCP Apps UI resource registry and embedded HTML apps.
// It manages registration and retrieval of interactive HTML resources that
// MCP clients (like Claude Desktop) can render alongside tool results.
package apps

import (
	"fmt"
	"sort"
	"sync"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// UIResource represents an HTML app that can be rendered by an MCP client.
type UIResource struct {
	URI         string                   // ui:// URI identifying this resource
	Name        string                   // Human-readable name
	Description string                   // Description of what this UI shows
	MIMEType    string                   // MIME type (typically protocol.ResourceMIME)
	HTML        []byte                   // Raw HTML content
	Meta        *protocol.UIResourceMeta // Security and display metadata
}

// UIResourceRegistry manages UI resources that the MCP server exposes.
// Thread-safe for concurrent access.
type UIResourceRegistry struct {
	resources map[string]*UIResource
	mu        sync.RWMutex
}

// NewUIResourceRegistry creates a new empty registry.
func NewUIResourceRegistry() *UIResourceRegistry {
	return &UIResourceRegistry{
		resources: make(map[string]*UIResource),
	}
}

// Register adds a UI resource to the registry.
// Returns an error if a resource with the same URI is already registered.
func (r *UIResourceRegistry) Register(res *UIResource) error {
	if res == nil {
		return fmt.Errorf("resource cannot be nil")
	}
	if res.URI == "" {
		return fmt.Errorf("resource URI cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.resources[res.URI]; exists {
		return fmt.Errorf("resource already registered: %s", res.URI)
	}

	r.resources[res.URI] = res
	return nil
}

// List returns all registered resources as MCP Resource objects.
// Results are sorted by URI for deterministic ordering.
func (r *UIResourceRegistry) List() []protocol.Resource {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resources := make([]protocol.Resource, 0, len(r.resources))
	for _, res := range r.resources {
		resources = append(resources, protocol.Resource{
			URI:         res.URI,
			Name:        res.Name,
			Description: res.Description,
			MimeType:    res.MIMEType,
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].URI < resources[j].URI
	})
	return resources
}

// Read returns the contents of a resource by URI.
// Returns an error if the resource is not found.
func (r *UIResourceRegistry) Read(uri string) (*protocol.ReadResourceResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res, ok := r.resources[uri]
	if !ok {
		return nil, fmt.Errorf("resource not found: %s", uri)
	}

	contents := protocol.ResourceContents{
		URI:      res.URI,
		MimeType: res.MIMEType,
		Text:     string(res.HTML),
	}

	// Add UI-specific metadata if present
	if res.Meta != nil {
		metaMap := make(map[string]interface{})
		if res.Meta.CSP != nil {
			metaMap["csp"] = res.Meta.CSP
		}
		if res.Meta.Permissions != nil {
			metaMap["permissions"] = res.Meta.Permissions
		}
		if res.Meta.Domain != "" {
			metaMap["domain"] = res.Meta.Domain
		}
		if res.Meta.PrefersBorder != nil {
			metaMap["prefersBorder"] = *res.Meta.PrefersBorder
		}
		if len(metaMap) > 0 {
			contents.Meta = metaMap
		}
	}

	return &protocol.ReadResourceResult{
		Contents: []protocol.ResourceContents{contents},
	}, nil
}

// Count returns the number of registered resources.
func (r *UIResourceRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.resources)
}
