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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
)

// Reasoning models on Bedrock (DeepSeek, Qwen, ...) return reasoningContent
// blocks through Converse. reasoningContentText extracts the readable text so
// ChatConverse can surface it on LLMResponse.Thinking instead of silently
// dropping it (which made reasoning-heavy turns look like empty responses).
func TestReasoningContentText_TextBlock(t *testing.T) {
	rc := &bedrocktypes.ReasoningContentBlockMemberReasoningText{
		Value: bedrocktypes.ReasoningTextBlock{
			Text:      aws.String("the user asked for X, so I should call tool Y"),
			Signature: aws.String("sig"),
		},
	}
	assert.Equal(t, "the user asked for X, so I should call tool Y", reasoningContentText(rc))
}

// Redacted reasoning is encrypted bytes with no readable text — skipped.
func TestReasoningContentText_RedactedBlock(t *testing.T) {
	rc := &bedrocktypes.ReasoningContentBlockMemberRedactedContent{
		Value: []byte{0x01, 0x02},
	}
	assert.Empty(t, reasoningContentText(rc))
}

func TestReasoningContentText_Nil(t *testing.T) {
	assert.Empty(t, reasoningContentText(nil))
}
