// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package workspace is where work lives.
//
// A workspace is one body of work: the repositories it spans, what the machine
// knows about it, its units, its runs, its sessions and its memory. Every
// session binds to exactly one, for its whole life, and that binding is the
// scope of permissions, credentials, budget, retention and evidence.
//
// It is a row and a directory. The row is the registry and the bookkeeping;
// the directory is the world the work acts on. Nothing essential lives only in
// memory, which is what lets a workspace be listed, reopened tomorrow, and
// handed to someone else.
package workspace

import (
	"context"
	"errors"
	"time"
)

// Kind decides the shape of pack/ and units/. The machinery directories are
// identical for every kind; that is what lets one harness serve them all.
type Kind string

const (
	KindSoftwareDelivery Kind = "software-delivery"
	KindOptimisation     Kind = "optimisation"
	KindScratch          Kind = "scratch"
)

// Valid reports whether k is a kind this package knows how to lay out.
func (k Kind) Valid() bool {
	switch k {
	case KindSoftwareDelivery, KindOptimisation, KindScratch:
		return true
	}
	return false
}

// Status is a workspace's lifecycle. Deleting is a status change; the directory
// is removed only by the ws-manager, on permission.
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived" // hidden from the menu; refuses new sessions
	StatusDeleted  Status = "deleted"
)

// The six directories every workspace has. .tera, artifacts and scratch are the
// same in every kind; pack and units take their shape from the kind.
const (
	DirMachinery = ".tera"
	DirPack      = "pack"
	DirUnits     = "units"
	DirRepos     = "repos"
	DirArtifacts = "artifacts"
	DirScratch   = "scratch"
)

// Layout is every directory a workspace root contains, in creation order.
var Layout = []string{DirMachinery, DirPack, DirUnits, DirRepos, DirArtifacts, DirScratch}

// Workspace is the row.
type Workspace struct {
	ID          string
	OwnerUserID string
	Name        string
	Kind        Kind
	RootPath    string
	IsPersonal  bool
	Status      Status

	// Config is what tools need at spawn: caps, versioning boundaries,
	// retention, git policy, environments, local tuning. A document, because
	// its shape varies by kind and nothing in it is queried.
	Config map[string]any

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repo is one repository the workspace works in.
//
// The URL is the truth. A path the user gave was read once to learn the URL
// and the branch, and is not kept: tera clones every repo once into its own
// pool, and each workspace's worktree is cut from that clone — never from a
// checkout the user could delete.
type Repo struct {
	WorkspaceID  string
	Name         string // how specs and prompts refer to it: "loom", not a URL
	URL          string
	Ref          string // the branch this workspace works from
	WorktreePath string // under repos/, tera-controlled
}

var (
	ErrNotFound        = errors.New("workspace: not found")
	ErrExists          = errors.New("workspace: already exists")
	ErrInvalidKind     = errors.New("workspace: unknown kind")
	ErrArchived        = errors.New("workspace: archived")
	ErrSessionNotFound = errors.New("workspace: session not found")

	// ErrAlreadyBound is the binding-for-life invariant made structural: a
	// session's workspace is set once and there is no update path.
	ErrAlreadyBound = errors.New("workspace: session already bound")
)

// Store is the registry. One interface, two implementations — SQLite locally
// and Postgres in cloud — following the session store's pattern, so the CLI
// and cloud read and write identical rows. A workspace is its owner's: every
// call names the owner, and one that names a workspace or session the owner
// does not have reads it as missing and changes nothing.
type Store interface {
	Create(ctx context.Context, ws *Workspace) error
	Get(ctx context.Context, owner, id string) (*Workspace, error)
	GetByName(ctx context.Context, owner, name string) (*Workspace, error)

	// Personal returns the owner's personal workspace — the default, exactly
	// one per owner — or ErrNotFound.
	Personal(ctx context.Context, owner string) (*Workspace, error)

	// List returns active and archived workspaces, most recently updated
	// first. Deleted ones are never listed.
	List(ctx context.Context, owner string) ([]*Workspace, error)

	SetStatus(ctx context.Context, owner, id string, status Status) error
	UpdateConfig(ctx context.Context, owner, id string, config map[string]any) error

	AddRepo(ctx context.Context, owner string, repo *Repo) error
	RemoveRepo(ctx context.Context, owner, workspaceID, name string) error
	Repos(ctx context.Context, owner, workspaceID string) ([]*Repo, error)

	// BindSession sets a session's workspace once. A second call for the same
	// session returns ErrAlreadyBound, whatever the workspace.
	BindSession(ctx context.Context, owner, sessionID, workspaceID string) error
	SessionWorkspace(ctx context.Context, owner, sessionID string) (string, error)

	// Sessions lists the session ids bound to a workspace, newest first.
	Sessions(ctx context.Context, owner, workspaceID string) ([]string, error)

	// AdoptUnboundSessions binds every session of the owner that has no
	// workspace yet. Used once, when the personal workspace is created, so
	// sessions from before workspaces existed have somewhere to live.
	AdoptUnboundSessions(ctx context.Context, owner, workspaceID string) (int64, error)
}
