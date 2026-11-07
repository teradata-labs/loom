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

	const (
		plantedSk     = "sk-plantedFuzzSecret1234567890abcdef"
		plantedTvly   = "tvly-plantedFuzzSecret1234567890abcd"
		plantedAKIA   = "AKIAIOSFODNN7EXAMPLE"
		plantedBearer = "plantedBearerToken1234567890"
		plantedJSON   = "plantedJsonApiKey9876543210"
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

		// 3. Planted secrets embedded around arbitrary fuzz input must never survive.
		composite := fmt.Sprintf(
			"%s | %s | %s | %s | Authorization: Bearer %s | {\"api_key\":%q} | %s",
			input, plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON, input,
		)
		cleaned := redactCredentialString(composite)
		for _, secret := range []string{plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON} {
			if strings.Contains(cleaned, secret) {
				t.Fatalf("planted secret %q survived redaction in %q", secret, cleaned)
			}
		}

		// 4. Span-level redaction over structured values must not panic or leak planted secrets.
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
			Status: Status{Code: StatusError, Message: "failed with " + plantedSk + " " + input},
		}
		redactedSpan := RedactSpan(sp, PrivacyConfig{RedactCredentials: true, RedactPII: true})
		dumped := fmt.Sprintf("%+v", redactedSpan)
		for _, secret := range []string{plantedSk, plantedTvly, plantedAKIA, plantedBearer, plantedJSON} {
			if strings.Contains(dumped, secret) {
				t.Fatalf("planted secret %q survived RedactSpan in %s", secret, dumped)
			}
		}
	})
}
