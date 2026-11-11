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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// A decision judge's band is live unless it says shadow: true, the same as an
// agent's. RegisterJudge logs the decision config warnings once per
// registration (evaluation builds a judge per call, so the constructor would
// repeat them on every Evaluate).
func TestJudgeServer_RegisterJudge_LogsDecisionConfigWarnings(t *testing.T) {
	tests := []struct {
		name      string
		bands     []*loomv1.DecisionBand
		wantWarns int
		wantSite  string
	}{
		{
			name:      "live judge band warns",
			bands:     []*loomv1.DecisionBand{{Site: "judge.dj-live", ActMin: 0.4}},
			wantWarns: 1,
			wantSite:  "judge.dj-live",
		},
		{
			name:      "shadow judge band is quiet",
			bands:     []*loomv1.DecisionBand{{Site: "judge.dj-live", ActMin: 0.4, Shadow: true}},
			wantWarns: 0,
		},
		{
			name:      "no bands is quiet",
			wantWarns: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.WarnLevel)
			s := NewJudgeServer(observability.NewNoOpTracer(), zap.New(core))

			_, err := s.RegisterJudge(context.Background(), &loomv1.RegisterJudgeRequest{Config: &loomv1.JudgeConfig{
				Id:   "dj-live",
				Name: "decision judge",
				Type: loomv1.JudgeType_JUDGE_TYPE_DECISION,
				Decision: &loomv1.DecisionConfig{
					Provider: "mock",
					Bands:    tt.bands,
				},
			}})
			require.NoError(t, err)

			warns := logs.FilterMessage("decision config").All()
			require.Len(t, warns, tt.wantWarns)
			if tt.wantWarns == 0 {
				return
			}
			fields := warns[0].ContextMap()
			assert.Equal(t, "dj-live", fields["judge_id"])
			assert.Contains(t, fields["warning"], tt.wantSite)
			assert.Contains(t, fields["warning"], "LIVE")
		})
	}
}
