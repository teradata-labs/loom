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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestNewUIResourceRegistry(t *testing.T) {
	registry := NewUIResourceRegistry()
	require.NotNil(t, registry)
	assert.Equal(t, 0, registry.Count())
}

func TestUIResourceRegistry_Register(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.Register(&UIResource{
		URI:      "ui://test/app",
		Name:     "Test App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>test</html>"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, registry.Count())
}

func TestUIResourceRegistry_Register_Nil(t *testing.T) {
	registry := NewUIResourceRegistry()
	err := registry.Register(nil)
	assert.Error(t, err)
}

func TestUIResourceRegistry_Register_EmptyURI(t *testing.T) {
	registry := NewUIResourceRegistry()
	err := registry.Register(&UIResource{URI: ""})
	assert.Error(t, err)
}

func TestUIResourceRegistry_Register_Duplicate(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.Register(&UIResource{
		URI:  "ui://test/app",
		Name: "App 1",
		HTML: []byte("<html>1</html>"),
	})
	require.NoError(t, err)

	err = registry.Register(&UIResource{
		URI:  "ui://test/app",
		Name: "App 2",
		HTML: []byte("<html>2</html>"),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
	assert.Equal(t, 1, registry.Count())
}

func TestUIResourceRegistry_List(t *testing.T) {
	registry := NewUIResourceRegistry()

	_ = registry.Register(&UIResource{
		URI:         "ui://test/viewer",
		Name:        "Viewer",
		Description: "A viewer app",
		MIMEType:    protocol.ResourceMIME,
		HTML:        []byte("<html>viewer</html>"),
	})
	_ = registry.Register(&UIResource{
		URI:         "ui://test/editor",
		Name:        "Editor",
		Description: "An editor app",
		MIMEType:    protocol.ResourceMIME,
		HTML:        []byte("<html>editor</html>"),
	})

	resources := registry.List()
	require.Len(t, resources, 2)

	// List() returns resources sorted by URI for deterministic ordering.
	// "ui://test/editor" < "ui://test/viewer" alphabetically.
	assert.Equal(t, "ui://test/editor", resources[0].URI)
	assert.Equal(t, "Editor", resources[0].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[0].MimeType)

	assert.Equal(t, "ui://test/viewer", resources[1].URI)
	assert.Equal(t, "Viewer", resources[1].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[1].MimeType)
}

func TestUIResourceRegistry_List_Empty(t *testing.T) {
	registry := NewUIResourceRegistry()
	resources := registry.List()
	assert.Empty(t, resources)
}

func TestUIResourceRegistry_Read(t *testing.T) {
	registry := NewUIResourceRegistry()

	htmlContent := "<html><body>Hello World</body></html>"
	_ = registry.Register(&UIResource{
		URI:      "ui://test/app",
		Name:     "Test App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte(htmlContent),
	})

	result, err := registry.Read("ui://test/app")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Contents, 1)

	assert.Equal(t, "ui://test/app", result.Contents[0].URI)
	assert.Equal(t, protocol.ResourceMIME, result.Contents[0].MimeType)
	assert.Equal(t, htmlContent, result.Contents[0].Text)
}

func TestUIResourceRegistry_Read_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, err := registry.Read("ui://test/nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUIResourceRegistry_Read_WithMeta(t *testing.T) {
	registry := NewUIResourceRegistry()

	prefersBorder := true
	_ = registry.Register(&UIResource{
		URI:      "ui://test/app",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>app</html>"),
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: &prefersBorder,
			Domain:        "test.example.com",
			CSP: &protocol.UIResourceCSP{
				ConnectDomains: []string{"https://api.example.com"},
			},
		},
	})

	result, err := registry.Read("ui://test/app")
	require.NoError(t, err)
	require.NotNil(t, result.Contents[0].Meta)

	meta := result.Contents[0].Meta
	assert.Equal(t, true, meta["prefersBorder"])
	assert.Equal(t, "test.example.com", meta["domain"])
	assert.NotNil(t, meta["csp"])
}

func TestUIResourceRegistry_Read_NilMeta(t *testing.T) {
	registry := NewUIResourceRegistry()

	_ = registry.Register(&UIResource{
		URI:      "ui://test/app",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>simple</html>"),
		Meta:     nil,
	})

	result, err := registry.Read("ui://test/app")
	require.NoError(t, err)
	assert.Nil(t, result.Contents[0].Meta)
}

func TestUIResourceRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewUIResourceRegistry()

	// Register some resources first
	for i := 0; i < 10; i++ {
		_ = registry.Register(&UIResource{
			URI:      "ui://test/app-" + string(rune('a'+i)),
			Name:     "App",
			MIMEType: protocol.ResourceMIME,
			HTML:     []byte("<html>test</html>"),
		})
	}

	// Concurrent reads
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uri := "ui://test/app-" + string(rune('a'+i%10))

			// Alternate between List and Read
			if i%2 == 0 {
				resources := registry.List()
				assert.Equal(t, 10, len(resources))
			} else {
				result, err := registry.Read(uri)
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		}(i)
	}
	wg.Wait()
}

func TestUIResourceRegistry_ConcurrentWriteRead(t *testing.T) {
	registry := NewUIResourceRegistry()

	var wg sync.WaitGroup
	// Concurrent writes (some will fail with "already registered" - that's expected)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = registry.Register(&UIResource{
				URI:      fmt.Sprintf("ui://test/concurrent-%d", i),
				Name:     fmt.Sprintf("App %d", i),
				MIMEType: protocol.ResourceMIME,
				HTML:     []byte("<html>test</html>"),
			})
		}(i)
	}
	// Concurrent reads while writes are happening
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = registry.List()
			_ = registry.Count()
			// Some reads will succeed, some will get "not found" - both are fine
			_, _ = registry.Read(fmt.Sprintf("ui://test/concurrent-%d", i%20))
		}(i)
	}
	wg.Wait()

	// All 20 unique URIs should be registered (Register rejects duplicates)
	assert.Equal(t, 20, registry.Count())
}

func TestUIResourceRegistry_Get(t *testing.T) {
	registry := NewUIResourceRegistry()

	_ = registry.Register(&UIResource{
		URI:      "ui://test/app",
		Name:     "Test App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>test</html>"),
	})

	res, err := registry.Get("ui://test/app")
	require.NoError(t, err)
	assert.Equal(t, "Test App", res.Name)
	assert.Equal(t, "ui://test/app", res.URI)
}

func TestUIResourceRegistry_Get_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, err := registry.Get("ui://test/nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUIResourceRegistry_AppNames(t *testing.T) {
	registry := NewUIResourceRegistry()

	_ = registry.Register(&UIResource{URI: "ui://loom/data-chart", Name: "Data Chart", HTML: []byte("a")})
	_ = registry.Register(&UIResource{URI: "ui://loom/conversation-viewer", Name: "Conv Viewer", HTML: []byte("b")})

	names := registry.AppNames()
	require.Len(t, names, 2)
	assert.Equal(t, "conversation-viewer", names[0])
	assert.Equal(t, "data-chart", names[1])
}

func TestUIResourceRegistry_AppNames_Empty(t *testing.T) {
	registry := NewUIResourceRegistry()
	names := registry.AppNames()
	assert.Empty(t, names)
}

func TestUIResourceRegistry_AppHTML(t *testing.T) {
	registry := NewUIResourceRegistry()

	expected := []byte("<html>chart</html>")
	_ = registry.Register(&UIResource{URI: "ui://loom/data-chart", Name: "Data Chart", HTML: expected})

	html, err := registry.AppHTML("data-chart")
	require.NoError(t, err)
	assert.Equal(t, expected, html)
}

func TestUIResourceRegistry_AppHTML_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, err := registry.AppHTML("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUIResourceRegistry_ListAppInfo(t *testing.T) {
	registry := NewUIResourceRegistry()

	prefersBorder := true
	_ = registry.Register(&UIResource{
		URI:         "ui://loom/data-chart",
		Name:        "Data Chart",
		Description: "Charts data",
		MIMEType:    protocol.ResourceMIME,
		HTML:        []byte("<html>chart</html>"),
		Meta:        &protocol.UIResourceMeta{PrefersBorder: &prefersBorder},
	})
	_ = registry.Register(&UIResource{
		URI:         "ui://loom/conversation-viewer",
		Name:        "Conversation Viewer",
		Description: "Views conversations",
		MIMEType:    protocol.ResourceMIME,
		HTML:        []byte("<html>viewer</html>"),
	})

	infos := registry.ListAppInfo()
	require.Len(t, infos, 2)

	// Sorted by name
	assert.Equal(t, "conversation-viewer", infos[0].Name)
	assert.Equal(t, "Conversation Viewer", infos[0].DisplayName)
	assert.Equal(t, "ui://loom/conversation-viewer", infos[0].URI)
	assert.False(t, infos[0].PrefersBorder)

	assert.Equal(t, "data-chart", infos[1].Name)
	assert.Equal(t, "Data Chart", infos[1].DisplayName)
	assert.True(t, infos[1].PrefersBorder)
}

func TestUIResourceRegistry_GetAppHTML(t *testing.T) {
	registry := NewUIResourceRegistry()

	prefersBorder := true
	expected := []byte("<html>chart</html>")
	_ = registry.Register(&UIResource{
		URI:         "ui://loom/data-chart",
		Name:        "Data Chart",
		Description: "Charts",
		MIMEType:    protocol.ResourceMIME,
		HTML:        expected,
		Meta:        &protocol.UIResourceMeta{PrefersBorder: &prefersBorder},
	})

	html, info, err := registry.GetAppHTML("data-chart")
	require.NoError(t, err)
	assert.Equal(t, expected, html)
	assert.Equal(t, "data-chart", info.Name)
	assert.Equal(t, "Data Chart", info.DisplayName)
	assert.True(t, info.PrefersBorder)
}

func TestUIResourceRegistry_GetAppHTML_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, _, err := registry.GetAppHTML("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtractAppName(t *testing.T) {
	tests := []struct {
		uri      string
		expected string
	}{
		{"ui://loom/data-chart", "data-chart"},
		{"ui://loom/conversation-viewer", "conversation-viewer"},
		{"ui://test/app", "app"},
		{"no-slash", "no-slash"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractAppName(tt.uri))
		})
	}
}

// --- Upsert Tests ---

func TestUIResourceRegistry_Upsert_Create(t *testing.T) {
	registry := NewUIResourceRegistry()

	res := &UIResource{
		URI:      "ui://loom/my-dynamic-app",
		Name:     "My Dynamic App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>dynamic</html>"),
		Dynamic:  true,
	}

	created, err := registry.Upsert(res)
	require.NoError(t, err)
	assert.True(t, created, "first upsert should report created=true")
	assert.Equal(t, 1, registry.Count())

	// Verify the resource was stored correctly
	got, err := registry.Get("ui://loom/my-dynamic-app")
	require.NoError(t, err)
	assert.Equal(t, "My Dynamic App", got.Name)
	assert.True(t, got.Dynamic)
}

func TestUIResourceRegistry_Upsert_Replace(t *testing.T) {
	registry := NewUIResourceRegistry()

	// Create initial dynamic resource
	res1 := &UIResource{
		URI:      "ui://loom/my-app",
		Name:     "My App v1",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>v1</html>"),
		Dynamic:  true,
	}
	created, err := registry.Upsert(res1)
	require.NoError(t, err)
	assert.True(t, created)

	// Replace with updated content
	res2 := &UIResource{
		URI:      "ui://loom/my-app",
		Name:     "My App v2",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>v2</html>"),
		Dynamic:  true,
	}
	created, err = registry.Upsert(res2)
	require.NoError(t, err)
	assert.False(t, created, "second upsert should report created=false (replaced)")
	assert.Equal(t, 1, registry.Count(), "replacing should not change count")

	// Verify the resource was updated
	got, err := registry.Get("ui://loom/my-app")
	require.NoError(t, err)
	assert.Equal(t, "My App v2", got.Name)
	assert.Equal(t, []byte("<html>v2</html>"), got.HTML)
}

func TestUIResourceRegistry_Upsert_RejectEmbedded(t *testing.T) {
	registry := NewUIResourceRegistry()

	// Register an embedded (non-dynamic) resource
	err := registry.Register(&UIResource{
		URI:      "ui://loom/embedded-app",
		Name:     "Embedded App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>embedded</html>"),
		Dynamic:  false,
	})
	require.NoError(t, err)

	// Attempt to overwrite with a dynamic resource via Upsert
	res := &UIResource{
		URI:      "ui://loom/embedded-app",
		Name:     "Overwritten App",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>overwritten</html>"),
		Dynamic:  true,
	}
	_, err = registry.Upsert(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot overwrite embedded resource")

	// Verify the original resource is unchanged
	got, err := registry.Get("ui://loom/embedded-app")
	require.NoError(t, err)
	assert.Equal(t, "Embedded App", got.Name)
}

func TestUIResourceRegistry_Upsert_RejectNonDynamic(t *testing.T) {
	registry := NewUIResourceRegistry()

	// Upsert requires Dynamic=true
	res := &UIResource{
		URI:      "ui://loom/static",
		Name:     "Static",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>static</html>"),
		Dynamic:  false,
	}
	_, err := registry.Upsert(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only dynamic resources can be upserted")
}

func TestUIResourceRegistry_Upsert_NilResource(t *testing.T) {
	registry := NewUIResourceRegistry()
	_, err := registry.Upsert(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource cannot be nil")
}

func TestUIResourceRegistry_Upsert_EmptyURI(t *testing.T) {
	registry := NewUIResourceRegistry()
	_, err := registry.Upsert(&UIResource{URI: "", Dynamic: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resource URI cannot be empty")
}

func TestUIResourceRegistry_Upsert_CapacityLimit(t *testing.T) {
	registry := NewUIResourceRegistry()
	// Override maxDynamic to a small number for testing
	registry.maxDynamic = 2

	for i := 0; i < 2; i++ {
		res := &UIResource{
			URI:      fmt.Sprintf("ui://loom/app-%d", i),
			Name:     fmt.Sprintf("App %d", i),
			MIMEType: protocol.ResourceMIME,
			HTML:     []byte("<html>app</html>"),
			Dynamic:  true,
		}
		_, err := registry.Upsert(res)
		require.NoError(t, err)
	}

	// Third should be rejected
	res := &UIResource{
		URI:      "ui://loom/app-3",
		Name:     "App 3",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>app</html>"),
		Dynamic:  true,
	}
	_, err := registry.Upsert(res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dynamic app limit reached")
	assert.Equal(t, 2, registry.Count(), "count should remain at limit")
}

func TestUIResourceRegistry_Upsert_BytesCapacityLimit(t *testing.T) {
	registry := NewUIResourceRegistry()
	// Set very small byte limit
	registry.maxTotalBytes = 100

	res1 := &UIResource{
		URI:      "ui://loom/app-1",
		Name:     "App 1",
		MIMEType: protocol.ResourceMIME,
		HTML:     make([]byte, 80), // 80 bytes
		Dynamic:  true,
	}
	_, err := registry.Upsert(res1)
	require.NoError(t, err)

	// Second should be rejected (80 + 30 = 110 > 100)
	res2 := &UIResource{
		URI:      "ui://loom/app-2",
		Name:     "App 2",
		MIMEType: protocol.ResourceMIME,
		HTML:     make([]byte, 30),
		Dynamic:  true,
	}
	_, err = registry.Upsert(res2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "total size limit reached")
}

// --- Delete Tests ---

func TestUIResourceRegistry_Delete_Dynamic(t *testing.T) {
	registry := NewUIResourceRegistry()

	res := &UIResource{
		URI:      "ui://loom/to-delete",
		Name:     "To Delete",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>bye</html>"),
		Dynamic:  true,
	}
	_, err := registry.Upsert(res)
	require.NoError(t, err)
	assert.Equal(t, 1, registry.Count())

	err = registry.Delete("ui://loom/to-delete")
	require.NoError(t, err)
	assert.Equal(t, 0, registry.Count())

	// Verify the resource is actually gone
	_, err = registry.Get("ui://loom/to-delete")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUIResourceRegistry_Delete_RejectEmbedded(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.Register(&UIResource{
		URI:      "ui://loom/embedded",
		Name:     "Embedded",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>embedded</html>"),
		Dynamic:  false,
	})
	require.NoError(t, err)

	err = registry.Delete("ui://loom/embedded")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete embedded resource")

	// Resource should still exist
	assert.Equal(t, 1, registry.Count())
}

func TestUIResourceRegistry_Delete_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.Delete("ui://loom/nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- SetOnChange Tests ---

func TestUIResourceRegistry_SetOnChange(t *testing.T) {
	registry := NewUIResourceRegistry()

	callCount := 0
	registry.SetOnChange(func() {
		callCount++
	})

	// Upsert should fire callback
	res := &UIResource{
		URI:      "ui://loom/callback-test",
		Name:     "Callback Test",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>test</html>"),
		Dynamic:  true,
	}
	_, err := registry.Upsert(res)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "onChange should fire after Upsert create")

	// Upsert again (replace) should fire callback again
	res2 := &UIResource{
		URI:      "ui://loom/callback-test",
		Name:     "Callback Test v2",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>test v2</html>"),
		Dynamic:  true,
	}
	_, err = registry.Upsert(res2)
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "onChange should fire after Upsert replace")

	// Delete should fire callback
	err = registry.Delete("ui://loom/callback-test")
	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "onChange should fire after Delete")

	// Register (the original method) does NOT fire onChange
	err = registry.Register(&UIResource{
		URI:     "ui://loom/register-test",
		Name:    "Register Test",
		HTML:    []byte("<html>reg</html>"),
		Dynamic: false,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "onChange should NOT fire after Register")
}

func TestUIResourceRegistry_SetOnChange_ReplaceCallback(t *testing.T) {
	registry := NewUIResourceRegistry()

	firstCalled := false
	registry.SetOnChange(func() {
		firstCalled = true
	})

	secondCalled := false
	registry.SetOnChange(func() {
		secondCalled = true
	})

	res := &UIResource{
		URI:      "ui://loom/replace-cb",
		Name:     "Replace CB",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>test</html>"),
		Dynamic:  true,
	}
	_, err := registry.Upsert(res)
	require.NoError(t, err)

	assert.False(t, firstCalled, "first callback should not be called after replacement")
	assert.True(t, secondCalled, "second callback should be called")
}

// --- CreateApp Tests ---

func TestUIResourceRegistry_CreateApp(t *testing.T) {
	registry := NewUIResourceRegistry()

	info, overwritten, err := registry.CreateApp("test-app", "Test App", "A test app", []byte("<html>test</html>"), false)
	require.NoError(t, err)
	assert.False(t, overwritten)
	require.NotNil(t, info)
	assert.Equal(t, "test-app", info.Name)
	assert.Equal(t, "ui://loom/test-app", info.URI)
	assert.Equal(t, "Test App", info.DisplayName)
	assert.Equal(t, "A test app", info.Description)
	assert.True(t, info.Dynamic)
	assert.True(t, info.PrefersBorder)
	assert.Equal(t, protocol.ResourceMIME, info.MimeType)

	// Verify it's in the registry
	assert.Equal(t, 1, registry.Count())
	got, err := registry.Get("ui://loom/test-app")
	require.NoError(t, err)
	assert.True(t, got.Dynamic)
}

func TestUIResourceRegistry_CreateApp_DuplicateWithoutOverwrite(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, _, err := registry.CreateApp("test-app", "Test App", "desc", []byte("<html>v1</html>"), false)
	require.NoError(t, err)

	// Second create without overwrite should fail
	_, _, err = registry.CreateApp("test-app", "Test App v2", "desc", []byte("<html>v2</html>"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestUIResourceRegistry_CreateApp_DuplicateWithOverwrite(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, _, err := registry.CreateApp("test-app", "Test App", "desc", []byte("<html>v1</html>"), false)
	require.NoError(t, err)

	// With overwrite=true, should succeed
	info, _, err := registry.CreateApp("test-app", "Test App v2", "desc v2", []byte("<html>v2</html>"), true)
	require.NoError(t, err)
	assert.Equal(t, "Test App v2", info.DisplayName)
	assert.Equal(t, 1, registry.Count())
}

func TestUIResourceRegistry_CreateApp_RejectOverwriteEmbedded(t *testing.T) {
	registry := NewUIResourceRegistry()

	// Register an embedded app
	err := registry.Register(&UIResource{
		URI:      "ui://loom/embedded",
		Name:     "Embedded",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>embedded</html>"),
		Dynamic:  false,
	})
	require.NoError(t, err)

	// CreateApp should not overwrite embedded, even without overwrite flag
	_, _, err = registry.CreateApp("embedded", "New", "desc", []byte("<html>new</html>"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot overwrite embedded app")
}

// --- UpdateApp Tests ---

func TestUIResourceRegistry_UpdateApp(t *testing.T) {
	registry := NewUIResourceRegistry()

	// First create a dynamic app
	_, _, err := registry.CreateApp("my-app", "My App", "Original desc", []byte("<html>v1</html>"), false)
	require.NoError(t, err)

	// Update it
	info, err := registry.UpdateApp("my-app", "My App Updated", "Updated desc", []byte("<html>v2</html>"))
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "My App Updated", info.DisplayName)
	assert.Equal(t, "Updated desc", info.Description)
	assert.True(t, info.Dynamic)

	// Verify stored content
	html, _, err := registry.GetAppHTML("my-app")
	require.NoError(t, err)
	assert.Equal(t, []byte("<html>v2</html>"), html)
}

func TestUIResourceRegistry_UpdateApp_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, err := registry.UpdateApp("nonexistent", "Name", "Desc", []byte("<html>x</html>"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUIResourceRegistry_UpdateApp_KeepsExistingFields(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, _, err := registry.CreateApp("my-app", "Original Name", "Original Desc", []byte("<html>v1</html>"), false)
	require.NoError(t, err)

	// Update with empty displayName and description -- should keep existing
	info, err := registry.UpdateApp("my-app", "", "", []byte("<html>v2</html>"))
	require.NoError(t, err)
	assert.Equal(t, "Original Name", info.DisplayName)
	assert.Equal(t, "Original Desc", info.Description)
}

func TestUIResourceRegistry_UpdateApp_RejectEmbedded(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.Register(&UIResource{
		URI:      "ui://loom/embedded",
		Name:     "Embedded",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>embedded</html>"),
		Dynamic:  false,
	})
	require.NoError(t, err)

	_, err = registry.UpdateApp("embedded", "New", "Desc", []byte("<html>new</html>"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot update embedded app")
}

// --- DeleteApp Tests ---

func TestUIResourceRegistry_DeleteApp(t *testing.T) {
	registry := NewUIResourceRegistry()

	_, _, err := registry.CreateApp("my-app", "My App", "desc", []byte("<html>v1</html>"), false)
	require.NoError(t, err)
	assert.Equal(t, 1, registry.Count())

	err = registry.DeleteApp("my-app")
	require.NoError(t, err)
	assert.Equal(t, 0, registry.Count())
}

func TestUIResourceRegistry_DeleteApp_NotFound(t *testing.T) {
	registry := NewUIResourceRegistry()

	err := registry.DeleteApp("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Concurrent Upsert/Delete Tests ---

func TestUIResourceRegistry_ConcurrentUpsertDelete(t *testing.T) {
	registry := NewUIResourceRegistry()

	var wg sync.WaitGroup
	// Concurrent upserts
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := &UIResource{
				URI:      fmt.Sprintf("ui://loom/concurrent-%d", i),
				Name:     fmt.Sprintf("App %d", i),
				MIMEType: protocol.ResourceMIME,
				HTML:     []byte("<html>test</html>"),
				Dynamic:  true,
			}
			_, _ = registry.Upsert(res)
		}(i)
	}

	// Concurrent deletes (some will fail with not found, that is expected)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = registry.Delete(fmt.Sprintf("ui://loom/concurrent-%d", i))
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = registry.List()
			_ = registry.Count()
			_, _ = registry.Get(fmt.Sprintf("ui://loom/concurrent-%d", i%20))
		}(i)
	}

	wg.Wait()
	// The race detector is the primary assertion here
}

func TestUIResourceRegistry_ConcurrentNewMethods(t *testing.T) {
	registry := NewUIResourceRegistry()

	prefersBorder := true
	_ = registry.Register(&UIResource{
		URI:      "ui://loom/data-chart",
		Name:     "Data Chart",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>chart</html>"),
		Meta:     &protocol.UIResourceMeta{PrefersBorder: &prefersBorder},
	})
	_ = registry.Register(&UIResource{
		URI:      "ui://loom/conversation-viewer",
		Name:     "Conv Viewer",
		MIMEType: protocol.ResourceMIME,
		HTML:     []byte("<html>viewer</html>"),
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 5 {
			case 0:
				names := registry.AppNames()
				assert.Len(t, names, 2)
			case 1:
				_, err := registry.AppHTML("data-chart")
				assert.NoError(t, err)
			case 2:
				infos := registry.ListAppInfo()
				assert.Len(t, infos, 2)
			case 3:
				_, _, err := registry.GetAppHTML("data-chart")
				assert.NoError(t, err)
			case 4:
				_, err := registry.Get("ui://loom/data-chart")
				assert.NoError(t, err)
			}
		}(i)
	}
	wg.Wait()
}
