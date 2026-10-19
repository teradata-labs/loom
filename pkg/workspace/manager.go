// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// PersonalName is the personal workspace's name. One per owner, created on
// first launch, kind scratch: the place unnamed work lives.
const PersonalName = "default"

// ErrPersonalNoRepos: the personal workspace references repos by path and never
// adopts them. Adoption is what naming a workspace does.
var ErrPersonalNoRepos = errors.New("workspace: the personal workspace does not adopt repos — create a named workspace")

// Manager composes the store, the directory layout and git into the operations
// the CLI and cloud call. It owns no state of its own.
type Manager struct {
	Store Store

	// Root is where workspace directories are created: Root/<name>.
	Root string

	// Pool holds tera's own clones.
	Pool *Pool

	git Git
	ids func() string
}

// New builds a manager. root is where workspaces live; poolRoot is where clones
// live (conventionally ~/.tera/repos).
func New(store Store, root, poolRoot string) *Manager {
	g := Git{}
	return &Manager{
		Store: store,
		Root:  root,
		Pool:  NewPool(poolRoot, g),
		git:   g,
		ids:   uuid.NewString,
	}
}

// Create makes a workspace: the row and the six directories. Working
// immediately, empty pack — onboarding fills it later, if the work earns it.
func (m *Manager) Create(ctx context.Context, owner, name string, kind Kind) (*Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("workspace: name is required")
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
	ws := &Workspace{
		ID:          m.ids(),
		OwnerUserID: owner,
		Name:        name,
		Kind:        kind,
		RootPath:    filepath.Join(m.Root, name),
		Status:      StatusActive,
		Config:      map[string]any{},
	}
	if err := m.layout(ws.RootPath); err != nil {
		return nil, err
	}
	if err := m.Store.Create(ctx, ws); err != nil {
		return nil, err
	}
	return ws, nil
}

// EnsurePersonal returns the owner's personal workspace, creating it on first
// launch. Sessions from before workspaces existed are adopted into it, so
// nothing is left unbound.
func (m *Manager) EnsurePersonal(ctx context.Context, owner string) (*Workspace, error) {
	ws, err := m.Store.Personal(ctx, owner)
	if err == nil {
		return ws, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	ws = &Workspace{
		ID:          m.ids(),
		OwnerUserID: owner,
		Name:        PersonalName,
		Kind:        KindScratch,
		RootPath:    filepath.Join(m.Root, PersonalName),
		IsPersonal:  true,
		Status:      StatusActive,
		Config:      map[string]any{},
	}
	if err := m.layout(ws.RootPath); err != nil {
		return nil, err
	}
	if err := m.Store.Create(ctx, ws); err != nil {
		return nil, err
	}
	if _, err := m.Store.AdoptUnboundSessions(ctx, owner, ws.ID); err != nil {
		return nil, err
	}
	return ws, nil
}

// AddRepo registers a repo from what the user gave — a path or a URL — and cuts
// its worktree. Everything else is derived: a path yields its origin URL and
// current branch and is then forgotten; a URL yields its name and, if no ref is
// given, the remote's default branch.
func (m *Manager) AddRepo(ctx context.Context, workspaceID, input, ref string) (*Repo, error) {
	ws, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if ws.IsPersonal {
		return nil, ErrPersonalNoRepos
	}
	if ws.Status != StatusActive {
		return nil, ErrArchived
	}

	var url string
	if abs, e := filepath.Abs(input); e == nil && m.git.IsRepo(ctx, abs) {
		url, err = m.git.RemoteURL(ctx, abs)
		if err != nil {
			return nil, err
		}
		if ref == "" {
			if ref, err = m.git.CurrentBranch(ctx, abs); err != nil {
				return nil, err
			}
		}
	} else {
		url = NormalizeURL(input)
	}
	name := NameFromURL(url)
	if name == "" {
		return nil, fmt.Errorf("workspace: cannot derive a repo name from %q", input)
	}

	existing, err := m.Store.Repos(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range existing {
		if r.Name == name {
			return nil, fmt.Errorf("%w: repo %s", ErrExists, name)
		}
	}

	if _, err := m.Pool.EnsureClone(ctx, name, url); err != nil {
		return nil, err
	}
	if ref == "" {
		if ref, err = m.Pool.DefaultBranch(ctx, name); err != nil {
			return nil, fmt.Errorf("workspace: no ref given and no default branch found for %s", name)
		}
	}

	repo := &Repo{
		WorkspaceID:  ws.ID,
		Name:         name,
		URL:          url,
		Ref:          ref,
		WorktreePath: filepath.Join(ws.RootPath, DirRepos, name),
	}
	if err := m.Pool.AddWorktree(ctx, name, repo.WorktreePath, ref); err != nil {
		return nil, err
	}
	if err := m.Store.AddRepo(ctx, repo); err != nil {
		_ = m.Pool.RemoveWorktree(ctx, name, repo.WorktreePath)
		return nil, err
	}
	return repo, nil
}

// RemoveRepo forgets a repo and removes its worktree. A dirty worktree refuses:
// uncommitted work is never deleted by tera.
func (m *Manager) RemoveRepo(ctx context.Context, workspaceID, name string) error {
	repos, err := m.Store.Repos(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, r := range repos {
		if r.Name != name {
			continue
		}
		if st := m.git.Worktree(ctx, r.WorktreePath); st.Exists && st.Attached {
			if err := m.Pool.RemoveWorktree(ctx, name, r.WorktreePath); err != nil {
				return fmt.Errorf("workspace: %s has uncommitted changes or could not be removed: %w", name, err)
			}
		}
		return m.Store.RemoveRepo(ctx, workspaceID, name)
	}
	return ErrNotFound
}

// Archive hides a workspace and refuses new sessions. The directory stays.
func (m *Manager) Archive(ctx context.Context, workspaceID string) error {
	return m.Store.SetStatus(ctx, workspaceID, StatusArchived)
}

// Open is what a session does before it binds: fetch the row and run the check.
func (m *Manager) Open(ctx context.Context, workspaceID string, opts CheckOptions) (*Workspace, []Finding, error) {
	ws, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if ws.Status != StatusActive {
		return ws, nil, ErrArchived
	}
	findings, err := m.Check(ctx, ws, opts)
	return ws, findings, err
}

// Bind sets a session's workspace, once.
func (m *Manager) Bind(ctx context.Context, sessionID, workspaceID string) error {
	ws, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return err
	}
	if ws.Status != StatusActive {
		return ErrArchived
	}
	return m.Store.BindSession(ctx, sessionID, workspaceID)
}

func (m *Manager) layout(root string) error {
	for _, d := range Layout {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return fmt.Errorf("workspace layout %s: %w", d, err)
		}
	}
	return nil
}

// Worktree reports the state of a worktree directory — what the /workspace
// view shows beside each repo.
func (m *Manager) Worktree(ctx context.Context, path string) WorktreeState {
	return m.git.Worktree(ctx, path)
}
