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

package importer

import (
	"regexp"
	"sort"
	"strings"
)

// keywordStopwords contains common English filler words that make poor
// trigger keywords because they match too broadly.
var keywordStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true,
	"at": true, "be": true, "by": true, "for": true, "from": true,
	"has": true, "have": true, "in": true, "into": true, "is": true,
	"it": true, "its": true, "of": true, "on": true, "or": true,
	"over": true, "the": true, "this": true, "to": true, "use": true,
	"used": true, "using": true, "via": true, "was": true, "when": true,
	"with": true, "within": true, "without": true, "you": true, "your": true,
	"any": true, "all": true, "also": true, "but": true, "can": true,
	"do": true, "does": true, "if": true, "may": true, "must": true,
	"not": true, "should": true, "than": true, "that": true, "their": true,
	"them": true, "they": true, "what": true, "which": true, "while": true,
	"who": true, "why": true, "will": true, "would": true,
}

// codeSpan matches markdown inline code spans like `MLPPI` or `RANGE_N`.
var codeSpan = regexp.MustCompile("`([A-Za-z][A-Za-z0-9_./-]{1,40})`")

// capsAcronym matches all-caps tokens of length 2-12 (PPI, MLPPI, NUPI, NOS, JSON).
var capsAcronym = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{1,11})\b`)

// genericSQLKeywords are SQL DML/DDL verbs and structural words that
// appear all-caps in any SQL-related skill. They are not distinctive
// enough to route on, so we drop them from the all-caps acronym pass.
var genericSQLKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"MERGE": true, "CREATE": true, "DROP": true, "ALTER": true,
	"GRANT": true, "REVOKE": true, "REPLACE": true, "EXPLAIN": true,
	"FROM": true, "WHERE": true, "GROUP": true, "ORDER": true,
	"HAVING": true, "JOIN": true, "INNER": true, "OUTER": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "UNION": true,
	"WITH": true, "CASE": true, "WHEN": true, "THEN": true,
	"ELSE": true, "END": true, "AND": true, "NOT": true,
	"NULL": true, "INTO": true, "VALUES": true, "SET": true,
	"AS": true, "BY": true, "OR": true, "ON": true, "IS": true,
	"DDL": true, "DML": true, "DCL": true,
	"HELP": true, "SHOW": true,
	"TRUE": true, "FALSE": true,
}

// commonShortWords are short tokens that look distinctive but appear too
// often in casual English to be useful as keyword routing signals.
var commonShortWords = map[string]bool{
	"after": true, "alter": true, "build": true, "before": true,
	"check": true, "could": true, "every": true, "first": true,
	"given": true, "issue": true, "later": true, "level": true,
	"means": true, "might": true, "needs": true, "often": true,
	"order": true, "other": true, "place": true, "since": true,
	"still": true, "thing": true, "those": true, "under": true,
	"until": true, "where": true, "whose": true, "write": true,
	"based": true, "match": true, "items": true,
}

// kwCandidate carries a keyword candidate plus a priority score so we can
// rank by distinctiveness before truncating to the 32-entry cap.
type kwCandidate struct {
	value    string
	priority int // higher = more likely to be useful
}

// MaxKeywords is the per-skill keyword cap. The router's FTS5-fallback
// scorer divides by this denominator (clamped at keywordSaturationCount=5
// in pkg/skills/library.go), so going wildly higher hurts routing recall
// without helping precision.
const MaxKeywords = 32

// BuildKeywords synthesizes trigger keywords for FTS5-fallback routing.
//
// The matcher in pkg/skills/library.go FindByKeywords scores a hit when a
// keyword is either an exact tokenized match against the user message or a
// substring of it. That makes long phrases nearly useless and short,
// distinctive tokens highly valuable, so we emit:
//
//   - the skill name and its bare suffix (e.g. "partitioning")
//   - markdown inline code spans from the SKILL body (`MLPPI`, `RANGE_N`)
//   - all-caps acronyms from the SKILL body (PPI, NUPI, NOS, TASM)
//   - 2- to 3-word non-stopword phrases from each "When to Use" bullet
//   - distinctive single tokens (>= 6 chars, not a common English short word)
//
// Candidates are scored by source priority; the top MaxKeywords are emitted.
func BuildKeywords(imp *Skill) []string {
	seen := map[string]int{} // value -> highest priority seen
	add := func(s string, priority int) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || len(s) < 3 || len(s) > 40 {
			return
		}
		single := !strings.Contains(s, " ")
		if single {
			if keywordStopwords[s] || commonShortWords[s] {
				return
			}
			if isAllDigits(s) {
				return
			}
		}
		if cur, ok := seen[s]; !ok || priority > cur {
			seen[s] = priority
		}
	}

	// Priority 100: skill name itself — always include.
	add(imp.Name, 100)
	if strings.HasPrefix(imp.Name, "teradata-") {
		add(strings.TrimPrefix(imp.Name, "teradata-"), 100)
	}

	// Priority 90: terms from the description (most curated signal).
	for _, m := range capsAcronym.FindAllStringSubmatch(imp.Description, -1) {
		token := m[1]
		if len(token) < 3 || len(token) > 12 || hasDigit(token) ||
			genericSQLKeywords[strings.ToUpper(token)] {
			continue
		}
		add(token, 90)
	}
	descSegments := splitOnAny(stripParens(imp.Description), ",;:—-(/)")
	for _, seg := range descSegments {
		words := tokenizeWords(seg)
		for n := 2; n <= 3 && n <= len(words); n++ {
			for i := 0; i+n <= len(words); i++ {
				gram := words[i : i+n]
				if keywordStopwords[gram[0]] || keywordStopwords[gram[len(gram)-1]] {
					continue
				}
				add(strings.Join(gram, " "), 90)
			}
		}
	}

	// Priority 80: inline code spans from the SKILL body. We deliberately
	// do NOT mine reference bodies — they contain too many citations and
	// unrelated SQL keywords that pollute the keyword list.
	for _, m := range codeSpan.FindAllStringSubmatch(imp.Body, -1) {
		token := m[1]
		if strings.ContainsAny(token, "/.") {
			continue
		}
		add(token, 80)
	}

	// Priority 70: all-caps acronyms from the SKILL body.
	for _, m := range capsAcronym.FindAllStringSubmatch(imp.Body, -1) {
		token := m[1]
		if len(token) < 3 || len(token) > 12 || hasDigit(token) ||
			genericSQLKeywords[strings.ToUpper(token)] {
			continue
		}
		add(token, 70)
	}

	// Priority 60: 2- and 3-word n-grams from "When to Use" bullets.
	for _, bullet := range imp.WhenToUse {
		clean := bullet
		clean = strings.TrimPrefix(clean, "Use when ")
		clean = strings.TrimPrefix(clean, "Use for ")
		clean = strings.TrimPrefix(clean, "Using ")
		clean = stripParens(clean)
		segments := splitOnAny(clean, ",;:—-(/)")
		for _, seg := range segments {
			words := tokenizeWords(seg)
			for n := 2; n <= 3 && n <= len(words); n++ {
				for i := 0; i+n <= len(words); i++ {
					gram := words[i : i+n]
					if keywordStopwords[gram[0]] || keywordStopwords[gram[len(gram)-1]] {
						continue
					}
					add(strings.Join(gram, " "), 60)
				}
			}
		}
	}

	// Rank by priority desc, then alphabetically for stable output.
	cands := make([]kwCandidate, 0, len(seen))
	for v, p := range seen {
		cands = append(cands, kwCandidate{value: v, priority: p})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].priority != cands[j].priority {
			return cands[i].priority > cands[j].priority
		}
		return cands[i].value < cands[j].value
	})

	if len(cands) > MaxKeywords {
		cands = cands[:MaxKeywords]
	}

	// Sort the final cut alphabetically so the YAML output is stable
	// regardless of insertion order from regex/parser passes.
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.value
	}
	sort.Strings(out)
	return out
}

// BuildSlashCommands derives slash commands from the skill name. Authors
// can add more aliases after import. The parent index gets a stable alias
// so it's easy to summon.
func BuildSlashCommands(imp *Skill) []string {
	if imp.IsParentIndex {
		return []string{"/" + imp.Name, "/skill-index"}
	}
	return []string{"/" + imp.Name}
}

// stripParens removes everything between balanced parentheses.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// splitOnAny splits s on any byte from seps, returning trimmed non-empty
// segments. Mirrors strings.FieldsFunc but with an explicit separator set.
func splitOnAny(s, seps string) []string {
	cut := func(r rune) bool { return strings.ContainsRune(seps, r) }
	parts := strings.FieldsFunc(s, cut)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tokenizeWords lowercases s and returns its alphabetic word tokens.
// Numeric-only tokens are dropped. Hyphenated terms are preserved as
// single tokens.
func tokenizeWords(s string) []string {
	s = strings.ToLower(s)
	cut := func(r rune) bool {
		alpha := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		return !alpha && !digit && r != '_' && r != '-'
	}
	parts := strings.FieldsFunc(s, cut)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if isAllDigits(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
