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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision/jev"
)

// The decider endpoint is a server-level setting: Validate refuses an
// insecure one, and applyDecisionServerConfig installs a valid one for the
// process.
func TestDecisionServerConfig(t *testing.T) {
	validBase := func() *Config {
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     LLMConfig{Provider: "ollama", OllamaEndpoint: "http://localhost:11434", OllamaModel: "test"},
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}
	prev := jev.CurrentServerEndpoint()
	t.Cleanup(func() { require.NoError(t, jev.SetServerEndpoint(prev)) })

	tests := []struct {
		name    string
		dec     DecisionServerConfig
		wantErr bool
	}{
		{name: "unset", dec: DecisionServerConfig{}},
		{name: "https", dec: DecisionServerConfig{BaseURL: "https://ai-gateway.vercel.sh/typesafe"}},
		{name: "http refused", dec: DecisionServerConfig{BaseURL: "http://gateway.example"}, wantErr: true},
		{name: "loopback http with override", dec: DecisionServerConfig{BaseURL: "http://127.0.0.1:4000", AllowInsecureHTTP: true}},
		{name: "remote http with override refused", dec: DecisionServerConfig{BaseURL: "http://10.1.2.3", AllowInsecureHTTP: true}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBase()
			cfg.Decision = tt.dec
			if tt.wantErr {
				assert.ErrorIs(t, cfg.Validate(), jev.ErrInsecureEndpoint)
				assert.ErrorIs(t, applyDecisionServerConfig(cfg), jev.ErrInsecureEndpoint)
				return
			}
			require.NoError(t, cfg.Validate())
			require.NoError(t, applyDecisionServerConfig(cfg))
			assert.Equal(t, tt.dec.BaseURL, jev.CurrentServerEndpoint().BaseURL)
			assert.Equal(t, tt.dec.AllowInsecureHTTP, jev.CurrentServerEndpoint().AllowInsecureHTTP)
		})
	}
}
