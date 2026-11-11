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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/sites"
)

// TestConfigWarningsFlagsEveryNonShadowBand pins that every band without
// shadow: true is warned about, whatever its act_min, naming its site. At a
// site that acts the warning says the site is LIVE; at a site that only
// records it says nothing acts there, because the Router still reports
// DECIDER rows for it.
func TestConfigWarningsFlagsEveryNonShadowBand(t *testing.T) {
	type want struct {
		site    string
		live    bool
		actMin0 bool // the act-on-everything note
		tighten bool // the tighten-only note
	}
	tests := []struct {
		name  string
		bands []*loomv1.DecisionBand
		want  []want // one warning per entry, in band order
	}{
		{
			name:  "shadow band warns nothing",
			bands: []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank, ActMin: 0.8, Shadow: true}},
		},
		{
			name:  "live band with act_min 0",
			bands: []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank}},
			want:  []want{{site: sites.SiteRecallRerank, live: true, actMin0: true}},
		},
		{
			name:  "live band with act_min 0.8",
			bands: []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank, ActMin: 0.8}},
			want:  []want{{site: sites.SiteRecallRerank, live: true}},
		},
		{
			name:  "live band with act_min 0.3",
			bands: []*loomv1.DecisionBand{{Site: sites.SiteMemoryExtract, ActMin: 0.3}},
			want:  []want{{site: sites.SiteMemoryExtract, live: true}},
		},
		{
			name: "gate sites say tighten-only unless mode is replace",
			bands: []*loomv1.DecisionBand{
				{Site: sites.SiteStageValidation, ActMin: 0.8},
				{Site: sites.SiteDebateConsensus, ActMin: 0.8, Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE},
			},
			want: []want{
				{site: sites.SiteStageValidation, live: true, tighten: true},
				{site: sites.SiteDebateConsensus, live: true},
			},
		},
		{
			name: "multiple non-shadow bands warn once each",
			bands: []*loomv1.DecisionBand{
				{Site: sites.SiteRecallRerank, ActMin: 0.8},
				{Site: sites.SiteToolSearchRerank, Shadow: true},
				{Site: sites.SiteToolSearchRerank + ".x", ActMin: 0},
				{Site: sites.SiteFailureKind, ActMin: 1},
			},
			want: []want{
				{site: sites.SiteRecallRerank, live: true},
				{site: sites.SiteToolSearchRerank + ".x"},
				{site: sites.SiteFailureKind},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &loomv1.DecisionConfig{Provider: decision.ProviderMock, Bands: tt.bands}
			require.NoError(t, decision.ValidateConfig(cfg), "rig: the config is valid; warnings are not errors")
			got := decision.ConfigWarnings(cfg)
			require.Len(t, got, len(tt.want))
			for i, w := range tt.want {
				assert.Contains(t, got[i], "("+w.site+")", "the warning names its site")
				if w.live {
					assert.Contains(t, got[i], "site "+w.site+" is LIVE")
					assert.Contains(t, got[i], "act on the site instead of the existing mechanism")
					assert.NotContains(t, got[i], "nothing acts")
				} else {
					assert.Contains(t, got[i], "recorded as DECIDER")
					assert.Contains(t, got[i], "nothing acts on decider answers")
					assert.NotContains(t, got[i], "LIVE")
				}
				assert.Equal(t, w.actMin0, strings.Contains(got[i], "act_min is 0"), "act-on-everything note")
				assert.Equal(t, w.tighten, strings.Contains(got[i], "tighten-only"), "tighten-only note")
			}
		})
	}
	assert.Nil(t, decision.ConfigWarnings(nil))
}

// TestConfigWarningsActingSitesMatchSites keeps decision.SiteActs in step
// with the site constants: every site whose call site acts on Outcome.Act()
// is LIVE, and the record-only sites are not.
func TestConfigWarningsActingSitesMatchSites(t *testing.T) {
	for _, site := range []string{
		sites.SiteRecallRerank, sites.SiteToolSearchRerank, sites.SiteMemoryExtract,
		sites.SiteWorkflowBranch, sites.SiteStageValidation, sites.SiteSwarmTieBreak,
		sites.SiteDebateConsensus, sites.SiteSkillRoute, "judge.quality",
	} {
		assert.True(t, decision.SiteActs(site), site)
	}
	for _, site := range []string{
		sites.SiteFailureKind, sites.SiteConversationRerank, sites.SiteAsk, "recal.rerank", "",
	} {
		assert.False(t, decision.SiteActs(site), site)
	}
}

// TestValidateConfigDoesNotLogWarnings pins that ValidateConfig stays silent
// on a config with warnings: it runs on several paths for one config, so a
// warning logged there repeated up to three times per agent. The agent logs
// ConfigWarnings once when it builds its decider.
func TestValidateConfigDoesNotLogWarnings(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	cfg := &loomv1.DecisionConfig{Provider: decision.ProviderMock, Bands: []*loomv1.DecisionBand{{Site: sites.SiteRecallRerank}}}
	require.NotEmpty(t, decision.ConfigWarnings(cfg), "rig: the config has a warning")
	for i := 0; i < 3; i++ {
		require.NoError(t, decision.ValidateConfig(cfg))
	}
	assert.Zero(t, logs.Len(), "ValidateConfig logs nothing")
}
