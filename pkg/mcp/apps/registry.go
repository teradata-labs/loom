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
	"strings"
	"sync"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

const (
	// DefaultMaxDynamic is the maximum number of dynamic (agent-created) apps.
	DefaultMaxDynamic = 100

	// DefaultMaxTotalBytes is the maximum total HTML bytes for dynamic apps (50 MB).
	DefaultMaxTotalBytes int64 = 50 * 1024 * 1024
)

// UIResource represents an HTML app that can be rendered by an MCP client.
type UIResource struct {
	URI         string                   // ui:// URI identifying this resource
	Name        string                   // Human-readable name
	Description string                   // Description of what this UI shows
	MIMEType    string                   // MIME type (typically protocol.ResourceMIME)
	HTML        []byte                   // Raw HTML content
	Meta        *protocol.UIResourceMeta // Security and display metadata
	Dynamic     bool                     // true = agent-created, false = embedded
}

// UIResourceRegistry manages UI resources that the MCP server exposes.
// Thread-safe for concurrent access.
type UIResourceRegistry struct {
	resources     map[string]*UIResource
	mu            sync.RWMutex
	onChange      func() // Called outside lock after mutations
	maxDynamic    int    // Max dynamic apps (default 100)
	maxTotalBytes int64  // Max total HTML bytes for dynamic apps (default 50MB)
}

// NewUIResourceRegistry creates a new empty registry.
func NewUIResourceRegistry() *UIResourceRegistry {
	return &UIResourceRegistry{
		resources:     make(map[string]*UIResource),
		maxDynamic:    DefaultMaxDynamic,
		maxTotalBytes: DefaultMaxTotalBytes,
	}
}

// SetOnChange registers a callback that is invoked after any mutation
// (Upsert, Delete). The callback is called outside the lock to prevent
// deadlocks. Only one callback can be registered; a new call replaces
// the previous one.
func (r *UIResourceRegistry) SetOnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = fn
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

// Upsert creates or replaces a dynamic UI resource. It rejects overwrites of
// embedded (non-dynamic) resources. Returns true if a new resource was created,
// false if an existing dynamic resource was replaced.
func (r *UIResourceRegistry) Upsert(res *UIResource) (created bool, err error) {
	if res == nil {
		return false, fmt.Errorf("resource cannot be nil")
	}
	if res.URI == "" {
		return false, fmt.Errorf("resource URI cannot be empty")
	}
	if !res.Dynamic {
		return false, fmt.Errorf("only dynamic resources can be upserted")
	}

	var notify func()

	r.mu.Lock()
	created, err = r.upsertLocked(res)
	notify = r.onChange
	r.mu.Unlock()

	if err != nil {
		return false, err
	}
	if notify != nil {
		notify()
	}
	return created, nil
}

// upsertLocked inserts or replaces a dynamic resource in the map.
// The caller MUST hold r.mu in write mode.
func (r *UIResourceRegistry) upsertLocked(res *UIResource) (created bool, err error) {
	existing, exists := r.resources[res.URI]
	if exists && !existing.Dynamic {
		return false, fmt.Errorf("cannot overwrite embedded resource: %s", res.URI)
	}

	// Capacity checks for new resources
	if !exists {
		dynCount, dynBytes := r.dynamicStatsLocked()
		if dynCount >= r.maxDynamic {
			return false, fmt.Errorf("dynamic app limit reached (%d)", r.maxDynamic)
		}
		if dynBytes+int64(len(res.HTML)) > r.maxTotalBytes {
			return false, fmt.Errorf("dynamic app total size limit reached (%d bytes)", r.maxTotalBytes)
		}
	}

	r.resources[res.URI] = res
	return !exists, nil
}

// Delete removes a dynamic UI resource by URI. Rejects deletion of embedded resources.
func (r *UIResourceRegistry) Delete(uri string) error {
	var notify func()

	r.mu.Lock()
	existing, exists := r.resources[uri]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("resource not found: %s", uri)
	}
	if !existing.Dynamic {
		r.mu.Unlock()
		return fmt.Errorf("cannot delete embedded resource: %s", uri)
	}

	delete(r.resources, uri)
	notify = r.onChange
	r.mu.Unlock()

	if notify != nil {
		notify()
	}
	return nil
}

// dynamicStatsLocked returns the count and total HTML bytes of dynamic resources.
// Must be called with r.mu held.
func (r *UIResourceRegistry) dynamicStatsLocked() (count int, totalBytes int64) {
	for _, res := range r.resources {
		if res.Dynamic {
			count++
			totalBytes += int64(len(res.HTML))
		}
	}
	return count, totalBytes
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

// Get returns a UIResource by its URI, or an error if not found.
func (r *UIResourceRegistry) Get(uri string) (*UIResource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res, ok := r.resources[uri]
	if !ok {
		return nil, fmt.Errorf("resource not found: %s", uri)
	}
	copy := *res
	return &copy, nil
}

// AppNames returns sorted short names extracted from all registered URIs.
// For example, "ui://loom/data-chart" yields "data-chart".
func (r *UIResourceRegistry) AppNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.resources))
	for uri := range r.resources {
		names = append(names, extractAppName(uri))
	}
	sort.Strings(names)
	return names
}

// AppHTML returns the raw HTML content for an app identified by short name.
// Returns an error if no app matches the given name.
func (r *UIResourceRegistry) AppHTML(name string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for uri, res := range r.resources {
		if extractAppName(uri) == name {
			return append([]byte(nil), res.HTML...), nil
		}
	}
	return nil, fmt.Errorf("app not found: %s", name)
}

// AppInfo holds metadata about a UI app for use by the gRPC server.
// This avoids the server importing the apps package directly.
type AppInfo struct {
	Name          string
	URI           string
	DisplayName   string
	Description   string
	MimeType      string
	PrefersBorder bool
	Dynamic       bool
}

// ListAppInfo returns metadata for all registered apps, sorted by name.
func (r *UIResourceRegistry) ListAppInfo() []AppInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]AppInfo, 0, len(r.resources))
	for _, res := range r.resources {
		info := AppInfo{
			Name:        extractAppName(res.URI),
			URI:         res.URI,
			DisplayName: res.Name,
			Description: res.Description,
			MimeType:    res.MIMEType,
			Dynamic:     res.Dynamic,
		}
		if res.Meta != nil && res.Meta.PrefersBorder != nil {
			info.PrefersBorder = *res.Meta.PrefersBorder
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// GetAppHTML returns the HTML content and metadata for an app by short name.
// Returns an error if no app matches the given name.
func (r *UIResourceRegistry) GetAppHTML(name string) ([]byte, *AppInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, res := range r.resources {
		if extractAppName(res.URI) == name {
			info := &AppInfo{
				Name:        name,
				URI:         res.URI,
				DisplayName: res.Name,
				Description: res.Description,
				MimeType:    res.MIMEType,
				Dynamic:     res.Dynamic,
			}
			if res.Meta != nil && res.Meta.PrefersBorder != nil {
				info.PrefersBorder = *res.Meta.PrefersBorder
			}
			return append([]byte(nil), res.HTML...), info, nil
		}
	}
	return nil, nil, fmt.Errorf("app not found: %s", name)
}

// CreateApp creates a dynamic app from compiled HTML. It builds the UIResource,
// checks existence atomically, and returns the app info. This implements the
// AppProvider interface for the gRPC server.
func (r *UIResourceRegistry) CreateApp(name, displayName, description string, html []byte, overwrite bool) (*AppInfo, bool, error) {
	uri := "ui://loom/" + name
	res := &UIResource{
		URI:         uri,
		Name:        displayName,
		Description: description,
		MIMEType:    protocol.ResourceMIME,
		HTML:        html,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtrTrue(),
		},
		Dynamic: true,
	}

	var notify func()

	// Single write lock: check-and-create atomically to prevent TOCTOU races.
	r.mu.Lock()
	existing, exists := r.resources[uri]
	if exists && !overwrite {
		r.mu.Unlock()
		if existing.Dynamic {
			return nil, false, fmt.Errorf("app already exists: %s (use overwrite=true to replace)", name)
		}
		return nil, false, fmt.Errorf("cannot overwrite embedded app: %s", name)
	}

	created, err := r.upsertLocked(res)
	notify = r.onChange
	r.mu.Unlock()

	if err != nil {
		return nil, false, err
	}
	if notify != nil {
		notify()
	}

	info := &AppInfo{
		Name:          name,
		URI:           uri,
		DisplayName:   displayName,
		Description:   description,
		MimeType:      protocol.ResourceMIME,
		PrefersBorder: true,
		Dynamic:       true,
	}
	return info, !created, nil
}

// UpdateApp updates an existing dynamic app with new HTML. Returns an error
// if the app doesn't exist or is embedded.
func (r *UIResourceRegistry) UpdateApp(name, displayName, description string, html []byte) (*AppInfo, error) {
	uri := "ui://loom/" + name

	var notify func()

	// Single write lock: check-and-update atomically to prevent TOCTOU races.
	r.mu.Lock()
	existing, exists := r.resources[uri]
	if !exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("app not found: %s", name)
	}
	if !existing.Dynamic {
		r.mu.Unlock()
		return nil, fmt.Errorf("cannot update embedded app: %s", name)
	}

	// Use existing values for empty fields
	if displayName == "" {
		displayName = existing.Name
	}
	if description == "" {
		description = existing.Description
	}

	res := &UIResource{
		URI:         uri,
		Name:        displayName,
		Description: description,
		MIMEType:    protocol.ResourceMIME,
		HTML:        html,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtrTrue(),
		},
		Dynamic: true,
	}

	_, err := r.upsertLocked(res)
	notify = r.onChange
	r.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if notify != nil {
		notify()
	}

	return &AppInfo{
		Name:          name,
		URI:           uri,
		DisplayName:   displayName,
		Description:   description,
		MimeType:      protocol.ResourceMIME,
		PrefersBorder: true,
		Dynamic:       true,
	}, nil
}

// DeleteApp deletes a dynamic app by short name.
func (r *UIResourceRegistry) DeleteApp(name string) error {
	return r.Delete("ui://loom/" + name)
}

func boolPtrTrue() *bool {
	b := true
	return &b
}

// ExtractAppName extracts the short app name from a ui:// URI.
// For example, "ui://loom/data-chart" returns "data-chart".
func ExtractAppName(uri string) string {
	// Find last "/" and return everything after it
	if idx := strings.LastIndex(uri, "/"); idx >= 0 {
		return uri[idx+1:]
	}
	return uri
}

// extractAppName is an unexported alias for internal callers.
func extractAppName(uri string) string {
	return ExtractAppName(uri)
}
