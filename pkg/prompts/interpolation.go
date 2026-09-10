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
package prompts

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// placeholderRe matches {{.variable}} placeholders ({{.name}} syntax, like Go
// templates but simpler).
var placeholderRe = regexp.MustCompile(`\{\{\.(\w+)\}\}`)

// injectionPatterns is the single, canonical list of prompt-injection
// delimiters this package suppresses. Two mechanisms enforce it and MUST stay
// on this one list so they cannot drift apart:
//
//  1. sanitizePromptInjection removes every occurrence inside an escaped
//     value, so no single value can carry a pattern.
//  2. Interpolate's junction guard (appendGuarded/junctionFormsInjection)
//     prevents remnants from reassembling any of these patterns across the
//     boundaries where a value meets template text or another value.
//
// The junction guard derives its correctness from list invariants that are
// pinned by TestInjectionPatternInvariants: every pattern is non-empty ASCII,
// never starts or ends with a space, and never contains two consecutive
// spaces.
var injectionPatterns = []string{
	"```",              // Code blocks (backtick fence)
	"~~~",              // Code blocks (tilde fence, CommonMark)
	"###",              // Headers
	"---",              // Separators
	"System:",          // System prompts
	"Assistant:",       // Assistant prompts
	"Human:",           // Human prompts
	"[INST]",           // Instruction markers
	"[/INST]",          // Instruction markers
	"<|im_start|>",     // Instruction markers
	"<|im_end|>",       // Instruction markers
	"### Instruction:", // Alpaca-style
	"### Response:",    // Alpaca-style
}

// maxJunctionSeparators bounds appendGuarded's insertion loop, derived from
// injectionPatterns rather than hardcoded: once the junction holds a run of
// spaces longer than the longest run of spaces inside any suppressed pattern,
// no pattern can straddle it. A straddling occurrence would have to either
// contain that whole space run in its interior (impossible: no pattern holds a
// space run that long) or start/end inside it (impossible: no pattern starts
// or ends with a space — see TestInjectionPatternInvariants).
var maxJunctionSeparators = func() int {
	maxRun := 0
	for _, p := range injectionPatterns {
		run := 0
		for _, r := range p {
			if r == ' ' {
				run++
				if run > maxRun {
					maxRun = run
				}
			} else {
				run = 0
			}
		}
	}
	return maxRun + 1
}()

// Interpolate performs safe variable substitution in a prompt template.
//
// Uses {{.variable_name}} syntax (like Go templates but simpler).
// All values are escaped to prevent prompt injection attacks.
//
// Escaping alone cannot stop remnants of two sanitized values (or a value and
// the template's own text) from concatenating back into a suppressed pattern
// — e.g. two adjacent "##" remnants forming "####" ⊃ "###", or "…Sys"+"tem:…"
// forming "System:". Interpolate therefore guards every junction where a
// substituted value meets template text or another substituted value: if the
// concatenation would complete any pattern in injectionPatterns across that
// boundary, a single benign space is inserted at the junction. Value bytes are
// never deleted, so benign edges ("~/bin", "~10", "`code`") survive intact.
//
// Example:
//
//	template := "You are a {{.role}} agent for {{.backend_type}}"
//	result := Interpolate(template, map[string]interface{}{
//	    "role": "SQL",
//	    "backend_type": "Teradata",
//	})
//	// Returns: "You are a SQL agent for Teradata"
func Interpolate(template string, vars map[string]interface{}) string {
	if vars == nil {
		return template
	}

	matches := placeholderRe.FindAllStringSubmatchIndex(template, -1)
	if len(matches) == 0 {
		return template
	}

	var b strings.Builder
	b.Grow(len(template))

	// lastPieceWasValue tracks whether the piece currently ending the builder
	// came from an interpolated value (untrusted) rather than template text.
	lastPieceWasValue := false
	appendPiece := func(piece string, isValue bool) {
		if piece == "" {
			// Contributes nothing and leaves the junction state unchanged, so
			// an empty value between two template segments joins them exactly
			// as the trusted template author wrote them.
			return
		}
		// Guard every junction that touches an interpolated value on either
		// side. Template|template junctions (including kept placeholders for
		// missing variables) are trusted and joined verbatim.
		if b.Len() > 0 && (lastPieceWasValue || isValue) {
			appendGuarded(&b, piece)
		} else {
			b.WriteString(piece)
		}
		lastPieceWasValue = isValue
	}

	prev := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		appendPiece(template[prev:start], false)

		varName := template[m[2]:m[3]]
		if value, ok := vars[varName]; ok {
			appendPiece(escapeValue(value), true)
		} else {
			// Keep placeholder if variable not provided (trusted template text).
			appendPiece(template[start:end], false)
		}
		prev = end
	}
	appendPiece(template[prev:], false)

	return b.String()
}

// appendGuarded appends piece to b, first inserting single spaces at the
// junction while the concatenation would complete a suppressed injection
// pattern across the boundary. This neutralizes reassembly (two backtick
// remnants meeting, "##"+"##", "…Sys"+"tem:…") without ever deleting user
// data; benign edges pass through untouched because the guard fires only when
// a pattern would actually straddle the junction. The loop terminates within
// maxJunctionSeparators insertions (see its derivation) and inserted spaces
// can never themselves complete a pattern, since no suppressed pattern starts
// or ends with a space.
func appendGuarded(b *strings.Builder, piece string) {
	for i := 0; i < maxJunctionSeparators && junctionFormsInjection(b.String(), piece); i++ {
		b.WriteByte(' ')
	}
	b.WriteString(piece)
}

// junctionFormsInjection reports whether appending right after left would
// create an occurrence of any suppressed injection pattern that straddles the
// left/right boundary. For each pattern of length L it inspects a window of
// the last L-1 bytes of left plus the first L-1 bytes of right: both halves
// are shorter than L, so any occurrence found in the window necessarily
// straddles the junction, and any straddling occurrence has at most L-1 bytes
// on each side, so it always fits in the window. Patterns are pure ASCII, so
// byte slicing that may split a multi-byte rune at a window edge can neither
// fabricate nor hide a match.
func junctionFormsInjection(left, right string) bool {
	for _, p := range injectionPatterns {
		k := len(p) - 1
		lt := left
		if len(lt) > k {
			lt = lt[len(lt)-k:]
		}
		rh := right
		if len(rh) > k {
			rh = rh[:k]
		}
		if strings.Contains(lt+rh, p) {
			return true
		}
	}
	return false
}

// escapeValue converts a value to string and escapes it to prevent injection.
func escapeValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return escapeString(v)
	case int, int64, int32, float64, float32:
		return fmt.Sprintf("%v", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case []string:
		// Join with ", ": no suppressed pattern contains a comma, so escaped
		// elements cannot recombine into one across the separator.
		escaped := make([]string, len(v))
		for i, s := range v {
			escaped[i] = escapeString(s)
		}
		return strings.Join(escaped, ", ")
	default:
		// Default: fmt.Sprintf with escaping
		return escapeString(fmt.Sprintf("%v", v))
	}
}

// escapeString escapes special characters to prevent prompt injection.
//
// Implements multiple escaping strategies for production use:
//   - Control character removal
//   - XML/HTML entity escaping
//   - Prompt injection pattern detection
//
// Ordering invariant: every character-DELETING step runs before the pattern
// sanitization in step 5, and steps 6-7 only normalize whitespace — they
// reduce space runs to a single space and trim the edges, never removing a
// space entirely from between two non-space runs, so they cannot splice
// pattern remnants back together. Nothing after step 5 may transform
// characters (e.g. a future Unicode normalization would map U+1FEF to a
// backtick and silently resurrect the fence-injection bug).
//
// The escaped value can legitimately start or end with fence runes or pattern
// fragments ("~/bin", "`code`", "##"); escapeString preserves them. Preventing
// such edges from recombining with adjacent template text or another value
// into a suppressed pattern is the job of Interpolate's junction guard.
func escapeString(s string) string {
	// 1. Remove null bytes and invalid UTF-8
	s = strings.ReplaceAll(s, "\x00", "")
	if !utf8.ValidString(s) {
		// Fix invalid UTF-8 by replacing invalid runes
		s = strings.ToValidUTF8(s, "")
	}

	// 2. Normalize line endings (convert to spaces to prevent prompt boundary manipulation)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")

	// 3. Escape XML/HTML entities to prevent injection through markup
	s = html.EscapeString(s)

	// 4. Remove or escape other control characters (C0 and C1 control codes)
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		// Skip control characters except space
		if unicode.IsControl(r) && r != ' ' {
			continue
		}
		result.WriteRune(r)
	}
	s = result.String()

	// 5. Detect and sanitize common prompt injection patterns
	s = sanitizePromptInjection(s)

	// 6. Collapse multiple spaces
	s = strings.Join(strings.Fields(s), " ")

	// 7. Trim leading/trailing whitespace
	s = strings.TrimSpace(s)

	return s
}

// sanitizePromptInjection removes every occurrence of the suppressed injection
// patterns (see injectionPatterns) by replacing it with spaces. Replacement
// with spaces cannot create a new occurrence: no pattern starts or ends with a
// space, and the space-containing patterns require a "###" prefix that an
// earlier list entry has already removed.
func sanitizePromptInjection(s string) string {
	for _, pattern := range injectionPatterns {
		// Replace with escaped version (spaces instead of special chars)
		s = strings.ReplaceAll(s, pattern, strings.Repeat(" ", len(pattern)))
	}
	return s
}
