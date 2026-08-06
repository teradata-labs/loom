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

// Package supabaseauth implements client-side Supabase Auth for Loom CLI/TUI
// clients: a PKCE OAuth login flow, on-disk session persistence, automatic
// token refresh, and gRPC per-RPC bearer credentials.
package supabaseauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "loom"
	keyringKey     = "supabase_session"
)

// Session is a stored Supabase Auth session.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds
	ProjectRef   string `json:"project_ref"`
	URL          string `json:"url"`      // https://<ref>.supabase.co
	AnonKey      string `json:"anon_key"` // public anon key (needed for refresh)
	Email        string `json:"email,omitempty"`
}

// Expiring reports whether the access token expires within the given leeway.
func (s *Session) Expiring(leeway time.Duration) bool {
	if s.ExpiresAt == 0 {
		return false
	}
	return time.Now().Add(leeway).Unix() >= s.ExpiresAt
}

// Store persists a Session in the OS keyring, falling back to a 0600 file when
// the keyring is unavailable (e.g. headless/CI hosts).
type Store struct {
	// FilePath is the fallback session file. Created 0600 in a 0700 dir.
	FilePath string
	// UseKeyring enables the OS keyring as the primary backend.
	UseKeyring bool
}

// NewStore returns a Store that prefers the OS keyring and falls back to
// <dataDir>/auth/session.json.
func NewStore(dataDir string) *Store {
	return &Store{
		FilePath:   filepath.Join(dataDir, "auth", "session.json"),
		UseKeyring: true,
	}
}

// Save persists the session. It tries the keyring first (when enabled) and
// falls back to the 0600 file on any keyring error.
func (st *Store) Save(sess *Session) error {
	// #nosec G117 -- persisting tokens is the session store's purpose; the
	// marshaled bytes go only to the OS keyring or a 0600 file, never to logs.
	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if st.UseKeyring {
		if err := keyring.Set(keyringService, keyringKey, string(data)); err == nil {
			return nil
		}
	}
	return st.saveFile(data)
}

// Load returns the stored session, or (nil, nil) if none exists. It tries the
// keyring first (when enabled), then the file.
func (st *Store) Load() (*Session, error) {
	if st.UseKeyring {
		if v, err := keyring.Get(keyringService, keyringKey); err == nil && v != "" {
			return unmarshalSession([]byte(v))
		}
	}
	data, err := os.ReadFile(st.FilePath) // #nosec G304 -- path derived from data dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session file: %w", err)
	}
	return unmarshalSession(data)
}

// Clear removes the stored session from both backends (best effort).
func (st *Store) Clear() error {
	if st.UseKeyring {
		_ = keyring.Delete(keyringService, keyringKey)
	}
	if err := os.Remove(st.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session file: %w", err)
	}
	return nil
}

func (st *Store) saveFile(data []byte) error {
	dir := filepath.Dir(st.FilePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	if err := os.WriteFile(st.FilePath, data, 0o600); err != nil {
		return fmt.Errorf("write session file: %w", err)
	}
	return nil
}

func unmarshalSession(data []byte) (*Session, error) {
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &sess, nil
}
