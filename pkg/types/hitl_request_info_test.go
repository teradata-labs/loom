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
package types

import (
	"testing"
	"time"
)

// AC3 (CT-1, loom field half): HITLRequestInfo carries the additive Kind,
// Summary, and ExpiresAt fields, populated to mirror a stored request. D-1 owns
// only that these fields exist and round-trip on the carrier; the D-2 bridge
// that populates them from the stored HumanRequest is exercised at its own tier.

// The carrier can be populated to match a stored request and reads the same
// values back.
func TestHITLRequestInfo_CarriesKindSummaryExpiresAt(t *testing.T) {
	expires := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	info := HITLRequestInfo{
		RequestID: "req-1",
		Kind:      "approval",
		Summary:   "delete_table DELETE FROM users",
		ExpiresAt: expires,
	}

	if info.Kind != "approval" {
		t.Errorf("Kind = %q, want %q", info.Kind, "approval")
	}
	if info.Summary != "delete_table DELETE FROM users" {
		t.Errorf("Summary = %q, want %q", info.Summary, "delete_table DELETE FROM users")
	}
	if !info.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, expires)
	}
}

// The additions are additive: a zero-value carrier is legal — Kind and Summary
// are empty (consumers treat an empty Kind as a question) and ExpiresAt is the
// zero time.
func TestHITLRequestInfo_ZeroValueIsLegal(t *testing.T) {
	var info HITLRequestInfo

	if info.Kind != "" {
		t.Errorf("zero-value Kind = %q, want empty (treated as question)", info.Kind)
	}
	if info.Summary != "" {
		t.Errorf("zero-value Summary = %q, want empty", info.Summary)
	}
	if !info.ExpiresAt.IsZero() {
		t.Errorf("zero-value ExpiresAt = %v, want zero time", info.ExpiresAt)
	}
}
