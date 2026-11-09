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

package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/bedrockmantle"
)

// fakeAWSStaticCreds keeps the SigV4 credential chain off the network and
// off the developer's ~/.aws: static test credentials resolve locally.
func fakeAWSStaticCreds(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-access-key")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("BEDROCK_MANTLE_API_KEY", "")
	t.Setenv("LOOM_LLM_BEDROCK_MANTLE_MODEL", "")
	t.Setenv("BEDROCK_MANTLE_REGION", "")
	t.Setenv("AWS_REGION", "")
}

func TestAgentBuilder_WithBedrockMantleLLM_Defaults(t *testing.T) {
	fakeAWSStaticCreds(t)

	b := NewAgentBuilder().WithBedrockMantleLLM("us-west-2")
	require.NoError(t, b.err)

	client, ok := b.llm.(*bedrockmantle.Client)
	require.True(t, ok, "WithBedrockMantleLLM must install a *bedrockmantle.Client, got %T", b.llm)
	assert.Equal(t, "bedrock-mantle", client.Name())
	assert.Equal(t, DefaultBedrockMantleModel, client.Model())
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws", client.BaseURL())
}

func TestAgentBuilder_WithBedrockMantleLLM_ModelAndRegionFromEnv(t *testing.T) {
	fakeAWSStaticCreds(t)
	t.Setenv("LOOM_LLM_BEDROCK_MANTLE_MODEL", "openai.gpt-oss-120b")
	t.Setenv("BEDROCK_MANTLE_REGION", "us-east-1")

	b := NewAgentBuilder().WithBedrockMantleLLM("")
	require.NoError(t, b.err)

	client, ok := b.llm.(*bedrockmantle.Client)
	require.True(t, ok)
	assert.Equal(t, "openai.gpt-oss-120b", client.Model())
	assert.Equal(t, "https://bedrock-mantle.us-east-1.api.aws", client.BaseURL())
	assert.Equal(t, bedrockmantle.RouteV1, client.Route(), "gpt-oss is served on /v1")
}

func TestAgentBuilder_WithBedrockMantleLLM_AWSRegionFallback(t *testing.T) {
	fakeAWSStaticCreds(t)
	t.Setenv("AWS_REGION", "eu-west-1")

	b := NewAgentBuilder().WithBedrockMantleLLM("")
	require.NoError(t, b.err)
	assert.Equal(t, "https://bedrock-mantle.eu-west-1.api.aws", b.llm.(*bedrockmantle.Client).BaseURL())
}

// A construction failure must surface from Build, not be swallowed into the
// generic "LLM provider is required" message.
func TestAgentBuilder_WithBedrockMantleLLM_ErrorSurfacesInBuild(t *testing.T) {
	fakeAWSStaticCreds(t)

	b := NewAgentBuilder().WithBedrockMantleLLM("")
	require.Error(t, b.err)
	assert.Nil(t, b.llm)

	_, err := b.Build()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bedrock-mantle")
	assert.Contains(t, err.Error(), "region")
	assert.NotContains(t, err.Error(), "LLM provider is required")
}

// The first recorded error wins so a later helper cannot hide the root cause.
func TestAgentBuilder_RecordErrKeepsFirst(t *testing.T) {
	fakeAWSStaticCreds(t)

	b := NewAgentBuilder().WithBedrockMantleLLM("")
	first := b.err
	require.Error(t, first)

	b.WithBedrockMantleLLM("")
	assert.Same(t, first, b.err)
}
