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
package observability

// DefaultMaxContentLength is the number of runes TruncateContent keeps.
const DefaultMaxContentLength = 200

// RedactionWindowRunes is the number of leading runes RedactPreview redacts.
// The window is much larger than DefaultMaxContentLength so that a credential
// whose key or value starts inside the preview is seen with its context; a
// credential value cut by the window edge is caught by the unterminated-value
// rule in the credential redactor. Redacting only the window keeps the cost
// of a preview independent of the size of a tool result or LLM input.
const RedactionWindowRunes = 4096

// truncationMarker is appended to content that TruncateContent shortened.
const truncationMarker = "…"

// TruncateContent bounds content recorded on spans. It keeps the first
// DefaultMaxContentLength runes and appends "…" when it shortened s.
func TruncateContent(s string) string {
	runes := []rune(s)
	if len(runes) <= DefaultMaxContentLength {
		return s
	}
	return string(runes[:DefaultMaxContentLength]) + truncationMarker
}

// RedactPreview returns the bounded span preview of s: credentials and PII are
// removed from the first RedactionWindowRunes runes, then the result is
// truncated by TruncateContent. Redaction always runs before truncation, so a
// secret that crosses the 200-rune boundary is never cut into a fragment that
// no longer matches a pattern.
func RedactPreview(s string) string {
	preview, _ := RedactPreviewWhole(s)
	return preview
}

// RedactPreviewWhole is RedactPreview that also reports whether the preview
// holds all of s (after redaction), that is, whether nothing was cut by the
// redaction window or by truncation.
func RedactPreviewWhole(s string) (preview string, whole bool) {
	window, cut := runePrefix(s, RedactionWindowRunes)
	redacted := RedactText(window)
	preview = TruncateContent(redacted)
	return preview, !cut && preview == redacted
}

// runePrefix returns the first n runes of s, and whether s was longer.
func runePrefix(s string, n int) (string, bool) {
	if len(s) <= n { // n bytes hold at most n runes
		return s, false
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i], true
		}
		count++
	}
	return s, false
}
