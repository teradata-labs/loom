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
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProtectedResourceMetadata(t *testing.T) {
	t.Run("disabled without auth", func(t *testing.T) {
		t.Setenv("LOOM_SERVER_AUTH_ENABLED", "")
		t.Setenv("LOOM_MCP_RESOURCE_URL", "https://mcp.example.com")
		_, ok := buildProtectedResourceMetadata()
		assert.False(t, ok)
	})

	t.Run("disabled without resource URL", func(t *testing.T) {
		t.Setenv("LOOM_SERVER_AUTH_ENABLED", "true")
		t.Setenv("LOOM_SERVER_AUTH_SUPABASE_ISSUER", "https://auth.example.com")
		t.Setenv("LOOM_MCP_RESOURCE_URL", "")
		_, ok := buildProtectedResourceMetadata()
		assert.False(t, ok)
	})

	t.Run("explicit issuer", func(t *testing.T) {
		t.Setenv("LOOM_SERVER_AUTH_ENABLED", "true")
		t.Setenv("LOOM_SERVER_AUTH_SUPABASE_ISSUER", "https://auth.example.com")
		t.Setenv("LOOM_MCP_RESOURCE_URL", "https://mcp.example.com")
		payload, ok := buildProtectedResourceMetadata()
		require.True(t, ok)

		var md struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		require.NoError(t, json.Unmarshal(payload, &md))
		assert.Equal(t, "https://mcp.example.com", md.Resource)
		assert.Equal(t, []string{"https://auth.example.com"}, md.AuthorizationServers)
	})

	t.Run("issuer derived from project ref", func(t *testing.T) {
		t.Setenv("LOOM_SERVER_AUTH_ENABLED", "true")
		t.Setenv("LOOM_SERVER_AUTH_SUPABASE_ISSUER", "")
		t.Setenv("LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF", "abcd1234")
		t.Setenv("LOOM_MCP_RESOURCE_URL", "https://mcp.example.com")
		payload, ok := buildProtectedResourceMetadata()
		require.True(t, ok)
		assert.Contains(t, string(payload), "https://abcd1234.supabase.co/auth/v1")
	})
}

// TestResolveEdgeAudience (review finding 6, PR #328): accepted tokens must
// be bound to the advertised MCP resource; inconsistent configuration fails
// startup instead of validating against the wrong identity.
func TestResolveEdgeAudience(t *testing.T) {
	cases := []struct {
		name               string
		audience, resource string
		want               string
		wantErr            bool
	}{
		{"defaults without PRM", "", "", "authenticated", false},
		{"explicit audience without PRM", "my-aud", "", "my-aud", false},
		{"PRM binds the audience", "", "https://mcp.example.com/mcp", "https://mcp.example.com/mcp", false},
		{"matching explicit audience", "https://mcp.example.com/mcp", "https://mcp.example.com/mcp", "https://mcp.example.com/mcp", false},
		{"mismatch fails startup", "authenticated", "https://mcp.example.com/mcp", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveEdgeAudience(tc.audience, tc.resource)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
