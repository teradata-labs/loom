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
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Lesson mining (the ledger-grounded lane). Prose mid-conversation is where
// beliefs live: a fleet study measured per-turn extraction minting the WRONG
// error theory 58 times against the correct fix 8 — agents memorizing
// unverified hypotheses at high salience, then recalling themselves into the
// failure loop. Lessons therefore come from the one record beliefs cannot
// reach: the conversation's own tool ledger. A lesson candidate exists only
// where the ledger shows an error followed by a succeeding retry of the same
// kind of call — the fix demonstrably worked before the extractor ever sees
// it. This pass runs ONCE, at conversation end, over the full transcript.
const (
	// maxLessonPairs bounds the mining prompt; the highest-value transitions
	// are the earliest distinct ones.
	maxLessonPairs = 6
	// lessonInputPreview / lessonErrorPreview bound quoted ledger excerpts.
	lessonInputPreview = 400
	lessonErrorPreview = 300
	// lessonExtractionTimeout bounds the single mining LLM call.
	lessonExtractionTimeout = 30 * time.Second
	// lessonSalienceFloor: a verified lesson is always high-salience.
	lessonSalienceFloor = 0.8
	// lessonRecallBudget bounds tokens for one lesson-lane recall; lessons
	// are single sentences, so this is generous.
	lessonRecallBudget = 2000
)

// minedEvent is one tool execution as the miner sees it. Class key and
// previews are computed AT EXECUTION TIME: the compiled message view evicts
// old turns (measured: the long early overflow fight produced 3 lessons from
// the board's most common error while short late skirmishes produced 55), so
// the miner keeps its own ledger instead of trusting messages at the end.
type minedEvent struct {
	name    string
	key     string
	input   string
	ok      bool
	errText string
}

// maxLedgerEvents caps a session's mining ledger.
const maxLedgerEvents = 300

// fleetLessonAgentID is the shared partition verified lessons live in when
// fleet_lesson_sharing is enabled. Method knowledge is the one memory class
// that is inherently shareable: pass-7 measurement showed correct lessons
// minted into per-agent scopes were structurally invisible to 9 of 12 fleet
// agents. Sharing is opt-in (GraphMemoryConfig.fleet_lesson_sharing): a
// shared partition means one agent's mining shapes every agent's context,
// which is a policy decision the operator has to make, not a default.
const fleetLessonAgentID = "__fleet_lessons__"

// lessonPartition returns the graph-memory agent ID lessons are stored under
// and recalled from: the shared fleet partition when sharing is opted in,
// otherwise the agent's own partition (private lessons).
func (a *Agent) lessonPartition() string {
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.GetFleetLessonSharing() {
		return fleetLessonAgentID
	}
	return a.config.Name
}

// maxFleetLessons caps the dedicated lesson lane in the injected context.
const maxFleetLessons = 5

// maxErrorLessonInjections caps error-triggered lesson injections per
// session: enough to cover distinct failure modes, small enough that a
// flailing conversation doesn't fill its context with repeated lessons.
const maxErrorLessonInjections = 5

// errorLessonSession tracks which lessons a session has already been shown
// through the error-triggered path (so a repeated failure re-injects
// nothing) and where in the tool ledger each injection landed (so outcome
// credit can score what happened after it).
type errorLessonSession struct {
	injected   map[string]int // lesson memory ID → ledger length at injection
	injections int
}

// ledgerLen returns the session's current tool-ledger length — the
// injection position outcome credit scores against.
func (a *Agent) ledgerLen(sessionID string) int {
	a.toolLedgerMu.Lock()
	defer a.toolLedgerMu.Unlock()
	return len(a.toolLedgers[sessionID])
}

// takeErrorLessonState removes and returns the session's injection record
// (consumed exactly once by the end-of-conversation credit pass).
func (a *Agent) takeErrorLessonState(sessionID string) map[string]int {
	a.errorLessonMu.Lock()
	defer a.errorLessonMu.Unlock()
	st := a.errorLessonState[sessionID]
	delete(a.errorLessonState, sessionID)
	if st == nil {
		return nil
	}
	return st.injected
}

// errorLessonQuery reduces raw tool-error text to FTS-safe bareword terms.
// Error text carries punctuation, quotes, and parentheses that FTS5 parses
// as syntax; only alphanumeric/underscore words survive. Terms are deduped
// case-insensitively and capped GENEROUSLY: real errors arrive wrapped in
// transport boilerplate ("tool error ... db_error ... [Version ...] [Session
// ...]"), and a tight cap fills with wrapper words before the message that
// names the actual failure — measured live: "Numeric overflow occurred"
// never survived a 12-term cap on a Teradata error, so the lane recalled
// only generic lessons.
func errorLessonQuery(errText string) string {
	const maxTerms = 24
	var terms []string
	seen := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		defer cur.Reset()
		if cur.Len() < 3 { // 1-2 char fragments only add noise
			return
		}
		key := strings.ToLower(cur.String())
		if seen[key] {
			return
		}
		seen[key] = true
		terms = append(terms, cur.String())
	}
	for _, r := range errText {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
			continue
		}
		flush()
		if len(terms) >= maxTerms {
			return strings.Join(terms, " ")
		}
	}
	flush()
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	return strings.Join(terms, " ")
}

// injectErrorLessons is the error-triggered recall lane: when a tool batch
// contained failures, the lesson partition is re-queried with the ERROR
// text — not the task text — and any not-yet-seen matches are appended to
// the conversation before the model's retry turn. This closes the gap the
// conversation-start lane cannot: a lesson whose trigger is an error that
// has not happened yet shares no vocabulary with the task, so it can only
// be delivered at failure time. Called after the whole tool batch is
// appended (never between a tool_use and its tool_result).
func (a *Agent) injectErrorLessons(ctx context.Context, session *types.Session, errTexts []string) {
	if a.graphMemoryStore == nil || len(errTexts) == 0 {
		return
	}
	if a.graphMemoryConfig != nil && !a.graphMemoryConfig.Enabled {
		return
	}

	query := errorLessonQuery(strings.Join(errTexts, " "))
	if query == "" {
		return
	}
	lessons := a.fleetLessons(ctx, query, lessonRecallBudget)
	if len(lessons) == 0 {
		return
	}

	ledgerPos := a.ledgerLen(session.ID)

	a.errorLessonMu.Lock()
	if a.errorLessonState == nil {
		a.errorLessonState = map[string]*errorLessonSession{}
	}
	st := a.errorLessonState[session.ID]
	if st == nil {
		st = &errorLessonSession{injected: map[string]int{}}
		a.errorLessonState[session.ID] = st
	}
	if st.injections >= maxErrorLessonInjections {
		a.errorLessonMu.Unlock()
		return
	}
	var fresh []*memory.Memory
	for _, m := range lessons {
		if _, seen := st.injected[m.ID]; !seen {
			st.injected[m.ID] = ledgerPos
			fresh = append(fresh, m)
		}
	}
	if len(fresh) > 0 {
		st.injections++
	}
	a.errorLessonMu.Unlock()

	if len(fresh) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("[Verified Lessons — matched to the error just hit]\n")
	sb.WriteString("These fixes were observed to succeed on this error in prior work:\n")
	for _, m := range fresh {
		sb.WriteString("- ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	session.AddMessage(ctx, types.Message{
		Role:    "system",
		Content: sb.String(),
	})
}

// clearErrorLessonState drops the session's error-injection tracking
// (paired with ledger teardown at conversation end).
func (a *Agent) clearErrorLessonState(sessionID string) {
	a.errorLessonMu.Lock()
	delete(a.errorLessonState, sessionID)
	a.errorLessonMu.Unlock()
}

// recordToolLedger appends one execution to the session's mining ledger.
// Called from the conversation loop when the tool result lands. Previews are
// captured immediately — the params map can be mutated by later machinery.
func (a *Agent) recordToolLedger(sessionID string, tc types.ToolCall, result *shuttle.Result) {
	if !a.enableGraphMemoryExtraction || result == nil {
		return
	}
	ev := minedEvent{
		name:  tc.Name,
		key:   eventClass(tc),
		input: eventInputPreview(tc),
		ok:    result.Success && result.Error == nil,
	}
	if result.Error != nil {
		ev.errText = truncate(result.Error.Message, lessonErrorPreview)
	}
	a.toolLedgerMu.Lock()
	if a.toolLedgers == nil {
		a.toolLedgers = map[string][]minedEvent{}
	}
	if len(a.toolLedgers[sessionID]) < maxLedgerEvents {
		a.toolLedgers[sessionID] = append(a.toolLedgers[sessionID], ev)
	}
	a.toolLedgerMu.Unlock()
}

// takeToolLedger removes and returns the session's ledger (mining consumes
// it exactly once; sessions that never mine leak nothing past the map entry,
// which take also clears).
func (a *Agent) takeToolLedger(sessionID string) []minedEvent {
	a.toolLedgerMu.Lock()
	defer a.toolLedgerMu.Unlock()
	evs := a.toolLedgers[sessionID]
	delete(a.toolLedgers, sessionID)
	return evs
}

func eventClass(tc types.ToolCall) string {
	if sqlRaw, ok := tc.Input["sql"].(string); ok {
		return tc.Name + ":" + sqlClass(sqlRaw)
	}
	return tc.Name
}

func eventInputPreview(tc types.ToolCall) string {
	b, _ := json.Marshal(tc.Input)
	return truncate(string(b), lessonInputPreview)
}

// lessonPair is one error→verified-fix transition from the tool ledger.
type lessonPair struct {
	Tool       string
	FailingIn  string
	ErrorText  string
	SucceedsIn string
	// Intervening holds the successful calls made between the failure and
	// the success (most recent last, capped). When the failing and
	// succeeding inputs are near-identical, the fix lives HERE — e.g. an
	// INSERT that overflows, a DROP+CREATE with a widened column type, then
	// the same INSERT succeeding. Without these, cross-statement fixes are
	// invisible (measured: zero BIGINT lessons minted while the fix was
	// applied dozens of times, always via a re-CREATE).
	Intervening []string
	// ChangedFragments are the tokens the recovery actually changed,
	// computed mechanically (failing vs succeeding input, plus each
	// intervening call vs the most recent earlier call of its class).
	// The lesson must be grounded in these: the miner's narrative about
	// WHY a fix worked is untrustworthy — measured: recoveries that both
	// widened a DECIMAL and fixed an INTEGER column produced lessons
	// crediting only the (irrelevant) DECIMAL change.
	ChangedFragments []string
}

// maxChangedFragments caps the fragment list carried per pair.
const maxChangedFragments = 12

// tokenDiff returns the tokens that differ between two statements as a
// case-insensitive multiset diff: tokens added by `after` and tokens removed
// from `before`. Surrounding punctuation is trimmed so "INTEGER," matches
// "INTEGER"; case is preserved from the input for display.
func tokenDiff(before, after string) (added, removed []string) {
	count := func(s string) (map[string]int, map[string]string) {
		counts := map[string]int{}
		display := map[string]string{}
		for _, raw := range strings.Fields(s) {
			tok := strings.Trim(raw, ",;()[]{}'\"`")
			if len(tok) < 2 {
				continue
			}
			key := strings.ToLower(tok)
			counts[key]++
			if _, ok := display[key]; !ok {
				display[key] = tok
			}
		}
		return counts, display
	}
	bCounts, bDisplay := count(before)
	aCounts, aDisplay := count(after)
	for key, n := range aCounts {
		if n > bCounts[key] {
			added = append(added, aDisplay[key])
		}
	}
	for key, n := range bCounts {
		if n > aCounts[key] {
			removed = append(removed, bDisplay[key])
		}
	}
	return added, removed
}

// changedFragments computes the grounded fragment list for a pair: the diff
// of the failing vs succeeding input, plus the diff of each intervening
// event against the most recent earlier event of the same class (where a
// cross-statement fix like a re-CREATE actually lives).
func changedFragments(events []minedEvent, failIdx, succeedIdx int, interveningIdx []int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(toks []string) {
		for _, t := range toks {
			key := strings.ToLower(t)
			if seen[key] || len(out) >= maxChangedFragments {
				continue
			}
			seen[key] = true
			out = append(out, t)
		}
	}
	added, removed := tokenDiff(events[failIdx].input, events[succeedIdx].input)
	add(added)
	add(removed)
	for _, k := range interveningIdx {
		for j := k - 1; j >= 0; j-- {
			if events[j].key == events[k].key {
				a, r := tokenDiff(events[j].input, events[k].input)
				add(a)
				add(r)
				break
			}
		}
	}
	return out
}

// maxInterveningCalls bounds the between-failure-and-success excerpt; the
// fix is usually adjacent to the success, so the last few wins.
const maxInterveningCalls = 3

// mineLessonPairs walks the conversation's tool calls in order and pairs
// each failed call with the next SUCCEEDING call of the same kind (same
// tool; for SQL-bearing calls, same statement class — verb plus target).
// One pair per kind: the first failure against the fix that finally worked.
func mineLessonPairs(msgs []types.Message) []lessonPair {
	calls := map[string]types.ToolCall{} // ToolUseID → call
	var order []string                   // preserve call order for ID-less results
	var events []minedEvent
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if _, seen := calls[tc.ID]; !seen {
					order = append(order, tc.ID)
				}
				calls[tc.ID] = tc
			}
			continue
		}
		if m.Role != "tool" || m.ToolResult == nil {
			continue
		}
		tc, ok := calls[m.ToolUseID]
		if !ok {
			// ID-less transports: consume calls in order.
			if len(order) == 0 {
				continue
			}
			tc = calls[order[0]]
			order = order[1:]
		}
		errText := ""
		if m.ToolResult.Error != nil {
			errText = m.ToolResult.Error.Message
		}
		events = append(events, minedEvent{
			name:    tc.Name,
			key:     eventClass(tc),
			input:   eventInputPreview(tc),
			ok:      m.ToolResult.Success && m.ToolResult.Error == nil,
			errText: truncate(errText, lessonErrorPreview),
		})
	}
	return pairEvents(events)
}

// pairEvents pairs, per class, the earliest failure with the first later
// success, carrying the successful calls in between (the cross-statement fix
// path — e.g. a re-CREATE with a widened column type).
func pairEvents(events []minedEvent) []lessonPair {
	firstFailure := map[string]int{}
	emitted := map[string]bool{}
	var pairs []lessonPair
	for i, ev := range events {
		if !ev.ok {
			if _, have := firstFailure[ev.key]; !have {
				firstFailure[ev.key] = i
			}
			continue
		}
		fi, have := firstFailure[ev.key]
		if !have || emitted[ev.key] {
			continue
		}
		emitted[ev.key] = true
		var between []string
		var betweenIdx []int
		for k := fi + 1; k < i; k++ {
			if events[k].ok {
				between = append(between, events[k].input)
				betweenIdx = append(betweenIdx, k)
			}
		}
		if len(between) > maxInterveningCalls {
			between = between[len(between)-maxInterveningCalls:]
			betweenIdx = betweenIdx[len(betweenIdx)-maxInterveningCalls:]
		}
		pairs = append(pairs, lessonPair{
			Tool:             ev.name,
			FailingIn:        events[fi].input,
			ErrorText:        events[fi].errText,
			SucceedsIn:       ev.input,
			Intervening:      between,
			ChangedFragments: changedFragments(events, fi, i, betweenIdx),
		})
		if len(pairs) >= maxLessonPairs {
			return pairs
		}
	}
	return pairs
}

// sqlClass reduces a SQL statement to verb + first target identifier, so a
// failing INSERT into a table pairs with the succeeding INSERT into the same
// table even when the statement text changed (that change IS the lesson).
func sqlClass(sql string) string {
	fields := strings.Fields(strings.ToUpper(sql))
	if len(fields) == 0 {
		return ""
	}
	verb := fields[0]
	for i, f := range fields {
		switch f {
		case "INTO", "TABLE", "FROM", "UPDATE":
			if i+1 < len(fields) {
				return verb + " " + strings.Trim(fields[i+1], "(,;")
			}
		}
	}
	return verb
}

// buildLessonMiningPrompt asks for one reusable cause→fix memory per
// verified transition. The evidence is the ledger excerpt itself, so the
// model is describing an observed fix, never endorsing a theory.
func buildLessonMiningPrompt(pairs []lessonPair) string {
	var sb strings.Builder
	sb.WriteString("Each numbered item below is a VERIFIED fix from a tool-execution ledger: a tool call ")
	sb.WriteString("that failed, the error it produced, and the later call of the same kind that succeeded. ")
	sb.WriteString("The fix demonstrably worked — your job is only to name it.\n\n")
	for i, p := range pairs {
		fmt.Fprintf(&sb, "%d. tool: %s\n   FAILED INPUT: %s\n   ERROR: %s\n", i+1, p.Tool, p.FailingIn, p.ErrorText)
		for _, b := range p.Intervening {
			fmt.Fprintf(&sb, "   CALL BETWEEN FAILURE AND SUCCESS: %s\n", b)
		}
		fmt.Fprintf(&sb, "   SUCCEEDED INPUT: %s\n", p.SucceedsIn)
		if len(p.ChangedFragments) > 0 {
			fmt.Fprintf(&sb, "   OBSERVED CHANGED FRAGMENTS: %s\n", strings.Join(p.ChangedFragments, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(`For each item, extract ONE memory stating the lesson in reusable, situation-independent form — the cause and the fix, not the story. Compare the failed and succeeded inputs: the difference between them is the fix. When the failed and succeeded inputs are identical or nearly identical, the fix happened in the CALLS BETWEEN FAILURE AND SUCCESS — name THAT change as the lesson. Example form: "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT".

The OBSERVED CHANGED FRAGMENTS line lists what the recovery ACTUALLY changed, computed mechanically from the ledger. The lesson MUST quote these fragments verbatim. When several fragments changed, name ALL of them in the lesson — never pick one and present it as the sole cause; the ledger cannot tell which mattered, and neither can you.

Skip an item if the succeeded input does not actually address the error (e.g. it succeeded by abandoning the approach).

Return ONLY a JSON object:
{"entities": [], "relationships": [], "memories": [{"content": "cause and fix, one sentence", "summary": "short summary", "memory_type": "lesson", "tags": ["lesson"], "salience": 0.9, "entities": [], "event_date": "", "event_date_confidence": ""}]}
`)
	return sb.String()
}

// fleetLessons returns up to maxFleetLessons verified lessons matching the
// query from the lesson partition (fleet-shared when opted in, otherwise the
// agent's own). A dedicated lane, deliberately outside the echo-memory
// ranking and the rerank stage: lessons are verified by construction
// (ledger-mined from observed error→fix transitions), so they earn context
// space on relevance alone. The MemoryType filter keeps the private-mode
// query from pulling ordinary memories out of the agent's own partition.
func (a *Agent) fleetLessons(ctx context.Context, searchQuery string, budget int) []*memory.Memory {
	if a.graphMemoryStore == nil {
		return nil
	}
	lessons, err := a.graphMemoryStore.Recall(ctx, memory.RecallOpts{
		AgentID:     a.lessonPartition(),
		Query:       searchQuery,
		MemoryType:  memory.MemoryTypeLesson,
		MinSalience: lessonMinSalience, // outcome credit sinks bad lessons below this
		Limit:       maxFleetLessons,
		MaxTokens:   budget,
	})
	if err != nil {
		return nil
	}
	return lessons
}

// extractLessonsAtEnd runs the ledger-grounded lesson pass over the finished
// conversation. Fire-and-forget from the chat teardown via graphExtractionWG.
func (a *Agent) extractLessonsAtEnd(ctx context.Context, sessionID string) {
	if !a.enableGraphMemoryExtraction || a.graphMemoryStore == nil ||
		a.graphMemoryConfig == nil || !a.graphMemoryConfig.Enabled {
		return
	}
	events := a.takeToolLedger(sessionID)

	// Outcome credit (Fix B, docs/architecture/lesson-grounding-and-credit.md):
	// score the lessons the error lane injected against what the ledger says
	// happened after each injection. Runs before mining so credit lands even
	// when this conversation mints nothing new.
	a.applyLessonCredit(ctx, events, a.takeErrorLessonState(sessionID))

	var pairs []lessonPair
	if len(events) > 0 {
		pairs = pairEvents(events)
	} else if session, ok := a.memory.GetSession(sessionID); ok && session != nil {
		// Fallback (restored sessions, ledgerless paths): the compiled view.
		// It under-reports long conversations — compilation evicts early
		// turns — which is exactly why the ledger is primary.
		pairs = mineLessonPairs(session.GetMessages())
	}
	if len(pairs) == 0 {
		return
	}

	extractCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lessonExtractionTimeout)
	defer cancel()

	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}
	response, err := llmProvider.Chat(extractCtx, []types.Message{
		{Role: "user", Content: buildLessonMiningPrompt(pairs)},
	}, nil)
	if err != nil {
		zap.L().Debug("lesson mining: LLM call failed", zap.Error(err))
		return
	}
	data, ok := parseExtractionResponse(response.Content)
	if !ok {
		return
	}

	agentID := a.config.Name
	grounding := lessonGroundingTokens(pairs)
	stored, dropped := 0, 0
	for _, m := range data.Memories {
		if m.Content == "" {
			continue
		}
		// Grounding gate (Fix A): when the ledger observed concrete changed
		// fragments, a lesson naming none of them is narrative, not
		// observation — drop it rather than store a plausible misdiagnosis.
		if len(grounding) > 0 && !lessonIsGrounded(m.Content, grounding) {
			dropped++
			continue
		}
		salience := m.Salience
		if salience < lessonSalienceFloor || salience > 1 {
			salience = lessonSalienceFloor
		}
		mem := &memory.Memory{
			AgentID:       a.lessonPartition(),
			Content:       m.Content,
			Summary:       m.Summary,
			MemoryType:    memory.MemoryTypeLesson,
			Source:        "lesson_mined",
			MemoryAgentID: agentID, // provenance: who verified it
			Tags:          append(m.Tags, "lesson"),
			Salience:      salience,
		}
		if _, err := a.graphMemoryStore.Remember(extractCtx, mem); err == nil {
			stored++
		}
	}
	if stored > 0 || dropped > 0 {
		zap.L().Info("lesson mining: stored verified lessons",
			zap.String("agent", agentID),
			zap.Int("pairs", len(pairs)),
			zap.Int("lessons", stored),
			zap.Int("dropped_ungrounded", dropped))
	}
}

// lessonGroundingTokens is the union of every pair's observed changed
// fragments (lowercased) — the vocabulary a grounded lesson must draw from.
func lessonGroundingTokens(pairs []lessonPair) map[string]bool {
	out := map[string]bool{}
	for _, p := range pairs {
		for _, f := range p.ChangedFragments {
			out[strings.ToLower(f)] = true
		}
	}
	return out
}

// lessonIsGrounded reports whether the lesson text names at least one
// observed changed fragment.
func lessonIsGrounded(content string, grounding map[string]bool) bool {
	lc := strings.ToLower(content)
	for frag := range grounding {
		if strings.Contains(lc, frag) {
			return true
		}
	}
	return false
}

// lessonCreditWin / lessonCreditLoss are the salience deltas outcome credit
// applies; lessonMinSalience is the recall lane's eligibility floor. Four
// uncorrected losses sink a lesson from its 0.8 floor to below eligibility.
const (
	lessonCreditWin   = 0.02
	lessonCreditLoss  = -0.15
	lessonMinSalience = 0.3
)

// salienceAdjuster is the optional store capability outcome credit needs.
// Kept as a soft type assertion so GraphMemoryStore implementations that
// predate it (other repos, mocks) build unchanged; credit is skipped there.
type salienceAdjuster interface {
	AdjustSalience(ctx context.Context, memoryID string, delta float64) error
}

// applyLessonCredit scores error-lane injections against the ledger: a
// lesson injected while class C was failing gets a win if C succeeded after
// the injection, a loss if no failing class recovered after it. Class-level
// attribution, not causal proof — the claim is only that repeated injection
// into failing recoveries is evidence against a lesson.
func (a *Agent) applyLessonCredit(ctx context.Context, events []minedEvent, injections map[string]int) {
	if len(injections) == 0 || len(events) == 0 {
		return
	}
	adjuster, ok := a.graphMemoryStore.(salienceAdjuster)
	if !ok {
		return
	}
	wins, losses := 0, 0
	for lessonID, idx := range injections {
		if idx > len(events) {
			idx = len(events)
		}
		failedBefore := map[string]bool{}
		for _, ev := range events[:idx] {
			if !ev.ok {
				failedBefore[ev.key] = true
			}
		}
		if len(failedBefore) == 0 {
			continue
		}
		recovered := false
		for _, ev := range events[idx:] {
			if ev.ok && failedBefore[ev.key] {
				recovered = true
				break
			}
		}
		delta := lessonCreditLoss
		if recovered {
			delta = lessonCreditWin
		}
		if err := adjuster.AdjustSalience(ctx, lessonID, delta); err == nil {
			if recovered {
				wins++
			} else {
				losses++
			}
		}
	}
	if wins > 0 || losses > 0 {
		zap.L().Info("lesson credit applied",
			zap.String("agent", a.config.Name),
			zap.Int("wins", wins),
			zap.Int("losses", losses))
	}
}
