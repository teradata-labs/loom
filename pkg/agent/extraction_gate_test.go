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

//go:build fts5

package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/types"
)

// gateAgent builds an agent whose extraction would store exactly one memory
// if it ran, with the decision layer configured by band.
func gateAgent(t *testing.T, store memory.GraphMemoryStore, band []*loomv1.DecisionBand,
	dec decision.Decider, shadowStore decision.ShadowStore) (*Agent, *extractionMockLLM) {
	t.Helper()
	ctx := context.Background()
	extracted := ExtractedGraphData{
		Memories: []ExtractedMemory{{
			Content:    "The user's daughter is called Ada",
			Summary:    "daughter's name",
			MemoryType: "fact",
			Salience:   0.8,
		}},
	}
	body, err := json.Marshal(extracted)
	require.NoError(t, err)
	llm := &extractionMockLLM{response: string(body)}

	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "gate-session")
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "my daughter Ada starts school in September"})
	segMem.AddMessage(ctx, types.Message{Role: "assistant", Content: "Noted."})
	session.SegmentedMem = segMem

	var router *decision.Router
	if dec != nil {
		router = decision.NewRouter(dec, decision.WithBands(band))
	}
	a := &Agent{
		llm:                         llm,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled: true, EnableExtraction: true, MaxEntitiesPerExtraction: 10,
		},
		memory:              mem,
		config:              &Config{Name: "gate-agent"},
		decisionRouter:      router,
		decisionShadowStore: shadowStore,
	}
	a.decisionRecorder = decision.NewShadowRecorder(shadowStore, a.tracer)
	return a, llm
}

func extractLiveBand() []*loomv1.DecisionBand {
	return []*loomv1.DecisionBand{{
		Site: sites.SiteMemoryExtract, ActMin: 0.5, TrueMin: 0.5,
		Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE,
	}}
}

// The saving: a confident "nothing durable here" skips the generative
// extraction entirely.
func TestExtractionGateSkipsOnConfidentNo(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	dec := decisionmock.New().AnswerNoul(sites.QExtractDurable, 0.03)
	shadow := &memShadowStore{}
	a, llm := gateAgent(t, store, extractLiveBand(), dec, shadow)

	a.extractGraphMemoryAsync(context.Background(), "gate-session")

	assert.Equal(t, 0, llm.getCalls(), "the extraction call never happened")
	assert.Equal(t, 1, dec.CallCount(), "one cheap question instead")

	a.WaitDecisionShadows()
	rows, err := shadow.QueryShadow(context.Background(), decision.ShadowQuery{Site: sites.SiteMemoryExtract})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, rows[0].Path)
	assert.Equal(t, "false", rows[0].CandidateAnswer)
}

// The safety: anything other than a confident no still extracts.
func TestExtractionGateExtractsUnlessConfidentlyTold(t *testing.T) {
	for _, tt := range []struct {
		name string
		p    float64
	}{
		{"confident yes", 0.97},
		{"uncertain", 0.45},
		{"uncertain the other way", 0.55},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestGraphMemoryStore(t)
			dec := decisionmock.New().AnswerNoul(sites.QExtractDurable, tt.p)
			shadow := &memShadowStore{}
			a, llm := gateAgent(t, store, extractLiveBand(), dec, shadow)

			a.extractGraphMemoryAsync(context.Background(), "gate-session")
			assert.Equal(t, 1, llm.getCalls(), "extraction ran as it always did")

			a.WaitDecisionShadows()
			rows, err := shadow.QueryShadow(context.Background(), decision.ShadowQuery{Site: sites.SiteMemoryExtract})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, "true", rows[0].ReferenceAnswer,
				"the reference is what extraction actually stored, and it stored one")
			assert.Equal(t, sites.ReferenceSourceExtractionYield, rows[0].ReferenceSource)
		})
	}
}

// A shadow band changes nothing and still produces a labelled row.
func TestExtractionGateShadowRecordsAgainstYield(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	dec := decisionmock.New().AnswerNoul(sites.QExtractDurable, 0.02)
	shadow := &memShadowStore{}
	a, llm := gateAgent(t, store, nil, dec, shadow) // no band: shadow

	a.extractGraphMemoryAsync(context.Background(), "gate-session")
	assert.Equal(t, 1, llm.getCalls(), "a shadow band never skips the call")

	a.WaitDecisionShadows()
	rows, err := shadow.QueryShadow(context.Background(), decision.ShadowQuery{Site: sites.SiteMemoryExtract})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "false", rows[0].CandidateAnswer, "the decider said nothing durable")
	assert.Equal(t, "true", rows[0].ReferenceAnswer, "but extraction found something: a labelled disagreement")
}

// When extraction yields nothing, the reference says so — which is the row
// that tells us a skip would have been free.
func TestExtractionGateReferenceIsFalseWhenNothingStored(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	dec := decisionmock.New().AnswerNoul(sites.QExtractDurable, 0.9)
	shadow := &memShadowStore{}
	a, llm := gateAgent(t, store, nil, dec, shadow)
	llm.response = `{"entities":[],"relationships":[],"memories":[]}`

	a.extractGraphMemoryAsync(context.Background(), "gate-session")
	assert.Equal(t, 1, llm.getCalls())

	a.WaitDecisionShadows()
	rows, err := shadow.QueryShadow(context.Background(), decision.ShadowQuery{Site: sites.SiteMemoryExtract})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "false", rows[0].ReferenceAnswer, "extraction stored nothing")
	assert.Equal(t, "true", rows[0].CandidateAnswer, "the decider thought there was something")
}

// With no decision layer the extractor is exactly what it was.
func TestExtractionGateLayerOffIsUnchanged(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	a, llm := gateAgent(t, store, nil, nil, nil)

	a.extractGraphMemoryAsync(context.Background(), "gate-session")
	assert.Equal(t, 1, llm.getCalls())
	a.WaitDecisionShadows() // must not panic without a store
}

// The window carries the conversation and not tool payloads, which are
// neither durable nor safe to ship to a third party.
func TestRenderExtractionWindowDropsToolPayloads(t *testing.T) {
	t.Parallel()
	got := renderExtractionWindow([]types.Message{
		{Role: "user", Content: "my daughter Ada starts school"},
		{Role: "tool", Content: `{"rows":[{"ssn":"123-45-6789"}]}`},
		{Role: "assistant", Content: "Noted."},
		{Role: "user", Content: "   "},
	})
	assert.Contains(t, got, "[user] my daughter Ada starts school")
	assert.Contains(t, got, "[assistant] Noted.")
	assert.NotContains(t, got, "ssn", "tool payloads never reach the decider")
	assert.NotContains(t, got, "[user]   ", "blank turns are dropped")
}
