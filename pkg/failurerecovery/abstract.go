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
	"regexp"
	"strings"
)

// This file is the PII boundary (spec §10). The repository performs NO redaction
// on the memory write path, so masking is entirely this feature's responsibility.
//
// The strategy is allowlist, not denylist: a raw error is turned into an abstract
// problem/solution that contains STRUCTURE ONLY — known error categories and a
// curated structural vocabulary — never literal argument values, query text,
// ids, secrets, or connection strings. Everything not explicitly allowed is
// dropped. This guarantees no raw payload can reach the store or the embedder.

// lessonRule maps a recognized error shape to a fixed, literal-free problem and
// its reusable solution. Neither problem nor solution ever interpolates captured
// text — they are constant strings, so a rule match cannot leak data.
type lessonRule struct {
	re       *regexp.Regexp
	problem  string
	solution string
}

// lessonRules recognizes common, cross-provider failure shapes. Order matters:
// the first match wins, so more specific patterns precede generic ones.
var lessonRules = []lessonRule{
	{
		re:       regexp.MustCompile(`reserved (word|keyword)|syntax error at or near "(from|select|order|group|table|user|where)"`),
		problem:  "reserved SQL keyword used as an unquoted identifier",
		solution: "wrap the identifier in double quotes",
	},
	{
		re:       regexp.MustCompile(`(relation|table|view) .* does not exist|no such table|table .* not found|unknown table`),
		problem:  "referenced a table or relation that does not exist",
		solution: "verify the object name and schema before querying; list available tables first",
	},
	{
		re:       regexp.MustCompile(`(column|field) .* does not exist|no such column|unknown column`),
		problem:  "referenced a column that does not exist",
		solution: "inspect the table schema and use an existing column name",
	},
	{
		re:       regexp.MustCompile(`syntax error|parse error|malformed|invalid syntax`),
		problem:  "malformed query or expression syntax",
		solution: "correct the statement syntax and retry",
	},
	{
		re:       regexp.MustCompile(`permission denied|access denied|not authorized|unauthorized|forbidden|insufficient privilege`),
		problem:  "operation was denied due to insufficient permissions",
		solution: "use an operation within granted permissions, or request access to the resource",
	},
	{
		re:       regexp.MustCompile(`timeout|timed out|deadline exceeded`),
		problem:  "operation exceeded its time limit",
		solution: "reduce the scope of the request or add a narrower filter, then retry",
	},
	{
		re:       regexp.MustCompile(`connection refused|connection reset|could not connect|network unreachable`),
		problem:  "could not establish a connection to the backend",
		solution: "verify the endpoint is reachable and retry with backoff",
	},
	{
		re:       regexp.MustCompile(`rate limit|too many requests|throttl`),
		problem:  "request was rate limited",
		solution: "back off and retry with fewer, spaced-out requests",
	},
	{
		re:       regexp.MustCompile(`missing required (parameter|argument|field|property)|required .* is missing|is a required (property|field)`),
		problem:  "a required argument was missing from the tool call",
		solution: "include every required argument before calling the tool",
	},
	{
		re:       regexp.MustCompile(`invalid (type|argument type)|type mismatch|expected .* got|cannot unmarshal|invalid json|json parse`),
		problem:  "an argument had the wrong type or malformed structure",
		solution: "match each argument to its declared type and valid structure",
	},
	{
		re:       regexp.MustCompile(`duplicate|unique constraint|already exists`),
		problem:  "violated a uniqueness constraint with a duplicate value",
		solution: "check for an existing record before inserting, or use an upsert",
	},
	{
		re:       regexp.MustCompile(`null|not-null constraint|cannot be null`),
		problem:  "supplied a null value where a value is required",
		solution: "provide a non-null value for the required field",
	},
}

// structuralVocab is the allowlist for the fallback scrubber: only these
// error-vocabulary tokens survive. Identifiers, numbers, quoted strings, and any
// word not in this set are dropped, so no literal can pass through.
var structuralVocab = map[string]bool{
	"error": true, "errors": true, "failed": true, "failure": true,
	"invalid": true, "missing": true, "required": true, "not": true,
	"found": true, "exists": true, "permission": true, "denied": true,
	"access": true, "unauthorized": true, "forbidden": true,
	"timeout": true, "timed": true, "out": true, "deadline": true, "exceeded": true,
	"connection": true, "refused": true, "reset": true, "unreachable": true,
	"syntax": true, "near": true, "unexpected": true, "token": true, "parse": true,
	"argument": true, "arguments": true, "parameter": true, "parameters": true,
	"field": true, "property": true, "value": true,
	"type": true, "mismatch": true, "expected": true,
	"reserved": true, "keyword": true, "identifier": true, "unknown": true,
	"column": true, "table": true, "relation": true, "view": true, "schema": true,
	"constraint": true, "violation": true, "null": true, "duplicate": true, "unique": true,
	"rate": true, "limit": true, "throttled": true, "malformed": true, "json": true,
	"format": true, "range": true, "overflow": true, "empty": true, "closed": true,
}

// quotedOrBracketed strips the CONTENTS of quotes and brackets before tokenizing,
// so a literal like 'secret' or "patients" never contributes tokens even if a
// word inside it happened to be allowlisted.
var quotedOrBracketed = regexp.MustCompile(`'[^']*'|"[^"]*"|` + "`[^`]*`" + `|\([^)]*\)|\[[^\]]*\]|\{[^}]*\}`)

var nonLetters = regexp.MustCompile(`[^a-z]+`)

// scrubToStructure keeps only allowlisted structural tokens from text. It is the
// last-resort abstraction when no lessonRule matches; it guarantees the output
// contains no literal values.
func scrubToStructure(text string) string {
	low := strings.ToLower(text)
	low = quotedOrBracketed.ReplaceAllString(low, " ")
	tokens := nonLetters.Split(low, -1)
	kept := make([]string, 0, len(tokens))
	seen := make(map[string]bool)
	for _, tok := range tokens {
		if tok == "" || seen[tok] || !structuralVocab[tok] {
			continue
		}
		seen[tok] = true
		kept = append(kept, tok)
	}
	return strings.Join(kept, " ")
}

// argErrorRe recognizes argument-shaped failures: a malformed or invalid tool
// call (wrong/missing/badly-typed arguments). These are caught as the tool
// rejects its input rather than as a backend execution error.
var argErrorRe = regexp.MustCompile(`missing required (parameter|argument|field|property)|required .* is missing|is a required (property|field)|invalid (type|argument|parameter|json)|type mismatch|expected .* got|cannot unmarshal|json (parse|decode|unmarshal)|unmarshal .* into`)

// guardrailErrorRe recognizes validation-shaped failures: statements a guardrail
// validator rejects or the backend rejects as semantically invalid (bad SQL,
// unknown object, constraint violation). These are the "guardrail/validation"
// class (spec §11), distinct from raw runtime execution errors.
var guardrailErrorRe = regexp.MustCompile(`reserved (word|keyword)|syntax error|parse error|(relation|table|view|column|field) .* (does not exist|not found)|no such (table|column)|unknown (table|column)|constraint|duplicate|unique|not-null|cannot be null`)

// ClassifyError maps a raw error message to exactly one covered failure class.
// Priority: argument errors first (most specific to the call), then validation/
// guardrail errors, else a plain execution error. Purely lexical on the error
// text — it inspects only structure, never persists the input.
func ClassifyError(errMsg string) string {
	low := strings.ToLower(errMsg)
	switch {
	case argErrorRe.MatchString(low):
		return ClassToolArgs
	case guardrailErrorRe.MatchString(low):
		return ClassGuardrail
	default:
		return ClassToolExec
	}
}

// abstract converts a raw failure into an abstract, PII-free (problem, solution)
// pair. It never returns any substring of the raw error that is not a fixed
// canonical phrase or an allowlisted structural token.
func abstract(class, errMsg string) (problem, solution string) {
	low := strings.ToLower(errMsg)
	for _, r := range lessonRules {
		if r.re.MatchString(low) {
			return r.problem, r.solution
		}
	}
	// Fallback: structure-only description with a generic, still-actionable fix.
	p := scrubToStructure(errMsg)
	if p == "" {
		p = class + " failure"
	}
	return p, "the previous approach failed; change the arguments or strategy and retry"
}
