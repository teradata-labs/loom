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
	"fmt"
	"strconv"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// Rerank sites. Each replaces a generative call that scores a candidate list
// against a query with one Noul per candidate in a single request.
const (
	// SiteRecallRerank is graph-memory recall (agent.rerankMemories).
	SiteRecallRerank = "recall.rerank"
	// SiteToolSearchRerank is tool_search's LLM rerank (registry.rerankWithLLM).
	SiteToolSearchRerank = "tool_search.rerank"
	// SiteConversationRerank is segmented-memory conversation search.
	SiteConversationRerank = "conversation.rerank"
)

// Reference sources for rerank sites.
const (
	ReferenceSourceLLMRerank = "llm_rerank"
	ReferenceSourceBM25      = "bm25"
)

// MaxRerankCandidates bounds one request. The token guard would reject a
// larger request anyway; this keeps the state focused, which the vendor's
// own jaggedness list says matters.
const MaxRerankCandidates = 64

// maxRerankQueryRunes matches the 500-character truncation rerankMemories
// applies to the user message today.
const maxRerankQueryRunes = 500

// maxRerankCandidateRunes bounds each candidate's text in state.
const maxRerankCandidateRunes = 1000

// RerankFanOutKey is the state array whose items pair with the candidate
// questions (DecisionRequest.fan_out_key).
const RerankFanOutKey = "candidates"

// CandidateQuestionID is the question id for candidate index i.
func CandidateQuestionID(i int) string { return "c" + strconv.Itoa(i) }

// RerankRequest builds one request that asks, for every candidate, whether it
// bears on the query. Candidates beyond MaxRerankCandidates are dropped; the
// caller decides what to do with the tail (today: keep them, unranked).
func RerankRequest(site, query string, candidates []string) (*loomv1.DecisionRequest, error) {
	if len(candidates) == 0 {
		return nil, &decision.ValidationError{Field: "state.candidates", Msg: "no candidates"}
	}
	if len(candidates) > MaxRerankCandidates {
		candidates = candidates[:MaxRerankCandidates]
	}
	items := make([]any, 0, len(candidates))
	questions := make(map[string]*loomv1.DecisionQuestion, len(candidates))
	for i, c := range candidates {
		id := CandidateQuestionID(i)
		items = append(items, map[string]any{"id": id, "text": truncateRunes(c, maxRerankCandidateRunes)})
		questions[id] = decision.Noul(
			fmt.Sprintf("Does candidate %s contain information that bears on the query?", id),
			decision.WithCriteria(
				"the candidate is about the same subject, entity, or need as the query and would help answer it",
				"the candidate is about something else, or only shares incidental words with the query",
			),
		)
	}
	state := map[string]any{
		"query":      truncateRunes(query, maxRerankQueryRunes),
		"candidates": items,
	}
	req, err := decision.NewRequest(site, state, questions)
	if err != nil {
		return nil, err
	}
	// One question per candidate: a chunked request keeps only its own
	// candidates in state (decision.Chunked), so the provider never sees
	// more text than the questions it is asked.
	req.FanOutKey = RerankFanOutKey
	return req, nil
}

// RerankReference renders which candidate indexes the existing mechanism
// kept as one Reference per candidate question. n is the number of
// candidates the request carried.
func RerankReference(n int, kept []int, source string) map[string]decision.Reference {
	if n > MaxRerankCandidates {
		n = MaxRerankCandidates
	}
	keptSet := make(map[int]struct{}, len(kept))
	for _, i := range kept {
		keptSet[i] = struct{}{}
	}
	refs := make(map[string]decision.Reference, n)
	for i := 0; i < n; i++ {
		_, k := keptSet[i]
		refs[CandidateQuestionID(i)] = decision.Reference{Answer: strconv.FormatBool(k), Source: source}
	}
	return refs
}

// RerankReferenceSubjects is RerankReference with a subject per candidate
// (see DecisionShadowRecord.subject), so each row can be graded against an
// external truth later. subjects is index-aligned with the candidates; a
// shorter slice leaves the tail without a subject.
func RerankReferenceSubjects(n int, kept []int, source string, subjects []string) map[string]decision.Reference {
	refs := RerankReference(n, kept, source)
	for i := 0; i < n && i < len(subjects) && i < MaxRerankCandidates; i++ {
		ref := refs[CandidateQuestionID(i)]
		ref.Subject = subjects[i]
		refs[CandidateQuestionID(i)] = ref
	}
	return refs
}

// RerankActedReferences is the reference set for rows recorded when the site
// acted on the decider: each candidate's subject, and NotActed on every
// candidate the site kept without acting on its answer (RerankSelection's
// NotActed), so those rows say FALLBACK rather than claim a decider verdict
// the site did not apply.
func RerankActedReferences(n int, subjects []string, notActed []int) map[string]decision.Reference {
	refs := RerankSubjectsOnly(n, subjects)
	for _, i := range notActed {
		id := CandidateQuestionID(i)
		ref := refs[id]
		ref.NotActed = true
		refs[id] = ref
	}
	return refs
}

// RerankSubjectsOnly carries subjects with no reference answer, for rows
// recorded when the site acted on the decider and ran no other mechanism.
func RerankSubjectsOnly(n int, subjects []string) map[string]decision.Reference {
	if n > MaxRerankCandidates {
		n = MaxRerankCandidates
	}
	refs := make(map[string]decision.Reference, n)
	for i := 0; i < n && i < len(subjects); i++ {
		refs[CandidateQuestionID(i)] = decision.Reference{Subject: subjects[i]}
	}
	return refs
}

// RerankKeepProbability is the Noul probability at or above which a candidate
// counts as relevant in live mode.
const RerankKeepProbability = 0.5

// RerankContributed reports whether a live rerank outcome is worth acting on:
// at least one of n answers cleared the band and was acted on (uncertain
// counts RerankSelection.NotActed, so a confident drop of a cut candidate
// does not count). When every answer is uncertain
// the decider has said nothing, and the site is better off running its
// generative rerank than keeping every candidate. n is the caller's candidate
// count; like RerankKeptWithBand it is capped at MaxRerankCandidates, because
// uncertain counts only the candidates the request carried.
func RerankContributed(n, uncertain int) bool {
	n = min(n, MaxRerankCandidates)
	return n > 0 && uncertain < n
}

// RerankCut reports, per candidate the request carries, whether
// RerankRequest cut its text to maxRerankCandidateRunes. The decider judged
// a cut candidate on part of its text while the LLM rerank reads all of it.
func RerankCut(candidates []string) []bool {
	n := min(len(candidates), MaxRerankCandidates)
	cut := make([]bool, n)
	for i := 0; i < n; i++ {
		cut[i] = utf8.RuneCountInString(candidates[i]) > maxRerankCandidateRunes
	}
	return cut
}

// RerankSelection is the live-mode outcome of a rerank request.
type RerankSelection struct {
	// Kept holds the kept candidate indexes, in index order.
	Kept []int
	// NotActed holds the indexes kept without acting on the decider's
	// answer: no answer, an answer that did not clear the band, or a
	// confident drop of a cut candidate. Every one of them is also in Kept.
	NotActed []int
}

// RerankSelect is the live-mode selection for a fan-out band judged
// PER_QUESTION: a candidate is kept when its Noul says relevant, or when the
// answer does not clear the band (uncertain candidates are kept, because
// dropping a relevant memory costs an answer while keeping an irrelevant one
// costs tokens). cut (RerankCut, index-aligned; nil for none) marks
// candidates the request carried cut: one of those is kept on a confident
// yes but never dropped on its cut view, so a confident no on it counts as
// not acted on and the candidate is kept.
func RerankSelect(resp *loomv1.DecisionResponse, n int, band decision.Band, cut []bool) RerankSelection {
	var sel RerankSelection
	if resp == nil {
		return sel
	}
	if n > MaxRerankCandidates {
		n = MaxRerankCandidates
	}
	notActed := func(i int) {
		sel.Kept = append(sel.Kept, i)
		sel.NotActed = append(sel.NotActed, i)
	}
	for i := 0; i < n; i++ {
		id := CandidateQuestionID(i)
		a, err := decision.NoulOf(resp, id)
		if err != nil {
			notActed(i) // no answer: keep, never drop silently
			continue
		}
		switch {
		case !band.Confident(resp.Answers[id]):
			notActed(i)
		case band.IsTrue(a.Probability):
			sel.Kept = append(sel.Kept, i)
		case i < len(cut) && cut[i]:
			notActed(i) // a drop judged on a cut view: keep
		}
	}
	return sel
}

// RerankKeptWithBand is RerankSelect with no cut candidates, returning the
// kept indexes and how many were kept without acting on their answer.
func RerankKeptWithBand(resp *loomv1.DecisionResponse, n int, band decision.Band) (kept []int, uncertain int) {
	sel := RerankSelect(resp, n, band, nil)
	return sel.Kept, len(sel.NotActed)
}

// RerankKept returns the candidate indexes whose Noul probability is at or
// above threshold, in index order. It is the live-mode selection a call site
// applies when the router says to act.
func RerankKept(resp *loomv1.DecisionResponse, n int, threshold float64) []int {
	if resp == nil {
		return nil
	}
	if n > MaxRerankCandidates {
		n = MaxRerankCandidates
	}
	var kept []int
	for i := 0; i < n; i++ {
		a, err := decision.NoulOf(resp, CandidateQuestionID(i))
		if err != nil {
			continue
		}
		if a.Probability >= threshold {
			kept = append(kept, i)
		}
	}
	return kept
}
