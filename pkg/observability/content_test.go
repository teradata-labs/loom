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
	"fmt"
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
	v, _ := redacted.Attributes["message.preview"].(string)
	if strings.Contains(v, "555-123-4567") || strings.Contains(v, "a@b.example") {
		t.Error("preview still contains PII after redaction")
	}
	if !strings.Contains(v, "[PHONE_REDACTED]") || !strings.Contains(v, "[EMAIL_REDACTED]") {
		t.Error("preview lacks redaction markers")
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

func TestRedactPreviewNestedCredentialBeyondWindow(t *testing.T) {
	prefix := `{"credentials":{"meta":{"v":1},"value":"S3cr3tValueXYZ`
	suffix := `"}}`
	input := prefix + strings.Repeat("x", 7866-len(prefix)-len(suffix)) + suffix
	if len([]rune(input)) != 7866 {
		t.Fatal("test document does not have the intended length")
	}
	control := TruncateContent(input)
	if !strings.Contains(control, "S3cr3tValueXYZ") {
		t.Fatal("unredacted control does not contain the planted secret")
	}
	got := RedactPreview(input)
	if strings.Contains(got, "S3cr3tValueXYZ") || !strings.Contains(got, RedactedValue) {
		t.Fatalf("nested credential was not redacted: %q", got)
	}
}

func TestRedactPreviewCredentialStraddlesPreviewLimit(t *testing.T) {
	input := strings.Repeat("x", 180) + " password=SecretAcrossPreviewBoundary12345" + strings.Repeat("y", 300)
	control := TruncateContent(input)
	if !strings.Contains(control, "Secret") {
		t.Fatal("unredacted control does not contain the credential prefix")
	}
	if got := RedactPreview(input); strings.Contains(got, "Secret") || !strings.Contains(got, RedactedValue) {
		t.Fatalf("credential prefix survived: %q", got)
	}
}

func TestRedactPreviewJWTStraddlesWindow(t *testing.T) {
	header := "eyJhbGciOiJIUzI1NiJ9"
	claims := "eyJzdWIiOiJ1c2VyQGV4YW1wbGUuY29t"
	signature := "signature0123456789"
	payload := claims + strings.Repeat("A", 5000-len(header)-len(claims)-len(signature)-2)
	token := header + "." + payload + "." + signature
	if len(token) != 5000 || len(header)+1+len(payload) <= RedactionWindowRunes {
		t.Fatal("JWT does not straddle the window")
	}
	input := "token returned: " + token
	if control := TruncateContent(input); !strings.Contains(control, header+"."+claims) {
		t.Fatalf("unredacted control lacks JWT prefix: %q", control)
	}
	if got := RedactPreview(input); strings.Contains(got, header) || strings.Contains(got, claims) {
		t.Fatalf("JWT prefix survived: %q", got)
	}
}

func TestRedactPreviewBoundedWindow(t *testing.T) {
	window := strings.Repeat("界", RedactionWindowRunes-40) + ` password=window-secret`
	window += strings.Repeat("界", RedactionWindowRunes-len([]rune(window)))
	for _, size := range []int{1_000_000, 10_000_000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			input := window + strings.Repeat("z", size-RedactionWindowRunes)
			calls := 0
			got, whole := redactPreviewWhole(input, func(text string) string {
				calls++
				if text != window || len([]rune(text)) != RedactionWindowRunes {
					t.Errorf("redactor received data outside the window")
				}
				return RedactText(text)
			})
			if calls != 1 || whole || got != TruncateContent(RedactText(window)) {
				t.Fatalf("calls=%d, whole=%t, preview=%q", calls, whole, got)
			}
			prefix, cut := runePrefix(input, RedactionWindowRunes)
			if !cut || prefix != window {
				t.Error("window prefix or lookahead result is incorrect")
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

func BenchmarkRedactPreview_WindowBound(b *testing.B) {
	window := strings.Repeat("x", RedactionWindowRunes)
	for _, size := range []int{1_000_000, 10_000_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			input := window + strings.Repeat("y", size-len(window))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = RedactPreview(input)
			}
		})
	}
}
