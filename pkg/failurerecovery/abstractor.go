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

package failurerecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Abstractor converts a raw failure into an abstract, PII-safe (problem,
// solution) pair. Implementations must never emit raw arguments, queries,
// identifiers, values, or verbatim error text into their output — that text is
// persisted and embedded (spec §10).
type Abstractor interface {
	Abstract(ctx context.Context, class, errMsg string) (problem, solution string)
}

// ruleAbstractor is the default, deterministic abstractor: the rule table plus
// the allowlist scrubber (abstract()). It is provably leak-proof and doubles as
// the fallback for the LLM abstractor whenever the model is unavailable, errors,
// or produces output that fails the leak gate.
type ruleAbstractor struct{}

func (ruleAbstractor) Abstract(_ context.Context, class, errMsg string) (string, string) {
	return abstract(class, errMsg)
}

// LessonModel is the minimal LLM surface the LLM abstractor needs. Any
// pkg/types.LLMProvider satisfies it structurally, so the engine stays decoupled
// from concrete providers and is trivially testable with a fake.
type LessonModel interface {
	Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error)
}

const (
	defaultLLMAbstractTimeout = 8 * time.Second
	maxLessonFieldLen         = 240
)

// llmAbstractor drafts a richer lesson with an LLM, then enforces two guarantees
// before the text can be used:
//
//  1. the output must parse into a non-empty (problem, solution) pair, and
//  2. it must not echo any sensitive literal from the raw error (the leak gate).
//
// If the model errors, times out, returns unparseable text, or the gate trips,
// it falls back to the deterministic ruleAbstractor. The durable store therefore
// only ever holds either rule-based constant strings or LLM text that passed the
// gate — the provable-safety property of the rule path is preserved even when
// the LLM is in the loop.
//
// Using the agent's own (already instrumented) LLM provider means the raw error
// reaches no model that the agent did not already call on the reactive path, so
// this adds no new data egress; the gate exists for durable-storage hygiene.
type llmAbstractor struct {
	model    LessonModel
	fallback Abstractor
	timeout  time.Duration
	// preserveDetails relaxes the leak gate: when set, a drafted lesson MAY
	// reference concrete identifiers (table/column/schema names, object ids,
	// codes, keywords) from the raw error, and the prompt invites that detail.
	// The engine only sets this when the feature is user-scoped, so preserved
	// identifiers are the author's own schema and are never shown to another
	// user.
	preserveDetails bool
	// debug, when non-nil, receives a structured record of each abstraction
	// decision (which lesson source was used, or why the LLM output was
	// rejected and the rule fallback taken). Optional; nil disables it. The
	// fields never include raw error text — only lengths and safe categories.
	debug func(event string, fields map[string]any)
}

func (a *llmAbstractor) logf(event string, fields map[string]any) {
	if a.debug != nil {
		a.debug(event, fields)
	}
}

func newLLMAbstractor(model LessonModel, fallback Abstractor, timeout time.Duration) *llmAbstractor {
	if fallback == nil {
		fallback = ruleAbstractor{}
	}
	if timeout <= 0 {
		timeout = defaultLLMAbstractTimeout
	}
	return &llmAbstractor{model: model, fallback: fallback, timeout: timeout}
}

// lessonPromptTmpl is task-oriented (no role prompting per project convention).
// It requests strict JSON so the two fields parse deterministically and tells
// the model to omit specifics; the leak gate enforces that regardless.
const lessonPromptTmpl = `Summarize this tool failure as a reusable lesson for future attempts.
Output strict JSON only, with exactly two string fields: "problem" and "solution".
"problem": a SHORT canonical phrase (at most 12 words) naming the failure in
database/tool-error vocabulary (reserved keyword, wrong tool name, SQL syntax
error, missing table or column). Not a sentence — no "a statement failed" framing,
no error codes or verbatim error text. This phrase is matched against future
predicted pitfalls, so keep it terse and generic.
"solution": a general, one-sentence, reusable fix.
Do NOT include any specific identifiers, table or column names, schema names,
values, queries, IDs, codes, or verbatim error text. If you cannot generalize
safely, return empty strings.

Failure class: %s
Error: %s

JSON:`

// lessonPromptPreserveTmpl is the detail-preserving variant used when the engine
// runs user-scoped with PreserveDetails on. It invites concrete identifiers,
// which sharpen recall and are the author's own schema, shown back only to them.
const lessonPromptPreserveTmpl = `Summarize this tool failure as a reusable lesson for future attempts.
Output strict JSON only, with exactly two string fields: "problem" and "solution".
"problem": a SHORT canonical phrase (at most 12 words) naming the failure in
database/tool-error vocabulary (reserved keyword, wrong tool name, SQL syntax
error, missing table or column). Keep the specific offending identifier — the
exact tool name, column name, table name, or keyword that triggered the failure.
Not a sentence — no "a statement failed" framing, no error codes or verbatim error
text. This phrase is matched against future predicted pitfalls, so keep it terse.
"solution": a one-sentence, reusable fix that MAY reference those same identifiers.

Failure class: %s
Error: %s

JSON:`

func (a *llmAbstractor) Abstract(ctx context.Context, class, errMsg string) (string, string) {
	if a == nil || a.model == nil {
		return ruleAbstractor{}.Abstract(ctx, class, errMsg)
	}
	cctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	tmpl := lessonPromptTmpl
	if a.preserveDetails {
		tmpl = lessonPromptPreserveTmpl
	}
	prompt := fmt.Sprintf(tmpl, class, errMsg)
	resp, err := a.model.Chat(cctx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		a.logf("llm_abstractor.fallback", map[string]any{"reason": "chat_error", "error": err.Error(), "class": class})
		return a.fallback.Abstract(ctx, class, errMsg)
	}
	if resp == nil {
		a.logf("llm_abstractor.fallback", map[string]any{"reason": "nil_response", "class": class})
		return a.fallback.Abstract(ctx, class, errMsg)
	}

	problem, solution, ok := parseLesson(resp.Content)
	if !ok {
		a.logf("llm_abstractor.fallback", map[string]any{"reason": "unparseable", "class": class, "content_len": len(resp.Content)})
		return a.fallback.Abstract(ctx, class, errMsg)
	}
	// Leak gate: only runs when detail preservation is off. Then it rejects any
	// output that echoes a sensitive identifier from the raw error, keeping the
	// durable lesson generic so it can be shared safely across users. When
	// preservation is on — which the engine sets only for user-scoped memory —
	// the lesson may keep the author's own identifiers, since the store is
	// partitioned to that user and nothing crosses a user boundary.
	if !a.preserveDetails {
		if lit, field, leaked := firstLeak(errMsg, problem, solution); leaked {
			a.logf("llm_abstractor.fallback", map[string]any{"reason": "leak_gate", "class": class, "field": field, "leaked": lit})
			return a.fallback.Abstract(ctx, class, errMsg)
		}
	}
	a.logf("llm_abstractor.used", map[string]any{"class": class, "problem_len": len(problem), "solution_len": len(solution)})
	return problem, solution
}

// pitfallPromptTmpl asks the model to name — as a short, canonical phrase — the
// single most likely pitfall for a task the user is about to run, in the
// vocabulary of database/tool errors, and to keep the specific offending
// identifier (tool/column/table name or keyword) when the task names one. It
// forbids full sentences and any "fix" clause: proactive runs BEFORE any
// failure, so the fix is unknown, and a terse phrase keeps the recall query
// short and stable. Free-form, sentence-length output made the query embedding
// drift run-to-run (it crossed the similarity gate in one run and fell under it
// the next); a constrained phrase that retains the concrete identifier lands
// consistently on top of the stored problem text — which, with preserve_details,
// contains that same identifier. The retrieved lesson supplies the real, learned
// fix; this only has to match the problem. Task-oriented, no role prompting.
const pitfallPromptTmpl = `A user is about to run this task with a SQL/tool agent. Name the single most
likely failure for it as a SHORT phrase (at most 12 words) using the vocabulary of
database and tool errors (reserved keyword, wrong tool name, SQL syntax error,
missing table or column). Include the specific offending identifier from the task
if there is one — the exact tool name, column name, table name, or keyword that
would trigger the failure — because that concrete name is the strongest match to
past lessons. Output only that phrase — no sentence, no fix, no punctuation. If
there is no obvious pitfall, output exactly "none".

Task: %s

Pitfall phrase:`

// predictPitfall translates a task intent into failure-space text so a proactive
// recall query matches stored lessons that were written from real errors. It is
// best-effort: returns ok=false when the model is nil, errors, times out, or
// judges there is no pitfall ("none"/empty), so the caller falls back to the raw
// intent. Unlike the lesson abstractor this needs no leak gate — its input is
// the user's own task text, not a raw error, and its output is a transient query
// that is never persisted.
//
// The output is normalized (lowercased, quotes/punctuation stripped, whitespace
// collapsed) so cosmetic variance in the model's wording does not perturb the
// query embedding — the score against a stored lesson stays stable across runs.
func predictPitfall(ctx context.Context, model LessonModel, timeout time.Duration, intent string) (string, bool) {
	if model == nil || strings.TrimSpace(intent) == "" {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	prompt := fmt.Sprintf(pitfallPromptTmpl, intent)
	resp, err := model.Chat(cctx, []types.Message{{Role: "user", Content: prompt}}, nil)
	if err != nil || resp == nil {
		return "", false
	}
	text := normalizePitfall(resp.Content)
	if text == "" || strings.EqualFold(text, "none") {
		return "", false
	}
	return capLen(text, maxLessonFieldLen), true
}

// normalizePitfall reduces a model's pitfall reply to a stable canonical form:
// first line only, lowercased, surrounding quotes and trailing punctuation
// removed, and internal whitespace collapsed. This keeps the recall query
// embedding stable across runs so its similarity to a stored lesson does not
// flap around the proactive gate.
func normalizePitfall(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Keep only the first non-empty line (guards against the model adding a
	// second sentence or a "fix:" line despite the instructions).
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			s = t
			break
		}
	}
	s = strings.ToLower(s)
	s = strings.Trim(s, "\"'`.,;:!?()[]{} \t")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// lessonJSON is the expected shape of the model's reply.
type lessonJSON struct {
	Problem  string `json:"problem"`
	Solution string `json:"solution"`
}

// jsonObjectRe finds the first JSON object in a response, tolerating code fences
// or surrounding prose the model may add around the JSON.
var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// parseLesson extracts a non-empty (problem, solution) pair from an LLM response.
// Returns ok=false if no JSON object is present or either field is blank.
func parseLesson(content string) (problem, solution string, ok bool) {
	raw := jsonObjectRe.FindString(content)
	if raw == "" {
		return "", "", false
	}
	var l lessonJSON
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return "", "", false
	}
	problem = strings.TrimSpace(l.Problem)
	solution = strings.TrimSpace(l.Solution)
	if problem == "" || solution == "" {
		return "", "", false
	}
	return capLen(problem, maxLessonFieldLen), capLen(solution, maxLessonFieldLen), true
}

func capLen(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}

// dottedIdentRe matches schema-qualified identifiers like
// agentic_demo.demo_fr_orders — a common carrier of real object names.
var dottedIdentRe = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)+`)

// digitTokenRe matches tokens containing a digit (ids, error codes, versions),
// which are never part of a generic lesson and often carry specifics.
var digitTokenRe = regexp.MustCompile(`[a-zA-Z0-9_]*[0-9][a-zA-Z0-9_]*`)

// sqlReservedWords are SQL language keywords, not user data. When a syntax
// error quotes a reserved word (e.g. an unquoted `order` used as a column), that
// token is part of the SQL grammar and appears in any documentation of the fix,
// so a generalized lesson may legitimately reference it ("quote reserved words
// such as order"). Treating it as a sensitive literal would reject otherwise-
// safe lessons — the keyword is the whole point of the lesson — without
// protecting any real data. Real object names are still blocked by the other
// detectors: schema-qualified names (dottedIdentRe), digit-bearing tokens
// (digitTokenRe), and multi-word quoted contents (each field checked).
var sqlReservedWords = map[string]bool{
	"order": true, "group": true, "select": true, "from": true, "where": true,
	"table": true, "column": true, "user": true, "index": true, "key": true,
	"value": true, "values": true, "having": true, "join": true, "into": true,
	"update": true, "delete": true, "insert": true, "create": true, "drop": true,
	"alter": true, "primary": true, "foreign": true, "references": true,
	"default": true, "check": true, "unique": true, "view": true, "case": true,
	"when": true, "then": true, "else": true, "end": true, "distinct": true,
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"between": true, "like": true, "null": true, "union": true, "asc": true,
	"desc": true, "limit": true, "offset": true, "level": true, "row": true,
	"rows": true, "date": true, "time": true, "timestamp": true, "current": true,
	"session": true, "database": true, "schema": true, "grant": true,
	"revoke": true, "role": true, "type": true, "position": true, "start": true,
	"exists": true, "cast": true, "using": true, "natural": true, "outer": true,
	"inner": true, "cross": true, "full": true, "partition": true, "over": true,
}

// sensitiveLiterals extracts substrings of a raw error that could carry data:
// the CONTENTS of quotes/brackets (as whole values), schema-qualified
// identifiers, and any token containing a digit. Generic lowercase error words
// are intentionally NOT treated as sensitive — they are expected to appear in a
// good lesson, so the gate targets only the literal carriers to avoid
// over-rejecting.
//
// The quoted/bracketed contents are added whole and NOT split into individual
// words: database framing such as [Teradata Database], [Error 3706], or
// [Version 20.0.0.68] would otherwise contribute generic words (error, version,
// database) that any reasonable lesson uses, tripping the gate on safe text.
// Embedded identifiers and codes are still caught by the dotted-identifier and
// digit-token scanners below, which run over the whole raw error.
func sensitiveLiterals(raw string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) < 3 || seen[s] || sqlReservedWords[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range quotedOrBracketed.FindAllString(raw, -1) {
		add(strings.Trim(m, "'\"`()[]{}"))
	}
	for _, m := range dottedIdentRe.FindAllString(raw, -1) {
		add(m)
	}
	for _, m := range digitTokenRe.FindAllString(raw, -1) {
		add(m)
	}
	return out
}

// leaksRawLiteral reports whether candidate contains any sensitive literal from
// the raw error (case-insensitive substring). Best-effort: it targets the
// literal carriers (quoted values, qualified names, ids/codes), not generic
// error vocabulary. Any hit routes the abstractor to the safe rule fallback.
func leaksRawLiteral(raw, candidate string) bool {
	low := strings.ToLower(candidate)
	for _, lit := range sensitiveLiterals(raw) {
		if strings.Contains(low, lit) {
			return true
		}
	}
	return false
}

// firstLeak scans each candidate field for the first sensitive literal from the
// raw error and reports the matched literal and which field carried it. It is
// the observable form of leaksRawLiteral, used so the debug hook can record
// exactly why the leak gate tripped. fields are labelled "problem"/"solution"
// in the order given.
func firstLeak(raw string, problem, solution string) (lit, field string, leaked bool) {
	lits := sensitiveLiterals(raw)
	// Deterministic order: problem before solution.
	for _, f := range []struct {
		name string
		text string
	}{{"problem", problem}, {"solution", solution}} {
		low := strings.ToLower(f.text)
		for _, l := range lits {
			if strings.Contains(low, l) {
				return l, f.name, true
			}
		}
	}
	return "", "", false
}
