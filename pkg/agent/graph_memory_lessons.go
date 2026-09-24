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
// it.
//
// SCOPE OF ONE PASS: this runs at the end of every Chat() call — that is ONE
// USER MESSAGE, not one conversation — because the ledger it consumes is
// drained there (takeToolLedger). An error hit on turn 1 and fixed on turn 2
// is therefore structurally unmineable: the pair spans two ledgers and no
// single pass ever sees both halves. Only within-turn error→fix transitions
// mint lessons.
//
// LANE MEMBERSHIP IS STRUCTURAL: mined lessons live in their own partition
// (lessonPartition) that no other writer targets, and the "lesson" memory
// type is refused on both other ingestion paths — the per-turn extractor
// (graph_memory_extractor.go) and the LLM-callable graph_memory tool
// (graph_memory_tool.go). "Verified lessons from prior work" therefore names
// a set the miner alone can add to, rather than a prompt instruction other
// writers are trusted to respect. See
// docs/architecture/lesson-grounding-and-credit.md.
const (
	// maxLessonPairs bounds the mining prompt; the highest-value transitions
	// are the earliest distinct ones.
	maxLessonPairs = 6
	// lessonInputPreview / lessonErrorPreview / lessonResultPreview bound
	// quoted ledger excerpts.
	lessonInputPreview  = 400
	lessonErrorPreview  = 300
	lessonResultPreview = 200
	// lessonTaskPreview bounds the conversation-task background block.
	lessonTaskPreview = 600
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
	// resultPreview is a short excerpt of a successful call's result,
	// captured at execution time. The mining prompt shows it so the miner
	// can judge whether the "success" actually did work — tool-level
	// success is not task-level progress. Measured: a date-filter "fix"
	// that silenced the parse error by matching ZERO rows was mined as a
	// verified lesson, propagated fleet-wide, and produced entire waves
	// confidently reporting "0 cards, NULL total, no errors".
	resultPreview string
	// vacuous marks a success mechanically judged to have done no work.
	// Judgment is tri-layer and endpoint-agnostic: the tool's own
	// VacuousResultJudge (exact, per-endpoint) wins; the shuttle
	// convention matcher (recognizes common structured shapes, abstains
	// otherwise) is the default; whatever both abstain on falls through
	// to the mining LLM's result-preview judgment. A vacuous success can
	// neither close a lesson pair nor score outcome-credit recovery.
	vacuous bool
}

// judgeVacuous runs the mechanical layers of vacuous-success judgment for a
// successful call: the tool's own judge when it implements one, else the
// shuttle convention matcher. Abstention means not vacuous here — the model
// layer (result preview in the mining prompt) covers the remainder.
func (a *Agent) judgeVacuous(tc types.ToolCall, result *shuttle.Result) bool {
	if a.tools != nil {
		if tool, ok := a.tools.Get(tc.Name); ok {
			if judge, isJudge := tool.(shuttle.VacuousResultJudge); isJudge {
				if vacuous, judged := judge.VacuousSuccess(tc.Input, result); judged {
					return vacuous
				}
			}
		}
	}
	vacuous, judged := shuttle.ConventionVacuousResult(tc.Input, result)
	return judged && vacuous
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

// lessonPartitionSuffix derives the PRIVATE lesson partition from the agent's
// own name. Private mode used to store lessons under a.config.Name — the same
// partition the per-turn extractor and the LLM-callable graph_memory tool
// write to — so "verified lesson" was a claim about a prompt instruction, not
// about who could write the row. A derived partition makes the claim
// structural: a caller that does not go through the miner cannot reach it,
// and a future caller cannot forget a filter that does not exist.
const lessonPartitionSuffix = "__lessons"

// lessonPartition returns the graph-memory agent ID lessons are stored under
// and recalled from: the shared fleet partition when sharing is opted in,
// otherwise a partition derived from the agent's own name (private lessons).
// Neither value is ever a partition another writer targets — the per-turn
// extractor and the graph_memory tool both write under a.config.Name.
func (a *Agent) lessonPartition() string {
	if a.graphMemoryConfig != nil && a.graphMemoryConfig.GetFleetLessonSharing() {
		return fleetLessonAgentID
	}
	return a.config.Name + lessonPartitionSuffix
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
	// Probation is allowed here and nowhere else: this is the lane outcome
	// credit scores, so a demoted lesson offered here is actually re-measured.
	lessons := a.fleetLessons(ctx, query, lessonRecallBudget, true)
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

	var verified, probation []*memory.Memory
	for _, m := range fresh {
		if lessonOnProbation(m) {
			probation = append(probation, m)
			continue
		}
		verified = append(verified, m)
	}

	var sb strings.Builder
	// The heading has to match what is actually below it: a delivery holding
	// only a re-trial candidate is not a delivery of verified lessons.
	if len(verified) > 0 {
		sb.WriteString("[Verified Lessons — matched to the error just hit]\n")
	} else {
		sb.WriteString("[Lesson Re-trial — matched to the error just hit]\n")
	}
	if len(verified) > 0 {
		sb.WriteString("These fixes were observed to succeed on this error in prior work:\n")
		for _, m := range verified {
			sb.WriteString("- ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}
	// A demoted lesson rides the re-trial slot. Presenting it as verified
	// would be a lie — the outcome record has it preceding failures — so it
	// is labelled for what it is and the model is left free to ignore it.
	if len(probation) > 0 {
		sb.WriteString("Under re-trial (this one has preceded failures before — use only if it clearly fits):\n")
		for _, m := range probation {
			sb.WriteString("- ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}
	session.AddMessage(ctx, types.Message{
		Role:    "system",
		Content: sb.String(),
	})
}

// clearErrorLessonState drops the session's error-injection tracking
// (paired with ledger teardown at the end of a Chat() call).
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
	if ev.ok {
		if data, isStr := result.Data.(string); isStr {
			ev.resultPreview = truncate(data, lessonResultPreview)
		}
		ev.vacuous = a.judgeVacuous(tc, result)
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
	// SucceedsResult previews what the succeeding call actually returned,
	// so the miner can reject "fixes" that silenced the error by doing no
	// work (empty rowset, zero rows affected, all-NULL aggregates).
	SucceedsResult string
	// Downstream carries later uses of the same object ("input RETURNED
	// result"), the harm evidence a healthy-looking fix hides: a defensive
	// cast that NULLed a key column shows activity_count 55316 on its own
	// INSERT — and rows of zeros when the table is read three calls later.
	// The proof lives in the same ledger; the miner just never saw it.
	Downstream []string
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
		// A vacuous success (mechanically judged to have done no work)
		// cannot close a pair: an error silenced by matching nothing is
		// not a fix, and mining it teaches the fleet confident emptiness.
		if ev.vacuous {
			continue
		}
		emitted[ev.key] = true
		var between []string
		var betweenIdx []int
		for k := fi + 1; k < i; k++ {
			if events[k].ok && !events[k].vacuous {
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
			SucceedsResult:   ev.resultPreview,
			Downstream:       downstreamUses(events, i, ev.key),
			Intervening:      between,
			ChangedFragments: changedFragments(events, fi, i, betweenIdx),
		})
		if len(pairs) >= maxLessonPairs {
			return pairs
		}
	}
	return pairs
}

// maxDownstreamUses caps the later-use evidence carried per pair.
const maxDownstreamUses = 3

// downstreamUses collects later successful uses of the pair's target object
// (the identifier its class keys on) with their result previews. The last
// few are kept — the reads closest to the conversation's final answer are
// the ones that reveal whether the "fixed" object actually holds good data.
func downstreamUses(events []minedEvent, succeedIdx int, key string) []string {
	_, class, found := strings.Cut(key, ":")
	if !found {
		return nil
	}
	fields := strings.Fields(class)
	if len(fields) < 2 {
		return nil
	}
	target := fields[1]
	var uses []string
	for k := succeedIdx + 1; k < len(events); k++ {
		ev := events[k]
		if !ev.ok || ev.resultPreview == "" {
			continue
		}
		if !strings.Contains(strings.ToUpper(ev.input), target) {
			continue
		}
		uses = append(uses, truncate(ev.input, lessonResultPreview)+" RETURNED "+ev.resultPreview)
	}
	if len(uses) > maxDownstreamUses {
		uses = uses[len(uses)-maxDownstreamUses:]
	}
	return uses
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
func buildLessonMiningPrompt(pairs []lessonPair, task string) string {
	var sb strings.Builder
	sb.WriteString("Each numbered item below is a CLAIMED fix from a tool-execution ledger: a tool call ")
	sb.WriteString("that failed, the error it produced, and the later call of the same kind that stopped ")
	sb.WriteString("erroring. You are AUDITING these claims: an error going away proves nothing about the ")
	sb.WriteString("work being right. Name what each change did — and what it traded away.\n\n")
	if task != "" {
		sb.WriteString("THE CONVERSATION'S TASK (context for judging whether a fix served it — ")
		sb.WriteString("lessons must still generalize beyond this task):\n")
		sb.WriteString(task)
		sb.WriteString("\n\n")
	}
	for i, p := range pairs {
		fmt.Fprintf(&sb, "%d. tool: %s\n   FAILED INPUT: %s\n   ERROR: %s\n", i+1, p.Tool, p.FailingIn, p.ErrorText)
		for _, b := range p.Intervening {
			fmt.Fprintf(&sb, "   CALL BETWEEN FAILURE AND SUCCESS: %s\n", b)
		}
		fmt.Fprintf(&sb, "   SUCCEEDED INPUT: %s\n", p.SucceedsIn)
		if p.SucceedsResult != "" {
			fmt.Fprintf(&sb, "   SUCCEEDED RESULT: %s\n", p.SucceedsResult)
		}
		for _, d := range p.Downstream {
			fmt.Fprintf(&sb, "   LATER USE OF THE SAME OBJECT: %s\n", d)
		}
		if len(p.ChangedFragments) > 0 {
			fmt.Fprintf(&sb, "   OBSERVED CHANGED FRAGMENTS: %s\n", strings.Join(p.ChangedFragments, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(`For each item, extract ONE memory stating the lesson in reusable, situation-independent form — one or two sentences, the cause, the change, and its trade-off; not the story. Compare the failed and succeeded inputs: the difference between them is the change. When the failed and succeeded inputs are identical or nearly identical, the change happened in the CALLS BETWEEN FAILURE AND SUCCESS — name THAT change as the lesson.

State the trade-off and its applicability condition whenever the change has one. A change that silences an error by discarding or nulling data is a trade, not a free fix. Example of a clean fix: "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT". Example of a trade-off lesson: "wrapping CC_Number in a defensive CAST silences numeric overflow but NULLs unconvertible values — safe only when the column is not used as a key or in counts downstream".

Judge each claim against the evidence:
- LATER USE OF THE SAME OBJECT shows what the "fixed" object actually held afterward. Zeros, NULLs, or empty rows where the task expected data mean the change corrupted the data — record the lesson as a WARNING against that change, stating what it broke, not as a fix.
- SUCCEEDED RESULT: a success that did no work is NOT a fix. An empty result set, zero rows inserted/affected (e.g. "activity_count":0 on an INSERT), or all-NULL values means the change silenced the error by matching nothing. Skip such items entirely.
- Skip an item if the succeeded input does not actually address the error (e.g. it succeeded by abandoning the approach).

The OBSERVED CHANGED FRAGMENTS line lists what the recovery ACTUALLY changed, computed mechanically from the ledger. The lesson MUST quote these fragments verbatim. When several fragments changed, name ALL of them in the lesson — never pick one and present it as the sole cause; the ledger cannot tell which mattered, and neither can you.

Return ONLY a JSON object:
{"entities": [], "relationships": [], "memories": [{"content": "cause, change, and trade-off", "summary": "short summary", "memory_type": "lesson", "tags": ["lesson"], "salience": 0.9, "entities": [], "event_date": "", "event_date_confidence": ""}]}
`)
	return sb.String()
}

// fleetLessons returns up to maxFleetLessons verified lessons matching the
// query from the lesson partition (fleet-shared when opted in, otherwise the
// agent's own derived one). A dedicated lane, deliberately outside the
// echo-memory ranking and the rerank stage: lessons are verified by
// construction (ledger-mined from observed error→fix transitions), so they
// earn context space on relevance alone.
//
// Two structural filters, both enforced in the store query rather than in Go
// after the fact, make the lane's membership a fact and not a convention:
// AgentID is a partition only the miner writes to, and MemoryType is a class
// both other ingestion paths refuse. Nothing this returns can have been
// written by the per-turn extractor or by the model through the graph_memory
// tool.
//
// allowProbation offers ONE demoted lesson (below the recall floor) every
// lessonRetrialInterval-th call, so demotion is not an absorbing state. It is
// true only on the error-triggered lane, where outcome credit scores what
// happens next — a re-trial that nothing measures is pure cost.
func (a *Agent) fleetLessons(ctx context.Context, searchQuery string, budget int, allowProbation bool) []*memory.Memory {
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
	if !allowProbation || len(lessons) >= maxFleetLessons {
		return lessons
	}
	if a.lessonTrials.Add(1)%lessonRetrialInterval != 0 {
		return lessons
	}
	// Re-trial slot: the highest-ranked demoted lesson, one at a time. The
	// ceiling is a store-side condition (BelowSalience), so a demoted row is
	// selected as such instead of being fished out of a wider result set.
	demoted, err := a.graphMemoryStore.Recall(ctx, memory.RecallOpts{
		AgentID:       a.lessonPartition(),
		Query:         searchQuery,
		MemoryType:    memory.MemoryTypeLesson,
		MinSalience:   lessonProbationFloor,
		BelowSalience: lessonMinSalience,
		Limit:         1,
		MaxTokens:     budget,
	})
	if err != nil || len(demoted) == 0 {
		return lessons
	}
	// A store that does not honor BelowSalience (the field is optional; only
	// the SQLite store implements it) would answer with an eligible lesson
	// instead. Take the candidate only if it really is demoted and new.
	pick := demoted[0]
	if !lessonOnProbation(pick) {
		return lessons
	}
	for _, m := range lessons {
		if m.ID == pick.ID {
			return lessons
		}
	}
	return append(lessons, pick)
}

// lessonOnProbation reports whether a recalled lesson is a demoted one riding
// the re-trial slot — injected under a different, honest heading.
func lessonOnProbation(m *memory.Memory) bool {
	return m != nil && m.Salience < lessonMinSalience
}

// extractLessonsAtEnd runs the ledger-grounded lesson pass over the ledger
// accumulated during ONE Chat() call — one user message, not one conversation
// (see the file header). Fire-and-forget from the chat teardown via
// graphExtractionWG.
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

	// The conversation's opening request, as BACKGROUND for the miner:
	// whether a change served or corrupted the work is judgeable only
	// against what the work was for. Lessons still must generalize.
	var task string
	session, haveSession := a.memory.GetSession(sessionID)
	if haveSession && session != nil {
		for _, m := range session.GetMessages() {
			if m.Role == "user" && m.Content != "" {
				task = truncate(m.Content, lessonTaskPreview)
				break
			}
		}
	}

	var pairs []lessonPair
	if len(events) > 0 {
		pairs = pairEvents(events)
	} else if haveSession && session != nil {
		// Fallback (restored sessions, ledgerless paths): the compiled view.
		// It under-reports long conversations — compilation evicts early
		// turns — which is exactly why the ledger is primary.
		pairs = mineLessonPairs(session.GetMessages())
	}
	// Drop pairs the ledger observed no change in, BEFORE mining (Fix A).
	// The commonest error→success shape in agent tool use is a transient
	// retry — a rate limit or a timeout, then the IDENTICAL input succeeding
	// — which changed nothing. Asking the miner to explain a change that does
	// not exist is asking it to invent one, and an all-empty grounding map
	// used to switch OFF the very gate that would have caught the invention.
	// Dropping the pairs instead means the gate below is unconditional.
	pairs = groundedPairs(pairs)
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
		{Role: "user", Content: buildLessonMiningPrompt(pairs, task)},
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
		// Grounding gate (Fix A): every surviving pair carries observed
		// changed fragments (groundedPairs above), so a lesson naming none of
		// them is narrative, not observation — drop it rather than store a
		// plausible misdiagnosis. Unconditional: the gate must not weaken in
		// the case where the evidence is weakest.
		if !lessonIsGrounded(m.Content, grounding) {
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

// groundedPairs keeps only the pairs the ledger observed a concrete change
// in. A pair with no ChangedFragments has no cause to teach: either nothing
// changed between the failure and the success (the transient-retry shape) or
// the change happened in a call with no earlier same-class call to diff it
// against, and neither is evidence a lesson can be grounded in. Recall is
// deliberately traded for the guarantee that every stored lesson names
// something that actually happened.
func groundedPairs(pairs []lessonPair) []lessonPair {
	var out []lessonPair
	for _, p := range pairs {
		if len(p.ChangedFragments) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
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
//
// Demotion is deliberately NOT absorbing. A lesson below the floor would
// otherwise be unreachable forever — never recalled, so never injected, so
// never able to earn the win that is its only way back up (the store's other
// salience paths are decay; TouchMemories only counts accesses). Instead:
//   - lessonRetrialInterval: one demoted lesson rides the error lane's recall
//     every Nth call, so it gets re-measured against real work.
//   - a win by a demoted lesson reinstates it exactly TO the floor rather
//     than nudging it by lessonCreditWin (13 wins from the 0.05 clamp is a
//     theoretical path, not a real one). One observed recovery readmits it;
//     one observed failure demotes it again.
//
// The alternative — flooring salience at the recall threshold — was rejected:
// it keeps genuinely bad lessons permanently eligible, which is the failure
// outcome credit exists to fix.
const (
	lessonCreditWin   = 0.02
	lessonCreditLoss  = -0.15
	lessonMinSalience = 0.3
	// lessonProbationFloor is the lower bound of the re-trial band. Below the
	// store's AdjustSalience clamp (0.05) so every demoted lesson is
	// reachable, above 0 so the store's default-min-salience substitution
	// (which triggers on <= 0) does not silently widen it.
	lessonProbationFloor = 0.01
	// lessonRetrialInterval: one in N error-lane recalls carries a demoted
	// lesson. Frequent enough that a wrongly demoted lesson gets re-measured
	// within a working session, rare enough that a genuinely bad one costs
	// little context.
	lessonRetrialInterval = 8
)

// salienceAdjuster is the optional store capability outcome credit needs.
// Kept as a soft type assertion so GraphMemoryStore implementations that
// predate it (other repos, mocks) build unchanged; credit is skipped there.
type salienceAdjuster interface {
	AdjustSalience(ctx context.Context, memoryID string, delta float64) error
}

// applyLessonCredit scores error-lane injections against the ledger: a
// lesson injected while class C was failing gets a win if C succeeded after
// the injection, and a loss only if the ledger went on to show a FAILURE
// after it. Class-level attribution, not causal proof — the claim is only
// that repeated injection into failing recoveries is evidence against a
// lesson.
//
// Silence scores nothing. The ledger drains at the end of every Chat() call,
// so the normal ending of an interactive turn is an empty post-injection tail
// — charging that as a loss would demote every lesson delivered near the end
// of a turn, which is most of them (the error lane fires on the failure that
// usually ends the turn). Absence of evidence is not evidence of failure. A
// win needs an observed recovery; a loss needs observed contradiction — a
// failure after the injection, or a mechanically-judged no-work "success" of
// the failing class. Everything else leaves the lesson untouched.
func (a *Agent) applyLessonCredit(ctx context.Context, events []minedEvent, injections map[string]int) {
	if len(injections) == 0 || len(events) == 0 {
		return
	}
	adjuster, ok := a.graphMemoryStore.(salienceAdjuster)
	if !ok {
		return
	}
	wins, losses, unscored := 0, 0, 0
	for lessonID, idx := range injections {
		if idx < 0 {
			idx = 0
		}
		if idx >= len(events) {
			// Nothing was recorded after the injection at all.
			unscored++
			continue
		}
		failedBefore := map[string]bool{}
		for _, ev := range events[:idx] {
			if !ev.ok {
				failedBefore[ev.key] = true
			}
		}
		if len(failedBefore) == 0 {
			unscored++
			continue
		}
		recovered, contradicted := false, false
		for _, ev := range events[idx:] {
			// A vacuous success is not a recovery — silencing the error by
			// doing no work must not score a win for the injected lesson.
			if ev.ok && !ev.vacuous && failedBefore[ev.key] {
				recovered = true
				break
			}
			switch {
			case !ev.ok:
				// An observed failure after the injection.
				contradicted = true
			case ev.vacuous && failedBefore[ev.key]:
				// A no-work "fix" of the failing class, positively judged
				// (not abstained on) by the mechanical layers: the error went
				// quiet and the work did not happen. Evidence, not silence.
				contradicted = true
			}
		}
		if !recovered && !contradicted {
			// The tail holds only unrelated successful work: the lesson was
			// neither vindicated nor contradicted.
			unscored++
			continue
		}
		delta := lessonCreditLoss
		if recovered {
			delta = a.lessonWinDelta(ctx, lessonID)
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
			zap.Int("losses", losses),
			zap.Int("unscored", unscored))
	}
}

// lessonWinDelta is the salience delta for an observed win: the ordinary
// upward nudge for an eligible lesson, or full reinstatement to the recall
// floor for a demoted one that won its re-trial. Reinstatement is what keeps
// demotion reversible — see the constant block above. A store that cannot
// report current salience falls back to the nudge.
func (a *Agent) lessonWinDelta(ctx context.Context, lessonID string) float64 {
	if a.graphMemoryStore == nil {
		return lessonCreditWin
	}
	cur, err := a.graphMemoryStore.GetMemory(ctx, a.lessonPartition(), lessonID)
	if err != nil || cur == nil || cur.Salience >= lessonMinSalience {
		return lessonCreditWin
	}
	return lessonMinSalience - cur.Salience
}
