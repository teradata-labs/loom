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
	"sync"
	"testing"
)

func TestRegistry_Register(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test", description: "test tool", backend: "test"}

	reg.Register(tool)

	got, ok := reg.Get("test")
	if !ok {
		t.Fatal("Expected tool to be registered")
	}

	if got.Name() != "test" {
		t.Errorf("Expected name 'test', got %s", got.Name())
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test", description: "test tool"}

	reg.Register(tool)

	got, ok := reg.Get("test")
	if !ok {
		t.Fatal("Expected tool to exist")
	}

	if got == nil {
		t.Error("Expected non-nil tool")
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("Expected tool to not exist")
	}
}

func TestRegistry_List(t *testing.T) {
	reg := NewRegistry()

	reg.Register(&mockTool{name: "tool1"})
	reg.Register(&mockTool{name: "tool2"})
	reg.Register(&mockTool{name: "tool3"})

	list := reg.List()
	if len(list) != 3 {
		t.Errorf("Expected 3 tools, got %d", len(list))
	}

	// Check that all tools are present
	found := make(map[string]bool)
	for _, name := range list {
		found[name] = true
	}

	for _, expected := range []string{"tool1", "tool2", "tool3"} {
		if !found[expected] {
			t.Errorf("Expected to find %s in list", expected)
		}
	}
}

func TestRegistry_ListTools(t *testing.T) {
	reg := NewRegistry()

	reg.Register(&mockTool{name: "tool1", description: "desc1"})
	reg.Register(&mockTool{name: "tool2", description: "desc2"})

	tools := reg.ListTools()
	if len(tools) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(tools))
	}
}

func TestRegistry_ListByBackend(t *testing.T) {
	reg := NewRegistry()

	reg.Register(&mockTool{name: "sql1", backend: "postgres"})
	reg.Register(&mockTool{name: "sql2", backend: "postgres"})
	reg.Register(&mockTool{name: "api1", backend: "rest-api"})
	reg.Register(&mockTool{name: "generic", backend: ""})

	postgresTools := reg.ListByBackend("postgres")
	if len(postgresTools) != 3 { // sql1, sql2, and generic (backend-agnostic)
		t.Errorf("Expected 3 postgres-compatible tools, got %d", len(postgresTools))
	}

	apiTools := reg.ListByBackend("rest-api")
	if len(apiTools) != 2 { // api1 and generic
		t.Errorf("Expected 2 api-compatible tools, got %d", len(apiTools))
	}

	genericTools := reg.ListByBackend("")
	if len(genericTools) != 1 { // Only generic
		t.Errorf("Expected 1 backend-agnostic tool, got %d", len(genericTools))
	}
}

func TestRegistry_Unregister(t *testing.T) {
	reg := NewRegistry()
	tool := &mockTool{name: "test"}

	reg.Register(tool)

	if _, ok := reg.Get("test"); !ok {
		t.Fatal("Expected tool to be registered")
	}

	reg.Unregister("test")

	if _, ok := reg.Get("test"); ok {
		t.Error("Expected tool to be unregistered")
	}
}

func TestRegistry_Count(t *testing.T) {
	reg := NewRegistry()

	if reg.Count() != 0 {
		t.Error("Expected count to be 0")
	}

	reg.Register(&mockTool{name: "tool1"})
	reg.Register(&mockTool{name: "tool2"})

	if reg.Count() != 2 {
		t.Errorf("Expected count to be 2, got %d", reg.Count())
	}

	reg.Unregister("tool1")

	if reg.Count() != 1 {
		t.Errorf("Expected count to be 1, got %d", reg.Count())
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tool := &mockTool{name: "tool"}
			reg.Register(tool)
			_, _ = reg.Get("tool")
			_ = reg.List()
			_ = reg.Count()
			reg.Unregister("tool")
		}(i)
	}

	wg.Wait()
}

func TestRegistry_ConcurrentRegisterAndList(t *testing.T) {
	reg := NewRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)

		go func() {
			defer wg.Done()
			reg.Register(&mockTool{name: "tool"})
		}()

		go func() {
			defer wg.Done()
			_ = reg.ListTools()
			_ = reg.ListByBackend("")
		}()
	}

	wg.Wait()
}

func TestGlobalRegistry(t *testing.T) {
	// Clean up
	for _, name := range List() {
		Unregister(name)
	}

	tool := &mockTool{name: "global-test", description: "global test tool"}
	Register(tool)

	_, ok := Get("global-test")
	if !ok {
		t.Error("Expected global tool to be registered")
	}

	if Count() == 0 {
		t.Error("Expected at least one registered tool")
	}

	list := List()
	if len(list) == 0 {
		t.Error("Expected at least one tool in list")
	}

	Unregister("global-test")

	_, ok = Get("global-test")
	if ok {
		t.Error("Expected global tool to be unregistered")
	}
}

func TestGlobalRegistry_MustGet(t *testing.T) {
	// Clean up
	for _, name := range List() {
		Unregister(name)
	}

	tool := &mockTool{name: "must-get-test"}
	Register(tool)

	got := MustGet("must-get-test")
	if got == nil {
		t.Error("Expected non-nil tool")
	}

	if got.Name() != "must-get-test" {
		t.Errorf("Expected name 'must-get-test', got %s", got.Name())
	}

	// Clean up
	Unregister("must-get-test")
}

func TestGlobalRegistry_MustGet_Panic(t *testing.T) {
	// Clean up
	for _, name := range List() {
		Unregister(name)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for missing tool")
		}
	}()

	_ = MustGet("nonexistent")
}

func TestRegistry_ReplaceTools(t *testing.T) {
	reg := NewRegistry()

	tool1 := &mockTool{name: "test", description: "v1"}
	reg.Register(tool1)

	got, _ := reg.Get("test")
	if got.Description() != "v1" {
		t.Errorf("Expected description 'v1', got %s", got.Description())
	}

	tool2 := &mockTool{name: "test", description: "v2"}
	reg.Register(tool2)

	got, _ = reg.Get("test")
	if got.Description() != "v2" {
		t.Errorf("Expected description 'v2', got %s", got.Description())
	}

	// Count should still be 1 (replacement, not addition)
	if reg.Count() != 1 {
		t.Errorf("Expected count 1, got %d", reg.Count())
	}
}

func TestGlobalRegistry_ListByBackend(t *testing.T) {
	// Clean up
	for _, name := range List() {
		Unregister(name)
	}

	Register(&mockTool{name: "sql1", backend: "postgres"})
	Register(&mockTool{name: "sql2", backend: "postgres"})
	Register(&mockTool{name: "api1", backend: "rest-api"})

	postgresTools := ListByBackend("postgres")
	if len(postgresTools) < 2 { // At least sql1 and sql2
		t.Errorf("Expected at least 2 postgres tools, got %d", len(postgresTools))
	}

	// Clean up
	for _, name := range List() {
		Unregister(name)
	}
}
