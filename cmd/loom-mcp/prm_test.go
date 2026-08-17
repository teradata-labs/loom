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
