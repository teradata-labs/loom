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
// This file adapts Loom's human-in-the-loop approval surface to the MRTR
// InputHandler contract, so MCP elicitations and Loom-native approvals share
// one policy surface instead of growing a parallel confirmation mechanism.
package manager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// ElicitationPrompter is the surface an approval gate (HITL) implements to
// answer a server's elicitation: present message and requested schema to a
// human or policy engine and return the outcome.
type ElicitationPrompter interface {
	// PromptElicitation returns accepted=false for an explicit decline;
	// a non-nil error aborts the whole MRTR exchange.
	PromptElicitation(ctx context.Context, message string, requestedSchema map[string]interface{}) (accepted bool, content map[string]interface{}, err error)
}

// elicitParams is the subset of ElicitRequest params the adapter presents.
type elicitParams struct {
	Message         string                 `json:"message"`
	RequestedSchema map[string]interface{} `json:"requestedSchema,omitempty"`
}

// elicitResult mirrors the spec's ElicitResult.
type elicitResult struct {
	Action  string                 `json:"action"` // "accept" or "decline"
	Content map[string]interface{} `json:"content,omitempty"`
}

// NewElicitationInputHandler builds an MRTR InputHandler that routes
// elicitation/create requests through the given prompter. Any other
// inputRequest method aborts the exchange: the client advertises only the
// elicitation capability, so a conformant server never sends sampling or
// roots requests, and receiving one is a protocol violation rather than
// something to silently improvise an answer for.
func NewElicitationInputHandler(prompter ElicitationPrompter) client.InputHandler {
	return func(ctx context.Context, reqs protocol.InputRequests) (protocol.InputResponses, error) {
		responses := make(protocol.InputResponses, len(reqs))
		for key, req := range reqs {
			if req.Method != "elicitation/create" {
				return nil, fmt.Errorf("unsupported inputRequest method %q (key %s): this client advertises elicitation only", req.Method, key)
			}
			var params elicitParams
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					return nil, fmt.Errorf("invalid elicitation params (key %s): %w", key, err)
				}
			}
			accepted, content, err := prompter.PromptElicitation(ctx, params.Message, params.RequestedSchema)
			if err != nil {
				return nil, fmt.Errorf("elicitation prompt failed (key %s): %w", key, err)
			}
			result := elicitResult{Action: "decline"}
			if accepted {
				result.Action = "accept"
				result.Content = content
			}
			resultJSON, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			responses[key] = resultJSON
		}
		return responses, nil
	}
}
