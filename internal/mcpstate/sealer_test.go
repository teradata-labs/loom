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
package mcpstate

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealUnsealRoundTrip(t *testing.T) {
	s, err := NewSealer([]byte("deployment-secret-material"))
	require.NoError(t, err)

	state, err := s.Seal("user-a", []byte(`{"pending":"drop table"}`), 0)
	require.NoError(t, err)
	require.NotEmpty(t, state)

	data, err := s.Unseal("user-a", state)
	require.NoError(t, err)
	assert.JSONEq(t, `{"pending":"drop table"}`, string(data))
}

func TestUnsealFailuresAreUniform(t *testing.T) {
	s, err := NewSealer([]byte("deployment-secret-material"))
	require.NoError(t, err)
	state, err := s.Seal("user-a", []byte(`{"x":1}`), time.Minute)
	require.NoError(t, err)

	// Tamper: flip one byte of the ciphertext.
	raw, _ := base64.RawURLEncoding.DecodeString(state)
	raw[len(raw)-1] ^= 0x01
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	// A different key (rotation) rejects too.
	other, err := NewSealer([]byte("some-other-secret"))
	require.NoError(t, err)

	cases := map[string]error{}
	_, cases["tamper"] = s.Unseal("user-a", tampered)
	_, cases["wrong principal"] = s.Unseal("user-b", state)
	_, cases["not base64"] = s.Unseal("user-a", "!!!not-base64!!!")
	_, cases["truncated"] = s.Unseal("user-a", base64.RawURLEncoding.EncodeToString([]byte("xx")))
	_, cases["rotated key"] = other.Unseal("user-a", state)

	for name, err := range cases {
		assert.ErrorIs(t, err, ErrInvalidState, "%s must fail with the uniform error", name)
	}
}

func TestUnsealExpiry(t *testing.T) {
	s, err := NewSealer([]byte("deployment-secret-material"))
	require.NoError(t, err)

	// Seal with a 1-nanosecond TTL that is already past by the time we unseal.
	shortState, err := s.Seal("user-a", []byte(`{}`), time.Nanosecond)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // expiry has 1s granularity (unix seconds)
	_, err = s.Unseal("user-a", shortState)
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestSealerReplicaEquivalence(t *testing.T) {
	// Two sealers from the same secret (two replicas) interoperate.
	a, err := NewSealer([]byte("shared-secret"))
	require.NoError(t, err)
	b, err := NewSealer([]byte("shared-secret"))
	require.NoError(t, err)

	state, err := a.Seal("user-a", []byte(`{"hop":"replica"}`), time.Minute)
	require.NoError(t, err)
	data, err := b.Unseal("user-a", state)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hop":"replica"}`, string(data))
}

// TestSealerRotationKeyring (review finding 13, PR #328 — decision D2):
// state sealed before a key rotation stays unsealable through its TTL when
// the old secret rides the ring; a secret absent from the ring is rejected.
func TestSealerRotationKeyring(t *testing.T) {
	oldSealer, err := NewSealer([]byte("old-secret-material"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := oldSealer.Seal("user-a", []byte(`{"step":1}`), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Rotated deployment: new current key, old key retained in the ring.
	rotated, err := NewSealerWithPrevious([]byte("new-secret-material"), []byte("old-secret-material"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := rotated.Unseal("user-a", state)
	if err != nil {
		t.Fatalf("pre-rotation state must unseal during the retention horizon: %v", err)
	}
	if string(data) != `{"step":1}` {
		t.Fatalf("payload mismatch: %s", data)
	}

	// New state seals with the NEW key: the old sealer must reject it.
	newState, err := rotated.Seal("user-a", []byte(`{"step":2}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldSealer.Unseal("user-a", newState); err == nil {
		t.Fatal("old sealer must not open state sealed by the rotated key")
	}

	// A ring without the old key rejects pre-rotation state.
	fresh, err := NewSealer([]byte("new-secret-material"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Unseal("user-a", state); err == nil {
		t.Fatal("rotated-out keys must be rejected once dropped from the ring")
	}
}

// TestSealRejectsOversizedData (CodeQL alert 671): the plaintext bound keeps
// the ciphertext-allocation arithmetic overflow-free and refuses state blobs
// that could never be legitimate.
func TestSealRejectsOversizedData(t *testing.T) {
	s, err := NewSealer([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seal("user", make([]byte, MaxDataLen+1), 0); err == nil {
		t.Fatal("oversized data must be rejected before allocation")
	}
	// Data is a json.RawMessage: the at-the-bound case must be valid JSON.
	atBound := []byte(`"` + strings.Repeat("a", MaxDataLen-2) + `"`)
	if _, err := s.Seal("user", atBound, 0); err != nil {
		t.Fatalf("data at the bound must seal: %v", err)
	}
	// The principal is inside the sealed payload too: a payload-sized
	// principal must not slip past the data bound.
	if _, err := s.Seal(strings.Repeat("p", maxSealedPlaintextLen), atBound, 0); err == nil {
		t.Fatal("oversized principal must be rejected before allocation")
	}
}
