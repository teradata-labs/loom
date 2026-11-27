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
package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A temperature is sent only when one was configured. The OpenAI GPT models on
// Bedrock refuse any request that carries the field ("This model doesn't
// support the temperature field"), so a client that fills one in cannot reach
// them. These tests hold the rule on each of the three request shapes.

const (
	temperatureTestGPT    = "us.openai.gpt-5.4"
	temperatureTestClaude = "us.anthropic.claude-3-5-sonnet-20241022-v2:0"
)

// Converse: every non-Anthropic model.
func TestConverseSendsATemperatureOnlyWhenSet(t *testing.T) {
	unset := (&Client{modelID: temperatureTestGPT, maxTokens: 32000}).inferenceConfig()
	assert.Nil(t, unset.Temperature, "no temperature configured, none sent")
	assert.Equal(t, int32(32000), aws.ToInt32(unset.MaxTokens), "max tokens ride either way")

	set := (&Client{modelID: temperatureTestGPT, maxTokens: 32000, temperature: 0.7}).inferenceConfig()
	require.NotNil(t, set.Temperature, "a configured temperature is sent")
	assert.InDelta(t, 0.7, float64(*set.Temperature), 1e-6)
	assert.Equal(t, int32(32000), aws.ToInt32(set.MaxTokens))
}

// InvokeModel with the Anthropic Messages payload.
func TestInvokeModelSendsATemperatureOnlyWhenSet(t *testing.T) {
	messages := []map[string]interface{}{{"role": "user", "content": "hi"}}

	unset := (&Client{modelID: temperatureTestClaude, maxTokens: 4096}).messagesRequest(messages)
	assert.NotContains(t, unset, "temperature", "no temperature configured, none sent")
	assert.Equal(t, "bedrock-2023-05-31", unset["anthropic_version"])
	assert.Equal(t, 4096, unset["max_tokens"])
	assert.Equal(t, messages, unset["messages"])

	set := (&Client{modelID: temperatureTestClaude, maxTokens: 4096, temperature: 0.7}).messagesRequest(messages)
	assert.Equal(t, 0.7, set["temperature"], "a configured temperature is sent")
	assert.Equal(t, 4096, set["max_tokens"])
}

// The Anthropic SDK path: Claude. Thinking still drops a configured value.
func TestClaudeSendsATemperatureOnlyWhenSet(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		level       string
		temperature float64
		sent        bool
	}{
		{"unset, thinking off", "", 0, false},
		{"unset, thinking on", "low", 0, false},
		{"set, thinking off", "", 0.2, true},
		{"set, thinking on", "low", 0.2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &SDKClient{modelID: temperatureTestClaude, thinkingLevel: tc.level, maxTokens: 64000, temperature: tc.temperature}
			got := c.temperatureParam(ctx)
			require.Equal(t, tc.sent, got.Valid())
			if tc.sent {
				assert.Equal(t, tc.temperature, got.Value)
			}

			// What goes on the wire, not only what the helper returns.
			body, err := json.Marshal(anthropic.MessageNewParams{
				Model:       anthropic.Model(c.modelID),
				MaxTokens:   c.maxTokens,
				Temperature: got,
			})
			require.NoError(t, err)
			var wire map[string]interface{}
			require.NoError(t, json.Unmarshal(body, &wire))
			if tc.sent {
				assert.Equal(t, tc.temperature, wire["temperature"])
			} else {
				assert.NotContains(t, wire, "temperature")
			}
		})
	}
}

// Neither constructor fills a temperature in: what the caller left unset stays
// unset, and what it gave is kept.
func TestConstructorsFillNoTemperature(t *testing.T) {
	base := Config{Region: "us-west-2", AccessKeyID: "test", SecretAccessKey: "test"}

	converse, err := NewClient(base)
	require.NoError(t, err)
	assert.Zero(t, converse.temperature)

	sdk, err := NewSDKClient(base)
	require.NoError(t, err)
	assert.Zero(t, sdk.temperature)

	given := base
	given.Temperature = 0.7
	converse, err = NewClient(given)
	require.NoError(t, err)
	assert.Equal(t, 0.7, converse.temperature)

	sdk, err = NewSDKClient(given)
	require.NoError(t, err)
	assert.Equal(t, 0.7, sdk.temperature)
}

// The model decides which request shape is used; the rule holds on both.
func TestRoutedClientsFillNoTemperature(t *testing.T) {
	base := Config{Region: "us-west-2", AccessKeyID: "test", SecretAccessKey: "test"}

	base.ModelID = temperatureTestGPT
	p, err := NewClientForModel(base)
	require.NoError(t, err)
	gpt, ok := p.(*Client)
	require.True(t, ok, "a GPT id is served by the Converse client")
	assert.Nil(t, gpt.inferenceConfig().Temperature)

	base.ModelID = temperatureTestClaude
	p, err = NewClientForModel(base)
	require.NoError(t, err)
	claude, ok := p.(*SDKClient)
	require.True(t, ok, "a Claude id is served by the SDK client")
	assert.False(t, claude.temperatureParam(context.Background()).Valid())
}
