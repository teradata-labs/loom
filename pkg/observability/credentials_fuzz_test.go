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

// FuzzRedactCredentialString fuzzes the credential-redaction regexes and
// JSON/text cleaners in credentials.go over arbitrary untrusted text (F3).
// Properties checked:
//  1. Never panics on arbitrary text, JSON, escaped JSON, or attribute maps.
//  2. Idempotent: redacting an already-redacted string produces the exact same output.
//  3. Planted secrets (Bearer tokens, sk-/tvly-/AKIA keys, JSON credential fields,
//     URL query parameters) never survive in the redacted output.
func FuzzRedactCredentialString(f *testing.F) {
	f.Add("")
	f.Add("plain prose with basic understanding and bearer authentication")
	f.Add(`{"api_key":"sk-ant-api03-abcdefghijklmnop123456","nested":{"password":"hunter2password99"},"count":42}`)
	f.Add(`"{\"authorization\":\"Bearer tok_1234567890abcdef\",\"x_api_key\":\"secret_value_12345\"}"`)
	f.Add("Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c")
	f.Add("Set-Cookie: session_id=abc123def456; HttpOnly\r\nX-API-Key: key-9876543210")
	f.Add("api_key=my_secret_key_12345 token: tok9876543210abcdef")
	f.Add("the password is hunter2secret99 and the api key was sk-1234567890abcdefghij")
	f.Add("https://example.com/v1/search?q=loom&api_key=tvly-1234567890abcdefghij&sig=deadbeef1234")
	f.Add("AWS key AKIAIOSFODNN7EXAMPLE and GitHub ghp_1234567890abcdefghijklmnopqrstuvwxyzABCD")
	f.Add("0AKIAIOSFODNN7EXAMPLE0")
	f.Add("0000000 AKIA0000000000000000ghp_000000000000000000000000000000")

	const (
		plantedSk     = "sk-plantedFuzzSecret1234567890abcdef"
		plantedTvly   = "tvly-plantedFuzzSecret1234567890abcd"
		plantedAKIA   = "AKIAIOSFODNN7EXAMPLE"
		plantedBearer = "plantedBearerToken1234567890"
		plantedJSON   = "plantedJsonApiKey9876543210"
		sentinelStart = "\n<<<PLANTED_START>>>\n"
		sentinelEnd   = "\n<<<PLANTED_END>>>\n"
	)

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 16384 {
			input = input[:16384]
		}

		// 1. Direct string redaction and idempotence.
		first := redactCredentialString(input)
		second := redactCredentialString(first)
		if first != second {
			t.Fatalf("redactCredentialString is not idempotent:\ninput:  %q\nfirst:  %q\nsecond: %q", input, first, second)
		}

		// 2. Combined credential + PII text redaction idempotence.
		textFirst := RedactText(input)
		textSecond := RedactText(textFirst)
		if textFirst != textSecond {
			t.Fatalf("RedactText is not idempotent:\ninput:  %q\nfirst:  %q\nsecond: %q", input, textFirst, textSecond)
		}

		// 3. Planted secrets embedded between sentinels must never survive in the planted section.
		plantedBlock := fmt.Sprintf(
			"%s | %s | %s | Authorization: Bearer %s | {\"api_key\":%q}",
			plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON,
		)
		composite := input + sentinelStart + plantedBlock + sentinelEnd + input
		cleaned := redactCredentialString(composite)
		if _, rest, ok := strings.Cut(cleaned, sentinelStart); ok {
			if middle, _, ok := strings.Cut(rest, sentinelEnd); ok {
				for _, secret := range []string{plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON} {
					if strings.Contains(middle, secret) {
						t.Fatalf("planted secret %q survived redaction in planted block %q", secret, middle)
					}
				}
			}
		}

		// 4. Span-level redaction over structured values must not panic or leak planted copies.
		sp := &Span{
			Name:    "agent.tool_execution",
			TraceID: "t1",
			SpanID:  "s1",
			Attributes: map[string]interface{}{
				"input.value": composite,
				"slice":       []string{input, plantedSk},
				"nested": map[string]interface{}{
					"api_key": plantedJSON,
					"note":    input,
				},
			},
			Status: Status{Code: StatusError, Message: "failed with " + plantedSk + sentinelStart + input},
		}
		redactedSpan := RedactSpan(sp, PrivacyConfig{RedactCredentials: true, RedactPII: true})
		if inVal, ok := redactedSpan.Attributes["input.value"].(string); ok {
			if _, rest, ok := strings.Cut(inVal, sentinelStart); ok {
				if middle, _, ok := strings.Cut(rest, sentinelEnd); ok {
					for _, secret := range []string{plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON} {
						if strings.Contains(middle, secret) {
							t.Fatalf("planted secret %q survived RedactSpan input.value in %q", secret, middle)
						}
					}
				}
			}
		}
		if sl, ok := redactedSpan.Attributes["slice"].([]string); ok && len(sl) == 2 {
			if strings.Contains(sl[1], plantedSk) {
				t.Fatalf("planted secret %q survived RedactSpan slice element: %q", plantedSk, sl[1])
			}
		}
		if nested, ok := redactedSpan.Attributes["nested"].(map[string]interface{}); ok {
			if fmt.Sprint(nested["api_key"]) != RedactedValue {
				t.Fatalf("planted nested api_key = %v, want %s", nested["api_key"], RedactedValue)
			}
		}
		if beforeStatus, _, ok := strings.Cut(redactedSpan.Status.Message, sentinelStart); ok {
			if strings.Contains(beforeStatus, plantedSk) {
				t.Fatalf("planted secret %q survived RedactSpan Status.Message prefix: %q", plantedSk, beforeStatus)
			}
		}
	})
}
