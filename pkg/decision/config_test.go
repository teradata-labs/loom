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

package decision_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// Review #410 minor c: `act_min < 0 || act_min > 1` is false for NaN, so
// `act_min: .nan` was accepted. NaN and ±Inf are rejected for both
// thresholds.
func TestValidateConfigRejectsNonFiniteThresholds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		band    *loomv1.DecisionBand
		wantErr string
	}{
		{"act_min NaN", &loomv1.DecisionBand{Site: "s", ActMin: math.NaN()}, "act_min"},
		{"act_min +Inf", &loomv1.DecisionBand{Site: "s", ActMin: math.Inf(1)}, "act_min"},
		{"act_min -Inf", &loomv1.DecisionBand{Site: "s", ActMin: math.Inf(-1)}, "act_min"},
		{"true_min NaN", &loomv1.DecisionBand{Site: "s", ActMin: 0.5, TrueMin: math.NaN()}, "true_min"},
		{"true_min +Inf", &loomv1.DecisionBand{Site: "s", ActMin: 0.5, TrueMin: math.Inf(1)}, "true_min"},
		{"act_min out of range", &loomv1.DecisionBand{Site: "s", ActMin: 1.5}, "act_min"},
		{"valid", &loomv1.DecisionBand{Site: "s", ActMin: 0.5, TrueMin: 0.3}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := decision.ValidateConfig(&loomv1.DecisionConfig{Provider: "mock", Bands: []*loomv1.DecisionBand{tt.band}})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// Review #410 minor c: act_min 0 on a live band acts on every answer. The
// proto defines act_min as "at or above this confidence the answer is used"
// and the judge docs describe acting on every answer as meaningful, so it
// is accepted with a warning rather than rejected.
func TestConfigWarningsFlagsLiveBandActingOnEverything(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		band *loomv1.DecisionBand
		want int
	}{
		{"live band act_min 0", &loomv1.DecisionBand{Site: "recall.rerank"}, 1},
		{"shadow band act_min 0", &loomv1.DecisionBand{Site: "recall.rerank", Shadow: true}, 0},
		{"live band act_min set", &loomv1.DecisionBand{Site: "recall.rerank", ActMin: 0.3}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &loomv1.DecisionConfig{Provider: "mock", Bands: []*loomv1.DecisionBand{tt.band}}
			require.NoError(t, decision.ValidateConfig(cfg), "a warning never rejects the config")
			got := decision.ConfigWarnings(cfg)
			require.Len(t, got, tt.want)
			if tt.want > 0 {
				assert.Contains(t, got[0], "recall.rerank")
				assert.Contains(t, got[0], "act_min is 0")
			}
		})
	}
	assert.Nil(t, decision.ConfigWarnings(nil))
}
