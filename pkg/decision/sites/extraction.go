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

package sites

import (
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteMemoryExtract gates graph-memory extraction
// (agent.extractGraphMemoryAsync). Extraction is a generative call over the
// recent conversation window, fired on a counter whether or not the window
// contains anything worth remembering. Here one Noul asks whether it does.
//
// The gate is one-sided by design: it may only *skip* an extraction, never
// force one. See ExtractVerdict.
const SiteMemoryExtract = "memory.extract"

// QExtractDurable is the single question id at SiteMemoryExtract.
const QExtractDurable = "durable"

// ReferenceSourceExtractionYield labels the reference taken from what
// extraction actually produced: whether the run that followed stored at
// least one memory. Unlike most references in this package that is observed
// truth rather than another model's opinion, which makes shadow rows here
// a labelled dataset rather than an agreement measurement.
const ReferenceSourceExtractionYield = "agent.extractGraphMemoryAsync.stored"

// maxExtractWindowRunes bounds the conversation window carried in state.
// The window is the thing being judged, so it gets room, but extraction
// prompts are already large and this request should stay cheap.
const maxExtractWindowRunes = 6000

// ExtractRequest asks whether a conversation window holds anything worth
// remembering. window is the rendered recent turns; the caller decides how
// many and in what form, and must not include tool payloads.
//
// It returns a *decision.ValidationError when the window is empty.
func ExtractRequest(window string) (*loomv1.DecisionRequest, error) {
	if window == "" {
		return nil, &decision.ValidationError{Field: "state.window", Msg: "empty window"}
	}
	q := decision.Noul(
		"Does this conversation window contain a durable fact worth remembering after the conversation ends?",
		decision.WithCriteria(
			"it states something lasting about the user, their situation, their preferences, "+
				"a decision they made, or a fact about their systems that would still be true tomorrow",
			"it is small talk, a transient status, a restatement of what the assistant just said, "+
				"or work-in-progress that carries no fact worth keeping",
		),
	)
	return decision.NewRequest(SiteMemoryExtract,
		map[string]any{"window": truncateRunes(window, maxExtractWindowRunes)},
		map[string]*loomv1.DecisionQuestion{QExtractDurable: q})
}

// ExtractVerdict reports whether the extraction may be skipped.
//
// skip is true only on a confident *no*. That polarity is the whole design:
//
//   - A false "nothing here" loses a memory permanently, because nothing
//     revisits that window.
//   - A false "something here" costs one extraction call that finds nothing,
//     which is exactly what happens today on every fire.
//
// So the gate is allowed to make the cheap mistake and not the expensive
// one. It also suits a decider that is readier to say yes than no: the
// answer it is reluctant to give is the only one that can lose data.
//
// ok is false when there is no usable answer, in which case the caller
// extracts as it always did.
func ExtractVerdict(resp *loomv1.DecisionResponse, band decision.Band) (skip bool, ok bool) {
	if resp == nil {
		return false, false
	}
	a, err := decision.NoulOf(resp, QExtractDurable)
	if err != nil || a == nil {
		return false, false
	}
	if !band.Confident(resp.Answers[QExtractDurable]) {
		return false, false
	}
	// Confident, so the only question is which side of the line it fell on.
	// Skip only when it says there is nothing durable here.
	return !band.IsTrue(a.Probability), true
}

// ExtractReference renders what extraction actually produced as the
// reference for a shadow row: true when the run stored at least one memory.
func ExtractReference(stored int) map[string]decision.Reference {
	return map[string]decision.Reference{
		QExtractDurable: {
			Answer: boolAnswer(stored > 0),
			Source: ReferenceSourceExtractionYield,
		},
	}
}

func boolAnswer(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
