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
package fabric

import (
	"fmt"
	"sync"
)

// BackendFactory is a function that creates a new ExecutionBackend instance.
type BackendFactory func(config map[string]interface{}) (ExecutionBackend, error)

// Registry manages backend factories and allows dynamic registration.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]BackendFactory
}

// Global registry instance
var globalRegistry = NewRegistry()

// NewRegistry creates a new backend registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]BackendFactory),
	}
}

// Register registers a backend factory with the given name.
// If a factory with the same name already exists, it will be replaced.
func (r *Registry) Register(name string, factory BackendFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Get retrieves a backend factory by name.
func (r *Registry) Get(name string) (BackendFactory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[name]
	return factory, ok
}

// Create creates a new backend instance using the registered factory.
func (r *Registry) Create(name string, config map[string]interface{}) (ExecutionBackend, error) {
	factory, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("backend not registered: %s", name)
	}
	return factory(config)
}

// List returns all registered backend names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// Unregister removes a backend factory from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, name)
}

// Global registry functions for convenience

// Register registers a backend factory in the global registry.
func Register(name string, factory BackendFactory) {
	globalRegistry.Register(name, factory)
}

// Get retrieves a backend factory from the global registry.
func Get(name string) (BackendFactory, bool) {
	return globalRegistry.Get(name)
}

// Create creates a new backend instance from the global registry.
func Create(name string, config map[string]interface{}) (ExecutionBackend, error) {
	return globalRegistry.Create(name, config)
}

// List returns all registered backend names from the global registry.
func List() []string {
	return globalRegistry.List()
}

// Unregister removes a backend factory from the global registry.
func Unregister(name string) {
	globalRegistry.Unregister(name)
}
