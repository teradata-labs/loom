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
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// This file implements the DETERMINISTIC, LLM-free side of failure recovery:
// a signature extracted from the concrete failing call (tool + arguments +
// error) at learn time, and a matcher that compares a freshly PLANNED call
// against those signatures at dispatch time — before the tool runs.
//
// The design pivot: instead of predicting a failure from the user's intent
// (noisy, task-space, LLM-dependent), we intercept the agent's planned tool
// call (ground truth, same space as the stored failure) and check it against
// what has concretely broken before. Two tiers do this with pure string logic:
//
//	Tier 1 (exact replay): the normalized tool+args of the planned call equals
//	        a call that previously failed → certain repeat.
//	Tier 2 (token overlap): an "offending token" captured from the ERROR at
//	        failure time (e.g. the reserved word `order`, the bogus tool name
//	        `execute_sql_query`) appears in the planned call's arguments.
//
// Both are deterministic, need no model and no embeddings, and so keep working
// when the LLM is weak or unavailable — the exact robustness gap that intent-
// based proactive prediction could not close. Correction stays LLM-driven: a
// match only injects an advisory ("you previously did X, it failed, the fix was
// Y — revise your call"); the model re-plans. Detection is deterministic,
// correction is general — nothing is hardcoded to any specific demo case.

// callSignature is the deterministic fingerprint of a concrete tool call,
// captured at failure time and compared at dispatch time. It is PII-bearing
// (it contains real identifiers from the call), so it is ONLY ever stored on a
// lesson when the feature is user-scoped — the same safety rule the LLM
// abstractor's detail preservation already follows: the identifiers are the
// author's own and are shown back only to that author.
type callSignature struct {
	// Tool is the tool name of the failing call (e.g. "teradata_tool_call").
	Tool string `json:"tool,omitempty"`
	// CanonicalCall is a stable, order-independent serialization of
	// tool+arguments used for tier-1 exact-replay matching.
	CanonicalCall string `json:"canonical_call,omitempty"`
	// OffendingTokens are concrete identifiers extracted from the ERROR text
	// (not hand-picked): the reserved word the engine rejected, the unknown
	// tool/column/table name it named. A later planned call that contains any
	// of these in its arguments is about to repeat the mistake (tier 2).
	OffendingTokens []string `json:"offending_tokens,omitempty"`
	// ResourceTokens are the schema-qualified objects the FAILING call touched
	// (e.g. agentic_demo.demo_fr_orders and its tail demo_fr_orders). They are
	// the retrieval key for the advisory tier: when a later TASK references the
	// same resource, the lesson's fix is surfaced at planning time so the agent
	// avoids a semantic re-derivation of the same defect (e.g. a different query
	// that again leaves a reserved word unquoted on this exact table). Only
	// schema-qualified identifiers qualify, so a bare word can never pull a
	// lesson in — this keeps the advisory scoped to the object in hand, not all
	// of memory.
	ResourceTokens []string `json:"resource_tokens,omitempty"`
}

// empty reports whether the signature carries no usable deterministic signal.
func (s callSignature) empty() bool {
	return s.CanonicalCall == "" && len(s.OffendingTokens) == 0 && len(s.ResourceTokens) == 0
}

// marshal serializes the signature for storage in a lesson's properties. Returns
// "" for an empty signature so callers can omit the field.
func (s callSignature) marshal() string {
	if s.empty() {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseSignature best-effort reads a callSignature from a lesson's stored JSON.
// Returns the zero value on any error (deterministic tiers simply don't fire).
func parseSignature(s string) callSignature {
	if strings.TrimSpace(s) == "" {
		return callSignature{}
	}
	var sig callSignature
	if err := json.Unmarshal([]byte(s), &sig); err != nil {
		return callSignature{}
	}
	return sig
}

// canonicalCall builds a stable, order-independent string from a tool name and
// its argument map. Keys are sorted and values rendered with encoding/json, so
// two semantically identical calls (regardless of map iteration order) produce
// the same string — the key for tier-1 exact-replay matching.
func canonicalCall(tool string, args map[string]any) string {
	tool = strings.TrimSpace(strings.ToLower(tool))
	if len(args) == 0 {
		return tool + "()"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(tool)
	b.WriteByte('(')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strings.ToLower(k))
		b.WriteByte('=')
		// Render the value deterministically. encoding/json gives stable output
		// for scalars and recursively-sorted output is unnecessary here because
		// argument values are compared as whole literals, not structurally.
		vb, err := json.Marshal(args[k])
		if err != nil {
			b.WriteString("?")
			continue
		}
		b.Write([]byte(strings.ToLower(string(vb))))
	}
	b.WriteByte(')')
	return b.String()
}

// identTokenRe matches identifier-like tokens: a letter/underscore start, then
// letters, digits or underscores. Used to scan call arguments and error text.
var identTokenRe = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)

// offQuote matches an optional surrounding quote (single, double or back-tick)
// around an identifier inside an error message.
const offQuote = `['"` + "`" + `]?`

// invalidityPatterns capture the single identifier that an engine reports as
// INVALID — unknown, not found, nonexistent or undefined. Each pattern ties the
// captured identifier to an invalidity phrase, so we harvest exactly the object
// the error rejected (the bogus tool name, the missing table) and nothing else.
//
// Only invalidity errors yield tier-2 tokens, and this is the crux of
// soundness: an identifier the engine calls UNKNOWN / NONEXISTENT should never
// legitimately appear in a valid call, so its reuse is a safe repeat signal.
// A RESERVED-WORD or SYNTAX error is deliberately NOT matched here — it names an
// identifier that is VALID but merely misused (double-quoting fixes it). That
// identifier legitimately reappears, including in the correct fix and in
// unrelated clauses (e.g. `order` in `ORDER BY`), so blocking on it would
// false-positive and even block the correction. Those cases are handled by
// tier-1 exact replay plus the LLM advisory, never by tier-2.
var invalidityPatterns = []*regexp.Regexp{
	// "Unknown tool 'execute_sql_query'", "unknown column `foo`"
	regexp.MustCompile(`(?i)\bunknown\s+(?:tool|table|column|function|command|object|relation|database|schema)\s+` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)`),
	// "no such table 'orders'"
	regexp.MustCompile(`(?i)\bno\s+such\s+(?:tool|table|column|function|object|database)\s+` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)`),
	// "relation \"agentic_demo.demo_fr_orders\" does not exist"
	regexp.MustCompile(`(?i)\b(?:relation|table|column|function|tool|object|database|schema)\s+` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)` + offQuote + `\s+(?:does\s+not\s+exist|doesn't\s+exist|not\s+found|is\s+undefined|is\s+not\s+recognized)`),
	// "'execute_sql_query' is not a recognized tool", "`foo` does not exist"
	regexp.MustCompile(`(?i)` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)` + offQuote + `\s+(?:is\s+not\s+a\s+(?:recognized|valid|known)|does\s+not\s+exist|is\s+undefined|not\s+found)`),
	// bare schema-qualified name before an invalidity phrase (unquoted)
	regexp.MustCompile(`(?i)\b([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)+)\s+(?:does\s+not\s+exist|not\s+found|is\s+undefined)`),
	// "undefined column 'foo'", "undefined name bar"
	regexp.MustCompile(`(?i)\bundefined\s+(?:column|table|function|name|object)\s+` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)`),
	// "invalid object name 'dbo.missing'"
	regexp.MustCompile(`(?i)\binvalid\s+(?:object|tool|table|column|function)\s+(?:name\s+)?` + offQuote + `([a-zA-Z_][a-zA-Z0-9_.]*)`),
}

// commonErrorNoise are generic words that may be captured by an invalidity
// pattern but are NOT specific offending identifiers. Excluding them (plus the
// structural SQL/call keywords) keeps tier-2 precise so it never false-fires on
// database framing or on words that appear in every query.
var commonErrorNoise = map[string]bool{
	// error framing
	"error": true, "database": true, "version": true, "session": true,
	"warning": true, "failed": true, "failure": true, "exception": true,
	"null": true, "none": true, "true": true, "false": true, "value": true,
	"column": true, "table": true, "row": true, "rows": true, "type": true,
	"syntax": true, "tool": true, "query": true, "statement": true,
	"result": true, "status": true, "name": true, "sql": true,
	"parameters": true, "input": true, "output": true,
	// structural SQL keywords (legitimately appear in valid calls)
	"select": true, "from": true, "where": true, "order": true, "group": true,
	"having": true, "join": true, "inner": true, "outer": true, "left": true,
	"right": true, "and": true, "not": true, "into": true, "values": true,
	"insert": true, "update": true, "delete": true, "set": true, "distinct": true,
	"limit": true, "offset": true, "union": true, "between": true, "like": true,
	"exists": true, "count": true, "keyword": true, "something": true,
	"expected": true,
}

// extractOffendingTokens pulls the concrete INVALID identifiers from a raw error
// (lowercased, de-duplicated) — only the identifiers the engine declared unknown
// or nonexistent. These are the tokens whose reappearance in a future planned
// call is a sound signal of repeating that exact mistake.
//
// It deliberately returns nothing for reserved-word / syntax errors: see the
// doc on invalidityPatterns. For a schema-qualified capture it also adds the
// tail segment so a later call referencing just the object (demo_fr_orders) is
// matched as well as the full path (agentic_demo.demo_fr_orders).
func extractOffendingTokens(errMsg string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		tok = strings.Trim(tok, "'\"`")
		if tok == "" || len(tok) < 3 || commonErrorNoise[tok] {
			return
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
		if strings.Contains(tok, ".") {
			parts := strings.Split(tok, ".")
			tail := parts[len(parts)-1]
			if len(tail) >= 3 && !commonErrorNoise[tail] && !seen[tail] {
				seen[tail] = true
				out = append(out, tail)
			}
		}
	}
	for _, re := range invalidityPatterns {
		for _, m := range re.FindAllStringSubmatch(errMsg, -1) {
			if len(m) > 1 && m[1] != "" {
				add(m[1])
			}
		}
	}
	return out
}

// argTokens returns the set of lowercased identifier-like tokens that appear in
// a planned call's arguments (values and keys), for tier-2 overlap checks. The
// tool name is included so a planned call that passes a bad inner tool name as
// an argument (gateway pattern) is caught.
func argTokens(tool string, args map[string]any) map[string]bool {
	toks := map[string]bool{}
	addFrom := func(s string) {
		for _, t := range identTokenRe.FindAllString(strings.ToLower(s), -1) {
			toks[t] = true
		}
	}
	addFrom(tool)
	var walk func(v any)
	walk = func(v any) {
		switch tv := v.(type) {
		case string:
			addFrom(tv)
		case map[string]any:
			for k, vv := range tv {
				addFrom(k)
				walk(vv)
			}
		case []any:
			for _, vv := range tv {
				walk(vv)
			}
		}
	}
	for k, v := range args {
		addFrom(k)
		walk(v)
	}
	return toks
}

// resourceTokensFromArgs returns the schema-qualified objects a call's arguments
// reference — the dotted identifiers (agentic_demo.demo_fr_orders) plus their
// tail segment (demo_fr_orders), lowercased and de-duplicated. Only dotted names
// qualify: a bare word is never treated as a resource, so the advisory tier can
// never be pulled in by a generic token.
func resourceTokensFromArgs(args map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" || commonErrorNoise[tok] || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	var walk func(v any)
	walk = func(v any) {
		switch tv := v.(type) {
		case string:
			for _, d := range dottedIdentRe.FindAllString(tv, -1) {
				d = strings.ToLower(d)
				add(d)
				parts := strings.Split(d, ".")
				add(parts[len(parts)-1])
			}
		case map[string]any:
			for _, vv := range tv {
				walk(vv)
			}
		case []any:
			for _, vv := range tv {
				walk(vv)
			}
		}
	}
	for _, v := range args {
		walk(v)
	}
	return out
}

// resourceTokensFromText returns the schema-qualified objects a free-text task
// references, using the same dotted-only rule as resourceTokensFromArgs so the
// retrieval key is symmetric: a lesson is surfaced only when the task names the
// same qualified object (or its specific table name) that the failing call
// touched.
func resourceTokensFromText(s string) map[string]bool {
	set := map[string]bool{}
	for _, d := range dottedIdentRe.FindAllString(s, -1) {
		d = strings.ToLower(d)
		if !commonErrorNoise[d] {
			set[d] = true
		}
		parts := strings.Split(d, ".")
		tail := parts[len(parts)-1]
		if tail != "" && !commonErrorNoise[tail] {
			set[tail] = true
		}
	}
	return set
}

// matchResource reports whether a stored signature touched any resource that the
// current task also references — the gate for the advisory tier. It requires a
// concrete schema-qualified overlap, so it fires only for the specific object in
// hand and never for unrelated lessons.
func matchResource(sig callSignature, taskResources map[string]bool) bool {
	if len(taskResources) == 0 || len(sig.ResourceTokens) == 0 {
		return false
	}
	for _, r := range sig.ResourceTokens {
		if taskResources[strings.ToLower(r)] {
			return true
		}
	}
	return false
}

// callSkeleton reduces a tool call to its STRUCTURAL token set — the tool name,
// the argument keys, and the identifier/keyword tokens of the argument values —
// with the concrete object identifiers (schema-qualified names and their parts)
// REMOVED. It is the key to cross-object prevention: two calls that differ only
// in which table/column they touch collapse to the SAME skeleton, so a mistake
// learned on one object is recognized when the same call shape is planned on a
// DIFFERENT object. Conversely, an unrelated call shares little skeleton and
// scores low, so the match stays specific.
//
// Object identifiers are dropped (not kept) precisely because they are the part
// that legitimately varies across tables; keeping them would make the match
// table-bound (which is what the separate resource-gated advisory already does).
// Structural SQL keywords (select/from/order/…) are deliberately KEPT — they are
// the shape that predicts the defect (e.g. an unquoted reserved word after
// SELECT), not noise, here.
func callSkeleton(tool string, args map[string]any, resourceTokens []string) map[string]bool {
	drop := map[string]bool{}
	for _, r := range resourceTokens {
		for _, part := range strings.Split(strings.ToLower(r), ".") {
			if part != "" {
				drop[part] = true
			}
		}
	}
	skel := map[string]bool{}
	for _, tok := range identTokenRe.FindAllString(strings.ToLower(canonicalCall(tool, args)), -1) {
		if !drop[tok] {
			skel[tok] = true
		}
	}
	return skel
}

// skeletonFromCanonical derives the same structural token set from a stored
// signature's canonical call string, dropping that signature's own recorded
// resource tokens. It mirrors callSkeleton so the planned and stored sides are
// compared symmetrically.
func skeletonFromCanonical(canonical string, resourceTokens []string) map[string]bool {
	drop := map[string]bool{}
	for _, r := range resourceTokens {
		for _, part := range strings.Split(strings.ToLower(r), ".") {
			if part != "" {
				drop[part] = true
			}
		}
	}
	skel := map[string]bool{}
	for _, tok := range identTokenRe.FindAllString(strings.ToLower(canonical), -1) {
		if !drop[tok] {
			skel[tok] = true
		}
	}
	return skel
}

// skeletonSimilarity is the Jaccard overlap of two call skeletons together with
// the size of their intersection. Jaccard (|A∩B| / |A∪B|) is scale-free, so a
// same-shape call on a different table scores 1.0 while an unrelated call scores
// near 0; the caller also gates on a minimum intersection so a couple of shared
// wrapper tokens alone cannot trip the advisory.
func skeletonSimilarity(a, b map[string]bool) (jaccard float64, overlap int) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	inter := 0
	for tok := range a {
		if b[tok] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0, 0
	}
	return float64(inter) / float64(union), inter
}

// matchTier reports the strongest deterministic tier at which a planned call
// matches a stored failure signature, or 0 for no match.
//
//	1 = exact replay  (normalized tool+args identical to the failed call)
//	2 = token overlap (an offending token from the error is in the planned args)
//
// Tool scoping: tier-1 already keys on the tool via canonicalCall. Tier-2 does
// NOT require a tool match, because the gateway pattern (teradata_tool_call
// wrapping execute_sql_query) means the offending token travels in the args of
// a differently-named outer tool — requiring an identical tool name would miss
// exactly the wrong-tool case this is meant to catch.
func matchTier(sig callSignature, plannedCanonical string, plannedTokens map[string]bool) int {
	if sig.empty() {
		return 0
	}
	if sig.CanonicalCall != "" && plannedCanonical != "" &&
		strings.EqualFold(sig.CanonicalCall, plannedCanonical) {
		return 1
	}
	for _, tok := range sig.OffendingTokens {
		if plannedTokens[strings.ToLower(tok)] {
			return 2
		}
	}
	return 0
}
