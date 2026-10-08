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

// Package filestore keeps saved Lua scripts in a directory: <name>.lua holds
// the source, <name>.json the metadata, and attachments.json the per-agent
// attachments. Loom is single-tenant per server, so every script is the
// runner's own (TrustOwn).
package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
)

const attachmentsFile = "attachments.json"

// Store is a directory-backed store.ScriptStore.
//
// Two locks, neither ever held while a script runs:
//   - persistMu serializes every write as one unit (validate, update the
//     index, write the files). It is the only lock held across file I/O, so
//     two writers can never rename snapshots out of order.
//   - mu guards the in-memory index and is held only around map operations,
//     so reads never wait on file I/O. Writers take persistMu first, then mu
//     briefly; readers take only mu. The order is fixed, so it cannot
//     deadlock.
type Store struct {
	dir string
	lim luasandbox.Limits
	now func() time.Time

	persistMu sync.Mutex

	mu          sync.RWMutex
	scripts     map[string]store.Script
	attachments map[string][]string // agent -> sorted script names

	loadErrs []error
}

var _ store.ScriptStore = (*Store)(nil)

// meta is <name>.json.
type meta struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Manifest    *store.Manifest `json:"manifest,omitempty"`
	Version     int             `json:"version"`
	Published   bool            `json:"published"`
	Owner       string          `json:"owner"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Open loads the store in dir, creating the directory when needed. lim bounds
// the sources Save accepts (the same limits run_lua runs them under). A
// script whose files are missing or corrupt is skipped and reported by
// LoadErrors; only an unusable directory is an error.
func Open(dir string, lim luasandbox.Limits) (*Store, error) {
	if dir == "" {
		return nil, errors.New("filestore: empty directory")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("filestore: %w", err)
	}
	s := &Store{
		dir:         dir,
		lim:         lim.Normalize(),
		now:         time.Now,
		scripts:     make(map[string]store.Script),
		attachments: make(map[string][]string),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadErrors returns the problems Open skipped over, for the caller to log.
func (s *Store) LoadErrors() []error { return append([]error(nil), s.loadErrs...) }

func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("filestore: %w", err)
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || e.Name() == attachmentsFile {
			continue
		}
		sc, err := s.readScript(name)
		if err != nil {
			s.loadErrs = append(s.loadErrs, err)
			continue
		}
		s.scripts[name] = sc
	}
	b, err := os.ReadFile(filepath.Join(s.dir, attachmentsFile)) // #nosec G304 -- fixed file name inside the store directory
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		s.loadErrs = append(s.loadErrs, fmt.Errorf("filestore: %s: %w", attachmentsFile, err))
	default:
		var att map[string][]string
		if err := json.Unmarshal(b, &att); err != nil {
			s.loadErrs = append(s.loadErrs, fmt.Errorf("filestore: %s: %w", attachmentsFile, err))
			break
		}
		for agent, names := range att {
			var kept []string
			for _, n := range names {
				if _, ok := s.scripts[n]; ok {
					kept = append(kept, n)
				}
			}
			if len(kept) > 0 {
				sort.Strings(kept)
				s.attachments[agent] = kept
			}
		}
	}
	return nil
}

func (s *Store) readScript(name string) (store.Script, error) {
	if err := store.ValidateName(name); err != nil {
		return store.Script{}, fmt.Errorf("filestore: skipping %s.json: %w", name, err)
	}
	b, err := os.ReadFile(s.path(name, ".json")) // #nosec G304 -- name validated against the script-name pattern
	if err != nil {
		return store.Script{}, fmt.Errorf("filestore: %s: %w", name, err)
	}
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return store.Script{}, fmt.Errorf("filestore: %s.json: %w", name, err)
	}
	if m.Name != name {
		return store.Script{}, fmt.Errorf("filestore: %s.json names script %q", name, m.Name)
	}
	src, err := os.ReadFile(s.path(name, ".lua")) // #nosec G304 -- name validated against the script-name pattern
	if err != nil {
		return store.Script{}, fmt.Errorf("filestore: %s: %w", name, err)
	}
	return store.Script{
		Name:        m.Name,
		Description: m.Description,
		Source:      string(src),
		Manifest:    m.Manifest,
		Version:     m.Version,
		Published:   m.Published,
		Owner:       m.Owner,
		UpdatedAt:   m.UpdatedAt,
	}, nil
}

func (s *Store) path(name, ext string) string { return filepath.Join(s.dir, name+ext) }

// writeFile writes data atomically: a temp file in the same directory, then
// a rename over the target.
func (s *Store) writeFile(target string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmpName, target)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	return nil
}

func (s *Store) writeMeta(sc store.Script) error {
	b, err := json.MarshalIndent(meta{
		Name: sc.Name, Description: sc.Description, Manifest: sc.Manifest, Version: sc.Version,
		Published: sc.Published, Owner: sc.Owner, UpdatedAt: sc.UpdatedAt,
	}, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(s.path(sc.Name, ".json"), append(b, '\n'))
}

// writeAttachments persists att. Called with persistMu held.
func (s *Store) writeAttachments(att map[string][]string) error {
	b, err := json.MarshalIndent(att, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(filepath.Join(s.dir, attachmentsFile), append(b, '\n'))
}

// copyAttachments returns a deep copy of the index's attachments.
func (s *Store) copyAttachments() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.attachments))
	for agent, names := range s.attachments {
		out[agent] = append([]string(nil), names...)
	}
	return out
}

// Save implements store.ScriptStore.
func (s *Store) Save(_ context.Context, sc store.Script, overwrite bool) (store.Script, bool, error) {
	if err := store.ValidateScript(sc, s.lim); err != nil {
		return store.Script{}, false, err
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	old, exists := s.scripts[sc.Name]
	s.mu.RUnlock()
	if exists && !overwrite {
		return store.Script{}, false, fmt.Errorf("%w: %s is at version %d", store.ErrDuplicate, sc.Name, old.Version)
	}
	sc.Version = 1
	if exists {
		sc.Version = old.Version + 1
		sc.Published = old.Published // a new version keeps its published state and attachments
		if sc.Owner == "" {
			sc.Owner = old.Owner
		}
	} else {
		sc.Published = false
	}
	sc.Trust = 0
	sc.UpdatedAt = s.now().UTC()

	// Source first, then metadata: a crash in between leaves the old
	// metadata naming the new source, never metadata without a source.
	if err := s.writeFile(s.path(sc.Name, ".lua"), []byte(sc.Source)); err != nil {
		return store.Script{}, false, fmt.Errorf("filestore: save %s: %w", sc.Name, err)
	}
	if err := s.writeMeta(sc); err != nil {
		return store.Script{}, false, fmt.Errorf("filestore: save %s: %w", sc.Name, err)
	}
	s.mu.Lock()
	s.scripts[sc.Name] = sc
	s.mu.Unlock()
	return sc, !exists, nil
}

// Get implements store.ScriptStore.
func (s *Store) Get(_ context.Context, name string) (store.Script, error) {
	s.mu.RLock()
	sc, ok := s.scripts[name]
	s.mu.RUnlock()
	if !ok {
		return store.Script{}, fmt.Errorf("%w: %s", store.ErrNotFound, name)
	}
	return sc, nil
}

// GetForRunner implements store.ScriptStore. Every script on a loom server is
// the runner's own.
func (s *Store) GetForRunner(ctx context.Context, name string) (store.Script, error) {
	sc, err := s.Get(ctx, name)
	if err != nil {
		return store.Script{}, err
	}
	sc.Trust = luasandbox.TrustOwn
	return sc, nil
}

// List implements store.ScriptStore.
func (s *Store) List(context.Context) ([]store.Script, error) {
	s.mu.RLock()
	out := make([]store.Script, 0, len(s.scripts))
	for _, sc := range s.scripts {
		out = append(out, sc)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete implements store.ScriptStore.
func (s *Store) Delete(_ context.Context, name string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	_, ok := s.scripts[name]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", store.ErrNotFound, name)
	}
	att := s.copyAttachments()
	changed := false
	for agent, names := range att {
		if kept := without(names, name); len(kept) != len(names) {
			changed = true
			if len(kept) == 0 {
				delete(att, agent)
			} else {
				att[agent] = kept
			}
		}
	}
	if changed {
		if err := s.writeAttachments(att); err != nil {
			return fmt.Errorf("filestore: delete %s: %w", name, err)
		}
	}
	// Metadata first: without it the script is gone on the next load even if
	// removing the source fails.
	for _, ext := range []string{".json", ".lua"} {
		if err := os.Remove(s.path(name, ext)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("filestore: delete %s: %w", name, err)
		}
	}
	s.mu.Lock()
	delete(s.scripts, name)
	s.attachments = att
	s.mu.Unlock()
	return nil
}

// SetPublished implements store.ScriptStore.
func (s *Store) SetPublished(_ context.Context, name string, published bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	sc, ok := s.scripts[name]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", store.ErrNotFound, name)
	}
	if sc.Published == published {
		return nil
	}
	sc.Published = published
	if err := s.writeMeta(sc); err != nil {
		return fmt.Errorf("filestore: publish %s: %w", name, err)
	}
	s.mu.Lock()
	s.scripts[name] = sc
	s.mu.Unlock()
	return nil
}

// Attach implements store.ScriptStore.
func (s *Store) Attach(_ context.Context, agent, name string) error {
	if agent == "" {
		return fmt.Errorf("%w: empty agent name", store.ErrInvalid)
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	sc, ok := s.scripts[name]
	s.mu.RUnlock()
	switch {
	case !ok:
		return fmt.Errorf("%w: %s", store.ErrNotFound, name)
	case !sc.Published:
		return fmt.Errorf("%w: %s", store.ErrNotPublished, name)
	}
	att := s.copyAttachments()
	for _, n := range att[agent] {
		if n == name {
			return nil
		}
	}
	att[agent] = append(att[agent], name)
	sort.Strings(att[agent])
	if err := s.writeAttachments(att); err != nil {
		return fmt.Errorf("filestore: attach %s: %w", name, err)
	}
	s.mu.Lock()
	s.attachments = att
	s.mu.Unlock()
	return nil
}

// Detach implements store.ScriptStore.
func (s *Store) Detach(_ context.Context, agent, name string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	att := s.copyAttachments()
	names := att[agent]
	kept := without(names, name)
	if len(kept) == len(names) {
		return nil
	}
	if len(kept) == 0 {
		delete(att, agent)
	} else {
		att[agent] = kept
	}
	if err := s.writeAttachments(att); err != nil {
		return fmt.Errorf("filestore: detach %s: %w", name, err)
	}
	s.mu.Lock()
	s.attachments = att
	s.mu.Unlock()
	return nil
}

// Attachments implements store.ScriptStore.
func (s *Store) Attachments(_ context.Context, agent string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.attachments[agent]...), nil
}

// AttachedAgents implements store.ScriptStore.
func (s *Store) AttachedAgents(_ context.Context, name string) ([]string, error) {
	s.mu.RLock()
	var out []string
	for agent, names := range s.attachments {
		for _, n := range names {
			if n == name {
				out = append(out, agent)
				break
			}
		}
	}
	s.mu.RUnlock()
	sort.Strings(out)
	return out, nil
}

func without(names []string, name string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}
