// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package factory

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/bedrockmantle"
)

// clearMantleEnv isolates each test from the developer's shell (AWS_REGION is
// commonly set, and would otherwise satisfy the region fallback).
func clearMantleEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"BEDROCK_MANTLE_REGION", "BEDROCK_MANTLE_ENDPOINT", "BEDROCK_MANTLE_API_KEY", "AWS_REGION"} {
		t.Setenv(k, "")
	}
}

func TestCreateBedrockMantleProvider_FromConfig(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{
		BedrockMantleRegion: "us-west-2",
		BedrockMantleAPIKey: "ABSKtest",
		BedrockMantleModel:  "anthropic.claude-haiku-4-5",
		Temperature:         1.0,
	})

	raw, err := f.createBedrockMantleProvider("")
	require.NoError(t, err)
	client, ok := raw.(*bedrockmantle.Client)
	require.True(t, ok, "expected *bedrockmantle.Client")
	assert.Equal(t, "bedrock-mantle", client.Name())
	assert.Equal(t, "anthropic.claude-haiku-4-5", client.Model())
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws/anthropic/v1/messages", client.Endpoint())
}

func TestCreateBedrockMantleProvider_DefaultModel(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{BedrockMantleRegion: "us-east-1", BedrockMantleAPIKey: "ABSKtest"})

	raw, err := f.createBedrockMantleProvider("")
	require.NoError(t, err)
	client := raw.(*bedrockmantle.Client)
	assert.Equal(t, bedrockmantle.DefaultModel, client.Model())
}

func TestCreateBedrockMantleProvider_ExplicitModelAndRoute(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{BedrockMantleRegion: "us-west-2", BedrockMantleAPIKey: "ABSKtest"})

	raw, err := f.createBedrockMantleProvider("google.gemma-4-e2b")
	require.NoError(t, err)
	client := raw.(*bedrockmantle.Client)
	assert.Equal(t, "google.gemma-4-e2b", client.Model())
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws/openai/v1/chat/completions", client.Endpoint())
}

func TestCreateBedrockMantleProvider_FromEnv(t *testing.T) {
	clearMantleEnv(t)
	t.Setenv("BEDROCK_MANTLE_REGION", "eu-west-1")
	t.Setenv("BEDROCK_MANTLE_API_KEY", "ABSKenv")
	f := NewProviderFactory(FactoryConfig{})

	raw, err := f.createBedrockMantleProvider("anthropic.claude-opus-4-7")
	require.NoError(t, err)
	client := raw.(*bedrockmantle.Client)
	assert.Equal(t, "https://bedrock-mantle.eu-west-1.api.aws", client.BaseURL())
}

func TestCreateBedrockMantleProvider_AWSRegionFallback(t *testing.T) {
	clearMantleEnv(t)
	t.Setenv("AWS_REGION", "us-east-2")
	f := NewProviderFactory(FactoryConfig{BedrockMantleAPIKey: "ABSKtest"})

	raw, err := f.createBedrockMantleProvider("")
	require.NoError(t, err)
	assert.Equal(t, "https://bedrock-mantle.us-east-2.api.aws", raw.(*bedrockmantle.Client).BaseURL())
}

func TestCreateBedrockMantleProvider_EndpointOverride(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{
		BedrockMantleEndpoint: "https://vpce-1.bedrock-mantle.us-west-2.vpce.amazonaws.com/",
		BedrockMantleAPIKey:   "ABSKtest",
	})

	raw, err := f.createBedrockMantleProvider("")
	require.NoError(t, err)
	assert.Equal(t, "https://vpce-1.bedrock-mantle.us-west-2.vpce.amazonaws.com", raw.(*bedrockmantle.Client).BaseURL())
}

func TestCreateBedrockMantleProvider_MissingRegion(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{BedrockMantleAPIKey: "ABSKtest"})

	_, err := f.createBedrockMantleProvider("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bedrock-mantle region not configured")
	assert.Contains(t, err.Error(), "BEDROCK_MANTLE_REGION")
	// llm.bedrock_mantle_region is not wired into looms config; do not suggest it.
	assert.NotContains(t, err.Error(), "llm.bedrock_mantle_region")
}

func TestCreateBedrockMantleProvider_ShortTermKeyRegionMismatch(t *testing.T) {
	clearMantleEnv(t)
	presigned := "https://bedrock.amazonaws.com/?Action=CallWithBearerToken&X-Amz-Credential=AKID%2F20261104%2Fus-east-1%2Fbedrock%2Faws4_request&Version=1"
	key := "bedrock-api-key-" + base64.StdEncoding.EncodeToString([]byte(presigned))
	f := NewProviderFactory(FactoryConfig{BedrockMantleRegion: "us-west-2", BedrockMantleAPIKey: key})

	_, err := f.createBedrockMantleProvider("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `different region than the endpoint region "us-west-2"`)
	assert.NotContains(t, err.Error(), "us-east-1")
}

func TestCreateBedrockMantleProvider_ExpandsEnvPlaceholders(t *testing.T) {
	clearMantleEnv(t)
	t.Setenv("MANTLE_TEST_KEY", "ABSKexpanded")
	t.Setenv("MANTLE_TEST_REGION", "us-west-2")
	f := NewProviderFactory(FactoryConfig{
		BedrockMantleRegion: "${MANTLE_TEST_REGION}",
		BedrockMantleAPIKey: "${MANTLE_TEST_KEY}",
	})

	raw, err := f.createBedrockMantleProvider("")
	require.NoError(t, err)
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws", raw.(*bedrockmantle.Client).BaseURL())
}

func TestCreateProvider_BedrockMantle(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{BedrockMantleRegion: "us-west-2", BedrockMantleAPIKey: "ABSKtest"})

	raw, err := f.CreateProvider("bedrock-mantle", "anthropic.claude-haiku-4-5")
	require.NoError(t, err)
	_, ok := raw.(*bedrockmantle.Client)
	assert.True(t, ok)
	assert.True(t, f.IsProviderAvailable("bedrock-mantle"))
}

func TestIsProviderAvailable_BedrockMantle_NoRegion(t *testing.T) {
	clearMantleEnv(t)
	f := NewProviderFactory(FactoryConfig{})
	assert.False(t, f.IsProviderAvailable("bedrock-mantle"))
}

func TestCreateBedrockMantleProvider_MaxTokensFromCatalog(t *testing.T) {
	clearMantleEnv(t)
	// No explicit MaxTokens: the catalog's bedrock-mantle namespace supplies
	// the per-model output cap (Haiku 4.5 = 64k).
	assert.Equal(t, 64_000, NewProviderFactory(FactoryConfig{}).resolveMaxOutput("bedrock-mantle", "anthropic.claude-haiku-4-5"))
	assert.Equal(t, fallbackMaxOutputTokens, NewProviderFactory(FactoryConfig{}).resolveMaxOutput("bedrock-mantle", "vendor.unknown"))
}
