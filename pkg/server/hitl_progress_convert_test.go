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
package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

// TestConvertHITLRequestToProto pins the gRPC card shape both progress streams
// (Weave and the multi-agent stream) share: the request's Context, which for an
// ask hold carries the joined reason of every asking hook, reaches context_json.
func TestConvertHITLRequestToProto(t *testing.T) {
	tests := []struct {
		name        string
		info        *agent.HITLRequestInfo
		wantNil     bool
		wantContext map[string]interface{}
	}{
		{name: "nil", info: nil, wantNil: true},
		{
			name: "ask hold carries joined reason",
			info: &agent.HITLRequestInfo{
				RequestID:   "r1",
				Question:    `Approve tool call "export_csv"?`,
				RequestType: "approval",
				Priority:    "normal",
				Timeout:     30 * time.Second,
				Context: map[string]interface{}{
					"kind":   "approval",
					"reason": "review-approve-off-list-program; review-approve-external-destination",
				},
			},
			wantContext: map[string]interface{}{
				"kind":   "approval",
				"reason": "review-approve-off-list-program; review-approve-external-destination",
			},
		},
		{
			name: "no context leaves context_json empty",
			info: &agent.HITLRequestInfo{RequestID: "r2", RequestType: "input"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertHITLRequestToProto(tt.info)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.info.RequestID, got.RequestId)
			assert.Equal(t, tt.info.Question, got.Question)
			assert.Equal(t, tt.info.RequestType, got.RequestType)
			assert.Equal(t, tt.info.Priority, got.Priority)
			assert.Equal(t, int32(tt.info.Timeout.Seconds()), got.TimeoutSeconds)
			if tt.wantContext == nil {
				assert.Empty(t, got.ContextJson)
				return
			}
			var ctxMap map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(got.ContextJson), &ctxMap))
			assert.Equal(t, tt.wantContext, ctxMap)
		})
	}
}
