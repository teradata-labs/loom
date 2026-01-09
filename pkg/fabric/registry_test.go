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
	"context"
	"errors"
	"sync"
	"testing"
)

// mockBackend is a mock implementation of ExecutionBackend for testing
type mockBackend struct {
	name string
}

func (m *mockBackend) Name() string { return m.name }
func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*QueryResult, error) {
	return &QueryResult{Type: "mock", RowCount: 0}, nil
}
func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*Schema, error) {
	return &Schema{Name: resource, Type: "table"}, nil
}
func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]Resource, error) {
	return []Resource{{Name: "mock_resource", Type: "table"}}, nil
}
func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{"mock": true}, nil
}
func (m *mockBackend) Ping(ctx context.Context) error { return nil }
func (m *mockBackend) Capabilities() *Capabilities {
	return NewCapabilities()
}
func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, errors.New("not implemented")
}
func (m *mockBackend) Close() error { return nil }

func TestRegistry_Register(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	reg.Register("test", factory)

	if _, ok := reg.Get("test"); !ok {
		t.Error("Expected factory to be registered")
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	reg.Register("test", factory)

	got, ok := reg.Get("test")
	if !ok {
		t.Fatal("Expected factory to exist")
	}

	if got == nil {
		t.Error("Expected non-nil factory")
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("Expected factory to not exist")
	}
}

func TestRegistry_Create(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		name, _ := config["name"].(string)
		return &mockBackend{name: name}, nil
	}

	reg.Register("test", factory)

	backend, err := reg.Create("test", map[string]interface{}{"name": "my-backend"})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if backend.Name() != "my-backend" {
		t.Errorf("Expected name 'my-backend', got %s", backend.Name())
	}
}

func TestRegistry_Create_NotRegistered(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Create("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for non-registered backend")
	}

	if err.Error() != "backend not registered: nonexistent" {
		t.Errorf("Expected specific error message, got: %v", err)
	}
}

func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	reg.Register("backend1", factory)
	reg.Register("backend2", factory)
	reg.Register("backend3", factory)

	list := reg.List()
	if len(list) != 3 {
		t.Errorf("Expected 3 backends, got %d", len(list))
	}

	// Check that all backends are present
	found := make(map[string]bool)
	for _, name := range list {
		found[name] = true
	}

	for _, expected := range []string{"backend1", "backend2", "backend3"} {
		if !found[expected] {
			t.Errorf("Expected to find %s in list", expected)
		}
	}
}

func TestRegistry_Unregister(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	reg.Register("test", factory)

	if _, ok := reg.Get("test"); !ok {
		t.Fatal("Expected factory to be registered")
	}

	reg.Unregister("test")

	if _, ok := reg.Get("test"); ok {
		t.Error("Expected factory to be unregistered")
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	// Test concurrent registration
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			reg.Register("backend", factory)
			_, _ = reg.Get("backend")
			reg.Unregister("backend")
		}(i)
	}

	wg.Wait()
}

func TestRegistry_ConcurrentCreateAndList(t *testing.T) {
	reg := NewRegistry()

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "test"}, nil
	}

	reg.Register("test", factory)

	// Test concurrent creation and listing
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)

		go func() {
			defer wg.Done()
			_, _ = reg.Create("test", nil)
		}()

		go func() {
			defer wg.Done()
			_ = reg.List()
		}()
	}

	wg.Wait()
}

func TestGlobalRegistry(t *testing.T) {
	// Clean up
	for _, name := range List() {
		Unregister(name)
	}

	factory := func(config map[string]interface{}) (ExecutionBackend, error) {
		name, _ := config["name"].(string)
		return &mockBackend{name: name}, nil
	}

	Register("global", factory)

	_, ok := Get("global")
	if !ok {
		t.Error("Expected global factory to be registered")
	}

	backend, err := Create("global", map[string]interface{}{"name": "test"})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if backend.Name() != "test" {
		t.Errorf("Expected name 'test', got %s", backend.Name())
	}

	list := List()
	if len(list) == 0 {
		t.Error("Expected at least one registered backend")
	}

	Unregister("global")

	_, ok = Get("global")
	if ok {
		t.Error("Expected global factory to be unregistered")
	}
}

func TestRegistry_ReplaceFactory(t *testing.T) {
	reg := NewRegistry()

	factory1 := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "v1"}, nil
	}

	factory2 := func(config map[string]interface{}) (ExecutionBackend, error) {
		return &mockBackend{name: "v2"}, nil
	}

	reg.Register("test", factory1)
	backend, _ := reg.Create("test", nil)
	if backend.Name() != "v1" {
		t.Errorf("Expected v1, got %s", backend.Name())
	}

	// Replace with factory2
	reg.Register("test", factory2)
	backend, _ = reg.Create("test", nil)
	if backend.Name() != "v2" {
		t.Errorf("Expected v2, got %s", backend.Name())
	}
}
