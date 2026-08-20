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

// MaxDataLen bounds the plaintext a single requestState may carry (1 MiB).
// State approaching the transport's whole-request cap is a defect.
const MaxDataLen = 1 << 20

// maxSealedPlaintextLen bounds the full marshaled payload — data, principal,
// and JSON envelope — immediately before the ciphertext allocation, keeping
// the size arithmetic there provably below any integer-overflow horizon
// (CodeQL go/allocation-size-overflow, alert 671). The slack over MaxDataLen
// covers the envelope and a sane principal; principals are identity strings,
// not payloads.
const maxSealedPlaintextLen = MaxDataLen + 4096

// ErrInvalidState is returned for every unseal failure. Callers must not
// distinguish tamper from expiry from wrong-principal.
var ErrInvalidState = errors.New("invalid or expired request state")

// Sealer seals and unseals requestState blobs. It holds a keyring (decision
// D2): sealing always uses the current key, while unsealing accepts any key
// in the ring, identified by the blob's key-ID prefix — so a rotation keeps
// previously sealed state (an MRTR retry straddling the rotation, or a
// rolling deploy) valid until its own TTL expires rather than rejecting it
// immediately.
type Sealer struct {
	currentID []byte
	current   cipher.AEAD
	ring      map[string]cipher.AEAD // keyID → AEAD, current included
}

// NewSealer derives the sealing key from deployment secret material
// (LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET, or LOOM_MCP_STATE_SECRET on
// JWKS-only deployments — see the Phase 4 brief). The ring holds only the
// current key; use NewSealerWithPrevious during rotations.
func NewSealer(secret []byte) (*Sealer, error) {
	return NewSealerWithPrevious(secret)
}

// NewSealerWithPrevious derives the current sealing key from secret and adds
// one ring entry per previous secret (deployment convention:
// LOOM_MCP_STATE_SECRET_PREVIOUS, comma-separated, retained through the
// sealed-state TTL horizon after a rotation). Sealing uses only the current
// key; previous keys only unseal.
func NewSealerWithPrevious(secret []byte, previous ...[]byte) (*Sealer, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("sealing requires non-empty secret material")
	}
	currentID, currentAEAD, err := deriveKey(secret)
	if err != nil {
		return nil, err
	}
	ring := map[string]cipher.AEAD{string(currentID): currentAEAD}
	for _, prev := range previous {
		if len(prev) == 0 {
			continue
		}
		id, aead, err := deriveKey(prev)
		if err != nil {
			return nil, fmt.Errorf("derive previous sealing key: %w", err)
		}
		ring[string(id)] = aead
	}
	return &Sealer{currentID: currentID, current: currentAEAD, ring: ring}, nil
}

// deriveKey turns secret material into an AEAD plus its non-secret key-ID
// fingerprint.
func deriveKey(secret []byte) ([]byte, cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(hkdfInfo)), key); err != nil {
		return nil, nil, fmt.Errorf("derive sealing key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(key)
	return sum[:keyIDLen], aead, nil
}

// sealedPayload is the authenticated plaintext.
type sealedPayload struct {
	Principal string          `json:"p"`
	ExpiresAt int64           `json:"e"` // unix seconds
	Data      json.RawMessage `json:"d,omitempty"`
}

// Seal produces an opaque requestState string bound to the principal and
// valid for ttl (DefaultTTL when ttl <= 0). Data larger than MaxDataLen is
// rejected.
func (s *Sealer) Seal(principal string, data []byte, ttl time.Duration) (string, error) {
	if len(data) > MaxDataLen {
		return "", fmt.Errorf("request state data exceeds %d bytes", MaxDataLen)
	}
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
	if len(plain) > maxSealedPlaintextLen {
		return "", fmt.Errorf("sealed payload exceeds %d bytes", maxSealedPlaintextLen)
	}
	nonce := make([]byte, s.current.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	blob := make([]byte, 0, keyIDLen+len(nonce)+len(plain)+s.current.Overhead())
	blob = append(blob, s.currentID...)
	blob = append(blob, nonce...)
	blob = s.current.Seal(blob, nonce, plain, s.currentID)
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// Unseal verifies and opens a requestState string for the given principal.
func (s *Sealer) Unseal(principal, state string) ([]byte, error) {
	blob, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return nil, ErrInvalidState
	}
	keyID := blob[:min(keyIDLen, len(blob))]
	aead, known := s.ring[string(keyID)]
	if !known {
		return nil, ErrInvalidState // unknown or rotated-out key
	}
	minLen := keyIDLen + aead.NonceSize()
	if len(blob) < minLen {
		return nil, ErrInvalidState
	}
	nonce := blob[keyIDLen:minLen]
	plain, err := aead.Open(nil, nonce, blob[minLen:], keyID)
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
