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
	"fmt"
	"sync"
)

// Registry manages tool registration and lookup.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// Global registry instance
var globalRegistry = NewRegistry()

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register registers a tool with the registry.
// If a tool with the same name already exists, it will be replaced.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// IsRegistered checks if a tool is already registered.
// This is useful for progressive tool disclosure (registering tools only when needed).
func (r *Registry) IsRegistered(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.tools[name]
	return exists
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// ListTools returns all registered tools.
func (r *Registry) ListTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ListByBackend returns all tools for a specific backend.
// Pass empty string to get backend-agnostic tools.
func (r *Registry) ListByBackend(backend string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var tools []Tool
	for _, tool := range r.tools {
		if tool.Backend() == backend || tool.Backend() == "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

// Unregister removes a tool from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Global registry functions for convenience

// Register registers a tool in the global registry.
func Register(tool Tool) {
	globalRegistry.Register(tool)
}

// Get retrieves a tool from the global registry.
func Get(name string) (Tool, bool) {
	return globalRegistry.Get(name)
}

// MustGet retrieves a tool from the global registry and panics if not found.
// This is useful for testing and initialization code.
func MustGet(name string) Tool {
	tool, ok := globalRegistry.Get(name)
	if !ok {
		panic(fmt.Sprintf("tool not found: %s", name))
	}
	return tool
}

// List returns all registered tool names from the global registry.
func List() []string {
	return globalRegistry.List()
}

// ListTools returns all registered tools from the global registry.
func ListTools() []Tool {
	return globalRegistry.ListTools()
}

// ListByBackend returns all tools for a specific backend from the global registry.
func ListByBackend(backend string) []Tool {
	return globalRegistry.ListByBackend(backend)
}

// Unregister removes a tool from the global registry.
func Unregister(name string) {
	globalRegistry.Unregister(name)
}

// Count returns the number of registered tools in the global registry.
func Count() int {
	return globalRegistry.Count()
}
