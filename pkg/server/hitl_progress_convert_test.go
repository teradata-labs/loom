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
// in-turn ask hold carries the joined reason of every asking hook, reaches
// context_json; the stored request's Kind, Summary, Params and ParamsTruncated
// reach kind, summary, params_json and params_truncated, so a parked card's
// per-item descriptors (each approval item's reason included) reach the client.
func TestConvertHITLRequestToProto(t *testing.T) {
	tests := []struct {
		name          string
		info          *agent.HITLRequestInfo
		wantNil       bool
		wantContext   map[string]interface{}
		wantParams    map[string]interface{}
		wantKind      string
		wantSummary   string
		wantTruncated bool
	}{
		{name: "nil", info: nil, wantNil: true},
		{
			name: "in-turn ask hold carries joined reason",
			info: &agent.HITLRequestInfo{
				RequestID:   "r1",
				Question:    `Approve tool call "export_csv"?`,
				RequestType: "approval",
				Priority:    "normal",
				Kind:        "approval",
				Summary:     "export_csv(v=w)",
				Params:      map[string]interface{}{"v": "w"},
				Timeout:     30 * time.Second,
				Context: map[string]interface{}{
					"reason": "review-approve-off-list-program; review-approve-external-destination",
				},
			},
			wantContext: map[string]interface{}{
				"reason": "review-approve-off-list-program; review-approve-external-destination",
			},
			wantParams:  map[string]interface{}{"v": "w"},
			wantKind:    "approval",
			wantSummary: "export_csv(v=w)",
		},
		{
			name: "parked card carries per-item descriptors with joined reason",
			info: &agent.HITLRequestInfo{
				RequestID:   "r-parked",
				Question:    "Approve 1 pending action(s)?",
				RequestType: "parked",
				Priority:    "normal",
				Kind:        "approval",
				Summary:     "export_csv(v=w)",
				Timeout:     time.Hour,
				Context:     map[string]interface{}{"kind": "parked"},
				Params: map[string]interface{}{
					"c-export": map[string]interface{}{
						"kind":   "approval",
						"tool":   "export_csv",
						"reason": "review-approve-off-list-program; review-approve-external-destination",
					},
				},
				ParamsTruncated: true,
			},
			wantContext: map[string]interface{}{"kind": "parked"},
			wantParams: map[string]interface{}{
				"c-export": map[string]interface{}{
					"kind":   "approval",
					"tool":   "export_csv",
					"reason": "review-approve-off-list-program; review-approve-external-destination",
				},
			},
			wantKind:      "approval",
			wantSummary:   "export_csv(v=w)",
			wantTruncated: true,
		},
		{
			name: "question leaves params_json empty",
			info: &agent.HITLRequestInfo{
				RequestID:   "r-q",
				Question:    "Which region?",
				RequestType: "input",
				Kind:        "question",
				Summary:     "Which region?",
			},
			wantKind:    "question",
			wantSummary: "Which region?",
		},
		{
			name: "unparseable param value drops params_json, keeps the rest",
			info: &agent.HITLRequestInfo{
				RequestID:   "r-bad",
				RequestType: "approval",
				Kind:        "approval",
				Params:      map[string]interface{}{"ch": make(chan int)},
			},
			wantKind: "approval",
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
			assert.Equal(t, tt.wantKind, got.Kind)
			assert.Equal(t, tt.wantSummary, got.Summary)
			assert.Equal(t, tt.wantTruncated, got.ParamsTruncated)

			if tt.wantContext == nil {
				assert.Empty(t, got.ContextJson)
			} else {
				var ctxMap map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(got.ContextJson), &ctxMap))
				assert.Equal(t, tt.wantContext, ctxMap)
			}

			if tt.wantParams == nil {
				assert.Empty(t, got.ParamsJson)
			} else {
				var params map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(got.ParamsJson), &params))
				assert.Equal(t, tt.wantParams, params)
			}
		})
	}
}
