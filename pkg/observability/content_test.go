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

func TestTruncateContent(t *testing.T) {
	long := strings.Repeat("界", 10_000) // multi-byte runes: truncation must count runes

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default keeps 200 runes", in: long, want: strings.Repeat("界", 200) + "…"},
		{name: "short content unchanged", in: "hello", want: "hello"},
		{name: "exactly at the limit unchanged", in: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateContent(tc.in); got != tc.want {
				t.Errorf("TruncateContent: got %d runes, want %d", len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

// TestRedactionAppliesToTruncatedPreview checks that PII kept inside the
// content bound is still redacted on export.
func TestRedactionAppliesToTruncatedPreview(t *testing.T) {
	preview := TruncateContent(strings.Repeat("x", 100) + " call 555-123-4567 or a@b.example" + strings.Repeat("y", 500))

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

// largeJSONResult builds a JSON array of n rows without credentials, the shape
// of a SQL or REST tool result.
func largeJSONResult(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"customer_id":1234,"name":"Customer","region":"EMEA","note":"order shipped via ground"}`)
	}
	b.WriteString("]")
	return b.String()
}

func TestRedactPreview_SmallInputMatchesFullRedaction(t *testing.T) {
	for _, in := range []string{
		"",
		"hello",
		`{"password":"hunter2hunter2","user":"dbc"}`,
		strings.Repeat("x", 150) + ` api_key=sk-abcdefghijklmnopqrstuvwxyz ` + strings.Repeat("y", 300),
	} {
		if got, want := RedactPreview(in), TruncateContent(RedactText(in)); got != want {
			t.Errorf("RedactPreview(%.40q) = %q, want %q", in, got, want)
		}
	}
}

// TestRedactPreview_RedactsOnlyTheWindow checks that the cost of a preview is
// bounded: the text after RedactionWindowRunes never reaches the redactor, so a
// 1 MB input gives the same preview as its first window.
func TestRedactPreview_RedactsOnlyTheWindow(t *testing.T) {
	in := largeJSONResult(12_000) // about 1 MB
	window, cut := runePrefix(in, RedactionWindowRunes)
	if !cut || len([]rune(window)) != RedactionWindowRunes {
		t.Fatalf("runePrefix: cut=%v runes=%d", cut, len([]rune(window)))
	}
	if got, want := RedactPreview(in), TruncateContent(RedactText(window)); got != want {
		t.Errorf("preview of the full input differs from the preview of its window")
	}
	if _, whole := RedactPreviewWhole(in); whole {
		t.Error("a cut input is not whole")
	}
	if _, whole := RedactPreviewWhole(`{"a":1}`); !whole {
		t.Error("a short input is whole")
	}
}

// TestRedactPreview_CredentialStraddlingWindowEdge covers a credential value
// that starts inside the preview and ends after the redaction window, so the
// redactor sees it without its closing quote or bracket.
func TestRedactPreview_CredentialStraddlingWindowEdge(t *testing.T) {
	secret := strings.Repeat("Zq9", RedactionWindowRunes) // longer than the window
	tests := []struct {
		name string
		in   string
	}{
		{"json string", `{"note":"x","private_key":"` + secret + `"}`},
		{"json object", `{"note":"x","credentials":{"user":"dbc","value":"` + secret + `"}}`},
		{"json array", `{"note":"x","api_keys":["` + secret + `"]}`},
		{"escaped json", `{"content":"{\"private_key\":\"` + secret + `\"}"}`},
		{"double-quoted assignment", `connect(user="dbc", password="` + secret + `")`},
		{"single-quoted assignment", `connect(user='dbc', password='` + secret + `')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactPreview(tc.in)
			if strings.Contains(got, "Zq9Zq9") {
				t.Errorf("secret prefix survived: %q", got)
			}
			if !strings.Contains(got, RedactedValue) {
				t.Errorf("no redaction marker: %q", got)
			}
		})
	}
}

func TestRedactUnterminatedCredentialValue(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"closed value kept", `{"password":"[REDACTED]","note":"ok"}`, `{"password":"[REDACTED]","note":"ok"}`},
		{"open string", `{"note":"ok","password":"abcdefgh`, `{"note":"ok","password":"[REDACTED]`},
		{"open non-credential string kept", `{"note":"abcdefgh`, `{"note":"abcdefgh`},
		{"closed object then open credential", `{"auth":{"a":"b"},"token":"abcdefgh`, `{"auth":{"a":"b"},"token":"[REDACTED]`},
		{"already redacted is a fixed point", `{"password":"[REDACTED]`, `{"password":"[REDACTED]`},
		{"bracket inside string does not close", `{"secret":["a]b`, `{"secret":[[REDACTED]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactUnterminatedCredentialValue(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func BenchmarkRedactPreview_1MBJSON(b *testing.B) {
	in := largeJSONResult(12_000)
	b.SetBytes(int64(len(in)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RedactPreview(in)
	}
}
