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

import (
	"strings"
	"testing"
)

func withMaxContentLength(t *testing.T, n int) {
	t.Helper()
	SetMaxContentLength(n)
	t.Cleanup(func() { SetMaxContentLength(DefaultMaxContentLength) })
}

func TestTruncateContent(t *testing.T) {
	long := strings.Repeat("界", 10_000) // multi-byte runes: truncation must count runes

	tests := []struct {
		name  string
		limit *int
		in    string
		want  string
	}{
		{name: "default keeps 200 runes", in: long, want: strings.Repeat("界", 200) + "…"},
		{name: "short content unchanged", in: "hello", want: "hello"},
		{name: "exactly at the limit unchanged", in: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
		{name: "research limit keeps 10k runes whole", limit: intPtr(32768), in: long, want: long},
		{name: "explicit 200", limit: intPtr(200), in: long, want: strings.Repeat("界", 200) + "…"},
		{name: "zero is unlimited", limit: intPtr(0), in: long, want: long},
		{name: "negative restores default", limit: intPtr(-5), in: long, want: strings.Repeat("界", 200) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.limit != nil {
				withMaxContentLength(t, *tc.limit)
			}
			if got := TruncateContent(tc.in); got != tc.want {
				t.Errorf("TruncateContent: got %d runes, want %d", len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

// TestRedactionAppliesBeyondDefaultLength guards D1: raising the content
// limit must not let PII past the redactor, wherever it sits in the text.
func TestRedactionAppliesBeyondDefaultLength(t *testing.T) {
	withMaxContentLength(t, 0)
	preview := TruncateContent(strings.Repeat("x", 5000) + " call 555-123-4567 or a@b.example")

	redacted := redactOTelSpan(&Span{Attributes: map[string]interface{}{"message.preview": preview}},
		PrivacyConfig{RedactCredentials: true, RedactPII: true})
	got := attrMap(t, exportAttributes("agent.chat", redacted.Attributes))
	for _, k := range []string{"gen_ai.prompt", "input.value"} {
		v, _ := got[k].(string)
		if strings.Contains(v, "555-123-4567") || strings.Contains(v, "a@b.example") {
			t.Errorf("%s still contains PII after redaction", k)
		}
		if !strings.Contains(v, "[PHONE_REDACTED]") || !strings.Contains(v, "[EMAIL_REDACTED]") {
			t.Errorf("%s lacks redaction markers", k)
		}
	}
}

func intPtr(n int) *int { return &n }
