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
