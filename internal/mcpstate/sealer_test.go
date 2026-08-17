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
