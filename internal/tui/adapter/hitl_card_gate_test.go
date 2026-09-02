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
package adapter

import (
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Only an ANSWERABLE card raises the dialog. The two other shapes that ride the
// HITL stage — a hold heartbeat and the pre-creation ping — must not, because a
// dialog without a request_id cannot deliver its answer: it dead-ends on "no
// communication channel available" while the hold behind it sits pending until
// it times out.
func TestIsAnswerableHITLCard(t *testing.T) {
	tests := []struct {
		name     string
		progress *loomv1.WeaveProgress
		want     bool
	}{
		{
			name: "card with a request id is answerable",
			progress: &loomv1.WeaveProgress{
				Stage:       loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP,
				HitlRequest: &loomv1.HITLRequestInfo{RequestId: "req-1", Question: "Approve?"},
			},
			want: true,
		},
		{
			name: "heartbeat carries no request at all",
			progress: &loomv1.WeaveProgress{
				Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP,
				Message: "Still waiting for human response",
			},
			want: false,
		},
		{
			name: "pre-creation ping carries a request with an empty id",
			progress: &loomv1.WeaveProgress{
				Stage:       loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP,
				HitlRequest: &loomv1.HITLRequestInfo{Question: "Approve?", RequestType: "approval"},
			},
			want: false,
		},
		{
			name: "an id on another stage is not a HITL card",
			progress: &loomv1.WeaveProgress{
				Stage:       loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
				HitlRequest: &loomv1.HITLRequestInfo{RequestId: "req-1"},
			},
			want: false,
		},
		{
			name:     "nil progress does not panic",
			progress: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAnswerableHITLCard(tt.progress); got != tt.want {
				t.Errorf("IsAnswerableHITLCard() = %v, want %v", got, tt.want)
			}
		})
	}
}
