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

	anthropic "github.com/anthropics/anthropic-sdk-go"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Both Bedrock paths must uphold the same wire law as the Anthropic client:
// user-role messages only ever enter through the coalesce helper, so the
// outgoing list never carries two consecutive user-role messages, and a
// tool_result followed by a skill-body sidecar shares one user message.

func TestAppendUserOrCoalesceConverse(t *testing.T) {
	text := func(s string) bedrocktypes.ContentBlock {
		return &bedrocktypes.ContentBlockMemberText{Value: s}
	}

	if got := appendUserOrCoalesceConverse(nil, nil); len(got) != 0 {
		t.Fatalf("empty blocks must be a no-op, got %d messages", len(got))
	}

	msgs := appendUserOrCoalesceConverse(nil, []bedrocktypes.ContentBlock{text("tool result")})
	if len(msgs) != 1 || msgs[0].Role != bedrocktypes.ConversationRoleUser {
		t.Fatalf("first append must create one user message, got %+v", msgs)
	}

	// A following sidecar coalesces into the SAME user message.
	msgs = appendUserOrCoalesceConverse(msgs, []bedrocktypes.ContentBlock{text("SKILL BODY")})
	if len(msgs) != 1 {
		t.Fatalf("sidecar after a user message must coalesce, got %d messages", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("coalesced user message must carry both blocks, got %d", len(msgs[0].Content))
	}

	// After an assistant message, a user block starts a NEW message.
	msgs = append(msgs, bedrocktypes.Message{Role: bedrocktypes.ConversationRoleAssistant})
	msgs = appendUserOrCoalesceConverse(msgs, []bedrocktypes.ContentBlock{text("next turn")})
	if len(msgs) != 3 || msgs[2].Role != bedrocktypes.ConversationRoleUser {
		t.Fatalf("after assistant, user blocks must open a new message, got %+v", msgs)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == bedrocktypes.ConversationRoleUser &&
			msgs[i-1].Role == bedrocktypes.ConversationRoleUser {
			t.Fatalf("consecutive user-role messages at index %d", i)
		}
	}
}

func TestAppendUserOrCoalesceSDK(t *testing.T) {
	if got := appendUserOrCoalesceSDK(nil, nil); len(got) != 0 {
		t.Fatalf("empty blocks must be a no-op, got %d messages", len(got))
	}

	msgs := appendUserOrCoalesceSDK(nil,
		[]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("tool result")})
	if len(msgs) != 1 || msgs[0].Role != anthropic.MessageParamRoleUser {
		t.Fatalf("first append must create one user message, got %+v", msgs)
	}

	msgs = appendUserOrCoalesceSDK(msgs,
		[]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("SKILL BODY")})
	if len(msgs) != 1 {
		t.Fatalf("sidecar after a user message must coalesce, got %d messages", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("coalesced user message must carry both blocks, got %d", len(msgs[0].Content))
	}

	msgs = append(msgs, anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant})
	msgs = appendUserOrCoalesceSDK(msgs,
		[]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("next turn")})
	if len(msgs) != 3 || msgs[2].Role != anthropic.MessageParamRoleUser {
		t.Fatalf("after assistant, user blocks must open a new message, got %+v", msgs)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == anthropic.MessageParamRoleUser &&
			msgs[i-1].Role == anthropic.MessageParamRoleUser {
			t.Fatalf("consecutive user-role messages at index %d", i)
		}
	}
}
