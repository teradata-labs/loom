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
package shuttle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
)

type countingToolRegistry struct{ searches int }

func (r *countingToolRegistry) Search(context.Context, *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error) {
	r.searches++
	return &loomv1.SearchToolsResponse{Results: []*loomv1.ToolSearchResult{{Tool: &loomv1.IndexedTool{
		Name: "file_read", Source: loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
	}}}}, nil
}

// TestDisableDynamicRegistration verifies that an executor with dynamic
// registration disabled never searches the catalog or registers a tool, on
// both the Execute fallback and the ResolveAndRegister path.
func TestDisableDynamicRegistration(t *testing.T) {
	tests := []struct {
		name string
		call func(*shuttle.Executor) error
	}{
		{"execute", func(e *shuttle.Executor) error {
			_, err := e.Execute(context.Background(), "file_read", map[string]interface{}{"path": "x"})
			return err
		}},
		{"resolve", func(e *shuttle.Executor) error {
			_, err := e.ResolveAndRegister(context.Background(), "file_read", nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := shuttle.NewRegistry()
			exec := shuttle.NewExecutor(reg)
			catalog := &countingToolRegistry{}
			exec.SetToolRegistry(catalog)
			exec.SetBuiltinToolProvider(builtin.NewProvider())
			exec.DisableDynamicRegistration()

			require.Error(t, tt.call(exec))
			assert.Zero(t, catalog.searches)
			assert.Zero(t, reg.Count())
		})
	}
}
