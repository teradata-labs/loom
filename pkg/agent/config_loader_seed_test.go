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
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadAgentConfig_LLMSeed covers the agent YAML → proto LLMConfig path for
// the optional seed field. The distinction that matters is "key omitted" (nil,
// provider samples randomly) versus "seed: 0" (an explicit, usable seed value
// for providers such as Ollama).
func TestLoadAgentConfig_LLMSeed(t *testing.T) {
	tests := []struct {
		name     string
		llmYAML  string
		wantSeed *int64
	}{
		{
			name: "seed omitted leaves the proto field unset",
			llmYAML: `    provider: ollama
    model: llama3.1`,
			wantSeed: nil,
		},
		{
			name: "explicit nonzero seed is carried through",
			llmYAML: `    provider: ollama
    model: llama3.1
    seed: 42`,
			wantSeed: int64Ptr(42),
		},
		{
			name: "explicit zero seed is preserved, not treated as unset",
			llmYAML: `    provider: ollama
    model: llama3.1
    seed: 0`,
			wantSeed: int64Ptr(0),
		},
		{
			name: "negative seed is carried through",
			llmYAML: `    provider: ollama
    model: llama3.1
    seed: -7`,
			wantSeed: int64Ptr(-7),
		},
		{
			name: "seed beyond float64 exact range survives as int64",
			llmYAML: `    provider: ollama
    model: llama3.1
    seed: 9007199254740993`,
			wantSeed: int64Ptr(9007199254740993),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "agent:\n  name: seed-test\n  llm:\n" + tt.llmYAML + "\n"
			path := filepath.Join(t.TempDir(), "agent.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := LoadAgentConfig(path)
			require.NoError(t, err)
			require.NotNil(t, cfg.Llm)

			if tt.wantSeed == nil {
				assert.Nil(t, cfg.Llm.Seed, "seed must stay unset when the YAML key is absent")
				return
			}
			require.NotNil(t, cfg.Llm.Seed, "seed must be set when the YAML key is present")
			assert.Equal(t, *tt.wantSeed, *cfg.Llm.Seed)
		})
	}
}

// TestLoadAgentConfig_LLMSeed_K8sFormat covers the same field through the
// apiVersion/kind YAML shape, which reaches the proto via convertK8sToLegacy.
func TestLoadAgentConfig_LLMSeed_K8sFormat(t *testing.T) {
	tests := []struct {
		name     string
		seedLine string
		wantSeed *int64
	}{
		{name: "seed omitted", seedLine: "", wantSeed: nil},
		{name: "explicit zero seed", seedLine: "    seed: 0\n", wantSeed: int64Ptr(0)},
		{name: "explicit nonzero seed", seedLine: "    seed: 123\n", wantSeed: int64Ptr(123)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "apiVersion: loom/v1\nkind: Agent\nmetadata:\n  name: seed-test\n" +
				"spec:\n  llm:\n    provider: ollama\n    model: llama3.1\n" + tt.seedLine

			path := filepath.Join(t.TempDir(), "agent.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := LoadAgentConfig(path)
			require.NoError(t, err)
			require.NotNil(t, cfg.Llm)

			if tt.wantSeed == nil {
				assert.Nil(t, cfg.Llm.Seed)
				return
			}
			require.NotNil(t, cfg.Llm.Seed)
			assert.Equal(t, *tt.wantSeed, *cfg.Llm.Seed)
		})
	}
}

// TestLLMConfigSeed_ConversionRoundTrip verifies both conversion helpers keep
// the unset/zero distinction and hand back independent pointers rather than
// aliasing the source value.
func TestLLMConfigSeed_ConversionRoundTrip(t *testing.T) {
	t.Run("unset stays unset in both directions", func(t *testing.T) {
		pb, err := convertLLMConfigYAMLToProto(&LLMConfigYAML{Provider: "ollama"})
		require.NoError(t, err)
		assert.Nil(t, pb.Seed)

		back := convertProtoToLLMConfigYAML(pb)
		require.NotNil(t, back)
		assert.Nil(t, back.Seed)
	})

	t.Run("explicit zero survives the round trip", func(t *testing.T) {
		pb, err := convertLLMConfigYAMLToProto(&LLMConfigYAML{
			Provider: "ollama",
			Seed:     int64Ptr(0),
		})
		require.NoError(t, err)
		require.NotNil(t, pb.Seed)
		assert.Equal(t, int64(0), *pb.Seed)

		back := convertProtoToLLMConfigYAML(pb)
		require.NotNil(t, back)
		require.NotNil(t, back.Seed)
		assert.Equal(t, int64(0), *back.Seed)
	})

	t.Run("pointers are copied, not aliased", func(t *testing.T) {
		src := int64Ptr(5)
		pb, err := convertLLMConfigYAMLToProto(&LLMConfigYAML{
			Provider: "ollama",
			Seed:     src,
		})
		require.NoError(t, err)
		require.NotNil(t, pb.Seed)
		assert.NotSame(t, src, pb.Seed, "proto must not share the YAML struct's int64")

		*src = 6
		assert.Equal(t, int64(5), *pb.Seed, "mutating the source must not change the proto")
	})
}

func int64Ptr(v int64) *int64 { return &v }
