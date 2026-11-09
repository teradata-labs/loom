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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/sites"
)

// TestConfigWarningsFlagsEveryNonShadowBand pins that every band without
// shadow: true is warned about, whatever its act_min: no site in this release
// acts on a decider answer, so any non-shadow band makes shadow reports show
// DECIDER rows for decisions that were not made.
func TestConfigWarningsFlagsEveryNonShadowBand(t *testing.T) {
	tests := []struct {
		name      string
		bands     []*loomv1.DecisionBand
		wantSites []string // one warning per entry, in band order
	}{
		{
			name:  "shadow band warns nothing",
			bands: []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank, ActMin: 0.8, Shadow: true}},
		},
		{
			name:      "non-shadow band with act_min 0",
			bands:     []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank}},
			wantSites: []string{sites.SiteRecallRerank},
		},
		{
			name:      "non-shadow band with act_min 0.8",
			bands:     []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank, ActMin: 0.8}},
			wantSites: []string{sites.SiteRecallRerank},
		},
		{
			name: "multiple non-shadow bands warn once each",
			bands: []*loomv1.DecisionBand{
				{Site: sites.SiteRecallRerank, ActMin: 0.8},
				{Site: sites.SiteToolSearchRerank, Shadow: true},
				{Site: sites.SiteToolSearchRerank + ".x", ActMin: 0},
				{Site: sites.SiteFailureKind, ActMin: 1},
			},
			wantSites: []string{sites.SiteRecallRerank, sites.SiteToolSearchRerank + ".x", sites.SiteFailureKind},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &loomv1.DecisionConfig{Provider: decision.ProviderMock, Bands: tt.bands}
			require.NoError(t, decision.ValidateConfig(cfg), "rig: the config is valid; warnings are not errors")
			got := decision.ConfigWarnings(cfg)
			require.Len(t, got, len(tt.wantSites))
			for i, site := range tt.wantSites {
				assert.Contains(t, got[i], "("+site+")", "the warning names its site")
				assert.Contains(t, got[i], "recorded as DECIDER")
				assert.Contains(t, got[i], "nothing acts on decider answers")
				assert.Contains(t, got[i], "becomes live when the site is wired")
			}
		})
	}
	assert.Nil(t, decision.ConfigWarnings(nil))
}
