// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHawkTracer_EmailRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple email",
			input:    "Contact: john.doe@example.com",
			expected: "Contact: [EMAIL_REDACTED]",
		},
		{
			name:     "multiple emails",
			input:    "From: alice@company.com to bob@company.com",
			expected: "From: [EMAIL_REDACTED] to [EMAIL_REDACTED]",
		},
		{
			name:     "email with numbers",
			input:    "User: user123@test456.org",
			expected: "User: [EMAIL_REDACTED]",
		},
		{
			name:     "no email",
			input:    "This is just text",
			expected: "This is just text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactPII: true,
					},
				},
			}

			span := &Span{
				Attributes: map[string]interface{}{
					"message": tt.input,
				},
			}

			redacted := tracer.redact(span)
			assert.Equal(t, tt.expected, redacted.Attributes["message"])
		})
	}
}

func TestHawkTracer_PhoneRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "phone with dashes",
			input:    "Call me at 555-123-4567",
			expected: "Call me at [PHONE_REDACTED]",
		},
		{
			name:     "phone with dots",
			input:    "Phone: 555.123.4567",
			expected: "Phone: [PHONE_REDACTED]",
		},
		{
			name:     "phone without separators",
			input:    "Contact: 5551234567",
			expected: "Contact: [PHONE_REDACTED]",
		},
		{
			name:     "multiple phones",
			input:    "Office: 555-111-2222 Mobile: 555-333-4444",
			expected: "Office: [PHONE_REDACTED] Mobile: [PHONE_REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactPII: true,
					},
				},
			}

			span := &Span{
				Attributes: map[string]interface{}{
					"contact_info": tt.input,
				},
			}

			redacted := tracer.redact(span)
			assert.Equal(t, tt.expected, redacted.Attributes["contact_info"])
		})
	}
}

func TestHawkTracer_SSNRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid SSN",
			input:    "SSN: 123-45-6789",
			expected: "SSN: [SSN_REDACTED]",
		},
		{
			name:     "SSN in sentence",
			input:    "The SSN 987-65-4321 was found in records",
			expected: "The SSN [SSN_REDACTED] was found in records",
		},
		{
			name:     "multiple SSNs",
			input:    "123-45-6789 and 987-65-4321",
			expected: "[SSN_REDACTED] and [SSN_REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactPII: true,
					},
				},
			}

			span := &Span{
				Attributes: map[string]interface{}{
					"data": tt.input,
				},
			}

			redacted := tracer.redact(span)
			assert.Equal(t, tt.expected, redacted.Attributes["data"])
		})
	}
}

func TestHawkTracer_CreditCardRedaction(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "card with dashes",
			input:    "Card: 1234-5678-9012-3456",
			expected: "Card: [CARD_REDACTED]",
		},
		{
			name:     "card with spaces",
			input:    "Card: 1234 5678 9012 3456",
			expected: "Card: [CARD_REDACTED]",
		},
		{
			name:     "card without separators",
			input:    "Card: 1234567890123456",
			expected: "Card: [CARD_REDACTED]",
		},
		{
			name:     "multiple cards",
			input:    "Primary: 1111-2222-3333-4444 Backup: 5555-6666-7777-8888",
			expected: "Primary: [CARD_REDACTED] Backup: [CARD_REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactPII: true,
					},
				},
			}

			span := &Span{
				Attributes: map[string]interface{}{
					"payment": tt.input,
				},
			}

			redacted := tracer.redact(span)
			assert.Equal(t, tt.expected, redacted.Attributes["payment"])
		})
	}
}

func TestHawkTracer_CredentialKeyRemoval(t *testing.T) {
	tests := []struct {
		name         string
		attributes   map[string]interface{}
		expectRemove []string
		expectKeep   []string
	}{
		{
			name: "password keys",
			attributes: map[string]interface{}{
				"password":      "secret123",
				"user_password": "pass456",
				"database":      "mydb",
			},
			expectRemove: []string{"password"},
			expectKeep:   []string{"database"},
		},
		{
			name: "api key variations",
			attributes: map[string]interface{}{
				"api_key": "key123",
				"apikey":  "key456",
				"API_KEY": "key789",
				"service": "myservice",
			},
			expectRemove: []string{"api_key", "apikey"},
			expectKeep:   []string{"service"},
		},
		{
			name: "token variations",
			attributes: map[string]interface{}{
				"token":         "tok123",
				"access_token":  "tok456",
				"refresh_token": "tok789",
				"user_id":       "12345",
			},
			expectRemove: []string{"token", "access_token", "refresh_token"},
			expectKeep:   []string{"user_id"},
		},
		{
			name: "secret variations",
			attributes: map[string]interface{}{
				"secret":        "sec123",
				"client_secret": "sec456",
				"aws_secret":    "sec789",
				"config":        "myconfig",
			},
			expectRemove: []string{"secret", "client_secret", "aws_secret"},
			expectKeep:   []string{"config"},
		},
		{
			name: "mixed credentials",
			attributes: map[string]interface{}{
				"authorization": "Bearer xyz",
				"bearer":        "abc",
				"private_key":   "key",
				"ssh_key":       "sshkey",
				"username":      "john",
			},
			expectRemove: []string{"authorization", "bearer", "private_key", "ssh_key"},
			expectKeep:   []string{"username"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactCredentials: true,
					},
				},
			}

			span := &Span{
				Attributes: tt.attributes,
			}

			redacted := tracer.redact(span)

			// Verify removed keys are gone
			for _, key := range tt.expectRemove {
				_, exists := redacted.Attributes[key]
				assert.False(t, exists, "Expected key %s to be removed", key)
			}

			// Verify kept keys remain
			for _, key := range tt.expectKeep {
				_, exists := redacted.Attributes[key]
				assert.True(t, exists, "Expected key %s to be kept", key)
			}
		})
	}
}

func TestHawkTracer_CredentialKeyPatterns(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		shouldRemove bool
	}{
		// Should remove
		{"password in key", "user_password_hash", true},
		{"secret in key", "database_secret_key", true},
		{"token in key", "oauth_token_value", true},
		{"api key pattern", "stripe_api_key", true},

		// Should keep
		{"normal key", "user_id", false},
		{"database", "database_name", false},
		{"service", "service_url", false},
		{"metadata", "request_metadata", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &HawkTracer{
				config: HawkConfig{
					Privacy: PrivacyConfig{
						RedactCredentials: true,
					},
				},
			}

			span := &Span{
				Attributes: map[string]interface{}{
					tt.key: "some_value",
				},
			}

			redacted := tracer.redact(span)

			_, exists := redacted.Attributes[tt.key]
			if tt.shouldRemove {
				assert.False(t, exists, "Expected key %s to be removed", tt.key)
			} else {
				assert.True(t, exists, "Expected key %s to be kept", tt.key)
			}
		})
	}
}

func TestHawkTracer_AllowedAttributesBypass(t *testing.T) {
	tracer := &HawkTracer{
		config: HawkConfig{
			Privacy: PrivacyConfig{
				RedactPII:         true,
				RedactCredentials: true,
				AllowedAttributes: []string{"api_key", "user_email"},
			},
		},
	}

	span := &Span{
		Attributes: map[string]interface{}{
			"api_key":    "should-keep-this",
			"user_email": "alice@example.com",
			"password":   "should-remove",
			"message":    "Contact bob@example.com",
		},
	}

	redacted := tracer.redact(span)

	// Allowlisted attributes should be kept
	assert.Equal(t, "should-keep-this", redacted.Attributes["api_key"])
	assert.Equal(t, "alice@example.com", redacted.Attributes["user_email"])

	// Non-allowlisted credentials should be removed
	_, exists := redacted.Attributes["password"]
	assert.False(t, exists, "Non-allowlisted credential should be removed")

	// Non-allowlisted PII should be redacted
	assert.Equal(t, "Contact [EMAIL_REDACTED]", redacted.Attributes["message"])
}

func TestHawkTracer_EventRedaction(t *testing.T) {
	tracer := &HawkTracer{
		config: HawkConfig{
			Privacy: PrivacyConfig{
				RedactPII: true,
			},
		},
	}

	span := &Span{
		Attributes: map[string]interface{}{
			"safe": "data",
		},
		Events: []Event{
			{
				Name: "user.action",
				Attributes: map[string]interface{}{
					"email": "user@example.com",
					"phone": "555-123-4567",
					"note":  "Contact at john@test.com or 555-999-8888",
				},
			},
			{
				Name: "payment.processed",
				Attributes: map[string]interface{}{
					"card": "1234-5678-9012-3456",
					"ssn":  "123-45-6789",
				},
			},
		},
	}

	redacted := tracer.redact(span)

	// Verify event attributes are redacted
	require.Len(t, redacted.Events, 2)

	// First event
	assert.Equal(t, "[EMAIL_REDACTED]", redacted.Events[0].Attributes["email"])
	assert.Equal(t, "[PHONE_REDACTED]", redacted.Events[0].Attributes["phone"])
	assert.Equal(t, "Contact at [EMAIL_REDACTED] or [PHONE_REDACTED]", redacted.Events[0].Attributes["note"])

	// Second event
	assert.Equal(t, "[CARD_REDACTED]", redacted.Events[1].Attributes["card"])
	assert.Equal(t, "[SSN_REDACTED]", redacted.Events[1].Attributes["ssn"])
}

func TestHawkTracer_MixedPIIInSingleString(t *testing.T) {
	tracer := &HawkTracer{
		config: HawkConfig{
			Privacy: PrivacyConfig{
				RedactPII: true,
			},
		},
	}

	span := &Span{
		Attributes: map[string]interface{}{
			"complex": "User alice@example.com, phone 555-123-4567, SSN 123-45-6789, card 1234-5678-9012-3456",
		},
	}

	redacted := tracer.redact(span)

	expected := "User [EMAIL_REDACTED], phone [PHONE_REDACTED], SSN [SSN_REDACTED], card [CARD_REDACTED]"
	assert.Equal(t, expected, redacted.Attributes["complex"])
}

func TestHawkTracer_NoRedactionWhenDisabled(t *testing.T) {
	tracer := &HawkTracer{
		config: HawkConfig{
			Privacy: PrivacyConfig{
				RedactPII:         false,
				RedactCredentials: false,
			},
		},
	}

	span := &Span{
		Attributes: map[string]interface{}{
			"email":    "user@example.com",
			"password": "secret123",
			"phone":    "555-123-4567",
			"api_key":  "key123",
		},
	}

	redacted := tracer.redact(span)

	// Everything should remain unchanged
	assert.Equal(t, "user@example.com", redacted.Attributes["email"])
	assert.Equal(t, "secret123", redacted.Attributes["password"])
	assert.Equal(t, "555-123-4567", redacted.Attributes["phone"])
	assert.Equal(t, "key123", redacted.Attributes["api_key"])
}

func TestHawkTracer_OnlyStringValuesRedacted(t *testing.T) {
	tracer := &HawkTracer{
		config: HawkConfig{
			Privacy: PrivacyConfig{
				RedactPII: true,
			},
		},
	}

	span := &Span{
		Attributes: map[string]interface{}{
			"text":   "Email: user@example.com",
			"number": 5551234567,
			"bool":   true,
			"nil":    nil,
			"map":    map[string]string{"email": "test@example.com"},
		},
	}

	redacted := tracer.redact(span)

	// String should be redacted
	assert.Equal(t, "Email: [EMAIL_REDACTED]", redacted.Attributes["text"])

	// Non-strings should be unchanged
	assert.Equal(t, 5551234567, redacted.Attributes["number"])
	assert.Equal(t, true, redacted.Attributes["bool"])
	assert.Nil(t, redacted.Attributes["nil"])
	assert.Equal(t, map[string]string{"email": "test@example.com"}, redacted.Attributes["map"])
}
