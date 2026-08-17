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
// Package mcpstate seals MRTR requestState blobs (MCP 2026-07-28, decision
// D2). requestState round-trips through the client, so it is
// attacker-controlled input on return: it is AEAD-encrypted and
// authenticated (AES-256-GCM) with a key HKDF-derived from deployment secret
// material, binds the authenticated principal, and carries an expiry. Every
// unseal failure — tamper, expiry, wrong principal, unknown key — returns
// the same ErrInvalidState so failures are indistinguishable.
package mcpstate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"
)

// hkdfInfo domain-separates the sealing key from the secret's other uses
// (the same deployment secret also validates JWTs).
const hkdfInfo = "loom-mcp-requeststate-v1"

// keyIDLen prefixes each blob with a short key identifier so rotation can
// accept old keys until their expiry horizon passes.
const keyIDLen = 4

// DefaultTTL bounds replay of sealed state.
const DefaultTTL = 10 * time.Minute

// ErrInvalidState is returned for every unseal failure. Callers must not
// distinguish tamper from expiry from wrong-principal.
var ErrInvalidState = errors.New("invalid or expired request state")

// Sealer seals and unseals requestState blobs.
type Sealer struct {
	keyID []byte
	aead  cipher.AEAD
}

// NewSealer derives the sealing key from deployment secret material
// (LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET, or LOOM_MCP_STATE_SECRET on
// JWKS-only deployments — see the Phase 4 brief).
func NewSealer(secret []byte) (*Sealer, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("sealing requires non-empty secret material")
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(hkdfInfo)), key); err != nil {
		return nil, fmt.Errorf("derive sealing key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The key ID identifies which derived key sealed a blob; it is a
	// non-secret fingerprint of the key itself.
	sum := sha256.Sum256(key)
	return &Sealer{keyID: sum[:keyIDLen], aead: aead}, nil
}

// sealedPayload is the authenticated plaintext.
type sealedPayload struct {
	Principal string          `json:"p"`
	ExpiresAt int64           `json:"e"` // unix seconds
	Data      json.RawMessage `json:"d,omitempty"`
}

// Seal produces an opaque requestState string bound to the principal and
// valid for ttl (DefaultTTL when ttl <= 0).
func (s *Sealer) Seal(principal string, data []byte, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	plain, err := json.Marshal(sealedPayload{
		Principal: principal,
		ExpiresAt: time.Now().Add(ttl).Unix(),
		Data:      data,
	})
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	blob := make([]byte, 0, keyIDLen+len(nonce)+len(plain)+s.aead.Overhead())
	blob = append(blob, s.keyID...)
	blob = append(blob, nonce...)
	blob = s.aead.Seal(blob, nonce, plain, s.keyID)
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// Unseal verifies and opens a requestState string for the given principal.
func (s *Sealer) Unseal(principal, state string) ([]byte, error) {
	blob, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return nil, ErrInvalidState
	}
	minLen := keyIDLen + s.aead.NonceSize()
	if len(blob) < minLen {
		return nil, ErrInvalidState
	}
	keyID := blob[:keyIDLen]
	if string(keyID) != string(s.keyID) {
		return nil, ErrInvalidState // unknown or rotated-out key
	}
	nonce := blob[keyIDLen:minLen]
	plain, err := s.aead.Open(nil, nonce, blob[minLen:], keyID)
	if err != nil {
		return nil, ErrInvalidState
	}
	var payload sealedPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, ErrInvalidState
	}
	if payload.Principal != principal {
		return nil, ErrInvalidState
	}
	if time.Now().Unix() > payload.ExpiresAt {
		return nil, ErrInvalidState
	}
	return payload.Data, nil
}
