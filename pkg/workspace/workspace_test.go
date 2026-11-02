// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// The store shares the sessions database. The session store opens first — it
// owns the sessions table and adds workspace_id to it — then this package's
// tables go beside it on a second handle to the same file, which is exactly how
// the CLI wires it.
func openStores(t *testing.T) (*agent.SessionStore, *SQLiteStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tera.db")
	sess, err := agent.NewSessionStoreWithConfig(agent.DBConfig{Path: path}, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	db, err := agent.OpenDB(agent.DBConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLiteStore(context.Background(), db)
	require.NoError(t, err)
	return sess, store
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// remoteAndCheckout builds what a user actually has: a remote, and their own
// clone of it on a branch. Returns the remote URL and the checkout path.
func remoteAndCheckout(t *testing.T, name string) (url, checkout string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remotes", name+".git") // its basename is the repo's name
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
	git(t, base, "init", "--bare", "-b", "develop", remote)
	// A bare remote refuses to delete its current branch; tests delete refs.
	git(t, remote, "config", "receive.denyDeleteCurrent", "ignore")

	checkout = filepath.Join(base, name)
	git(t, base, "clone", "-q", "file://"+remote, checkout)
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "README.md"), []byte("hello\n"), 0o644))
	git(t, checkout, "add", ".")
	git(t, checkout, "commit", "-q", "-m", "init")
	git(t, checkout, "push", "-q", "origin", "HEAD:develop")
	git(t, checkout, "checkout", "-q", "-B", "develop", "origin/develop")
	return "file://" + remote, checkout
}

func newManager(t *testing.T, store Store) *Manager {
	t.Helper()
	base := t.TempDir()
	return New(store, filepath.Join(base, "ws"), filepath.Join(base, "pool"))
}

// ---------------------------------------------------------------------------

func TestCreate_MakesRowAndLayout(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)

	ws, err := m.Create(context.Background(), "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)

	for _, d := range Layout {
		assert.DirExists(t, filepath.Join(ws.RootPath, d), d)
	}
	got, err := store.Get(context.Background(), "anuj", ws.ID)
	require.NoError(t, err)
	assert.Equal(t, KindSoftwareDelivery, got.Kind)
	assert.Equal(t, StatusActive, got.Status)

	_, err = m.Create(context.Background(), "anuj", "tera", KindSoftwareDelivery)
	assert.ErrorIs(t, err, ErrExists, "names are unique per owner")
}

func TestPersonal_OnePerOwner_AdoptsUnboundSessions(t *testing.T) {
	sess, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()

	// Sessions from before workspaces existed.
	require.NoError(t, sess.SaveSession(types.ContextWithUserID(ctx, "anuj"), &agent.Session{ID: "old-1"}))
	require.NoError(t, sess.SaveSession(types.ContextWithUserID(ctx, "anuj"), &agent.Session{ID: "old-2"}))
	require.NoError(t, sess.SaveSession(types.ContextWithUserID(ctx, "someone-else"), &agent.Session{ID: "other"}))

	ws, err := m.EnsurePersonal(ctx, "anuj")
	require.NoError(t, err)
	assert.True(t, ws.IsPersonal)
	assert.Equal(t, KindScratch, ws.Kind)

	got, err := store.SessionWorkspace(ctx, "anuj", "old-1")
	require.NoError(t, err)
	assert.Equal(t, ws.ID, got, "pre-existing sessions have somewhere to live")
	got, _ = store.SessionWorkspace(ctx, "someone-else", "other")
	assert.Empty(t, got, "another owner's sessions are not touched")

	again, err := m.EnsurePersonal(ctx, "anuj")
	require.NoError(t, err)
	assert.Equal(t, ws.ID, again.ID, "second launch finds the same one")
}

func TestBind_IsSetOnce(t *testing.T) {
	sess, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()

	a, err := m.Create(ctx, "anuj", "a", KindScratch)
	require.NoError(t, err)
	b, err := m.Create(ctx, "anuj", "b", KindScratch)
	require.NoError(t, err)
	require.NoError(t, sess.SaveSession(types.ContextWithUserID(ctx, "anuj"), &agent.Session{ID: "s1"}))

	require.NoError(t, m.Bind(ctx, "anuj", "s1", a.ID))
	assert.ErrorIs(t, m.Bind(ctx, "anuj", "s1", b.ID), ErrAlreadyBound, "a session's workspace is for life")
	assert.ErrorIs(t, m.Bind(ctx, "anuj", "s1", a.ID), ErrAlreadyBound, "even to the same one")
	assert.ErrorIs(t, m.Bind(ctx, "anuj", "nope", a.ID), ErrSessionNotFound)

	require.NoError(t, m.Archive(ctx, "anuj", b.ID))
	require.NoError(t, sess.SaveSession(types.ContextWithUserID(ctx, "anuj"), &agent.Session{ID: "s2"}))
	assert.ErrorIs(t, m.Bind(ctx, "anuj", "s2", b.ID), ErrArchived, "archived refuses new sessions")

	ids, err := store.Sessions(ctx, "anuj", a.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"s1"}, ids)
}

func TestAddRepo_ByPath_DerivesAndForgetsThePath(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, checkout := remoteAndCheckout(t, "loom")

	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)

	repo, err := m.AddRepo(ctx, "anuj", ws.ID, checkout, "")
	require.NoError(t, err)
	assert.Equal(t, "loom", repo.Name)
	assert.Equal(t, url, repo.URL, "the URL is the truth")
	assert.Equal(t, "develop", repo.Ref, "the branch the checkout was on")
	assert.Equal(t, filepath.Join(ws.RootPath, "repos", "loom"), repo.WorktreePath)

	assert.FileExists(t, filepath.Join(repo.WorktreePath, "README.md"))
	assert.True(t, m.Pool.HasClone("loom"), "cloned once into the pool")

	// The user's checkout was read, not kept: deleting it changes nothing.
	require.NoError(t, os.RemoveAll(checkout))
	st := m.git.Worktree(ctx, repo.WorktreePath)
	assert.True(t, st.Attached, "the worktree hangs off tera's clone, not the user's")
	assert.Equal(t, "develop", st.Branch)
}

func TestAddRepo_ByURL_SharesThePoolClone(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, _ := remoteAndCheckout(t, "loom")

	a, err := m.Create(ctx, "anuj", "a", KindSoftwareDelivery)
	require.NoError(t, err)
	b, err := m.Create(ctx, "anuj", "b", KindSoftwareDelivery)
	require.NoError(t, err)

	ra, err := m.AddRepo(ctx, "anuj", a.ID, url, "")
	require.NoError(t, err)
	assert.Equal(t, "develop", ra.Ref, "no ref given: the remote's default branch")

	// A second workspace on the same repo needs its own branch — one branch
	// lives in one worktree.
	_, err = m.AddRepo(ctx, "anuj", b.ID, url, "develop")
	assert.ErrorIs(t, err, ErrBranchInUse)

	// The branch exists on the remote and nobody has it checked out.
	git(t, ra.WorktreePath, "branch", "loop/TER-1")
	git(t, ra.WorktreePath, "push", "-q", "origin", "loop/TER-1")
	rb, err := m.AddRepo(ctx, "anuj", b.ID, url, "loop/TER-1")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(rb.WorktreePath, "README.md"))

	entries, _ := os.ReadDir(m.Pool.Root)
	assert.Len(t, entries, 1, "one clone in the pool, two worktrees from it")
}

func TestAddRepo_PersonalRefuses(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, _ := remoteAndCheckout(t, "loom")

	ws, err := m.EnsurePersonal(ctx, "anuj")
	require.NoError(t, err)
	_, err = m.AddRepo(ctx, "anuj", ws.ID, url, "")
	assert.ErrorIs(t, err, ErrPersonalNoRepos)
}

// The open check, one row of the table at a time.
func TestCheck_RepairsMirrors_ReportsState(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, _ := remoteAndCheckout(t, "loom")

	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)
	repo, err := m.AddRepo(ctx, "anuj", ws.ID, url, "")
	require.NoError(t, err)

	only := func(findings []Finding, level Level) []Finding {
		var out []Finding
		for _, f := range findings {
			if f.Level == level {
				out = append(out, f)
			}
		}
		return out
	}

	t.Run("clean workspace is all ok", func(t *testing.T) {
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		assert.Empty(t, only(findings, LevelReport))
		assert.Empty(t, only(findings, LevelRepaired))
	})

	t.Run("pool clone deleted → re-cloned", func(t *testing.T) {
		require.NoError(t, os.RemoveAll(m.Pool.ClonePath("loom")))
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		repaired := only(findings, LevelRepaired)
		require.NotEmpty(t, repaired)
		assert.Contains(t, repaired[0].Message, "re-cloned")
		assert.True(t, m.Pool.HasClone("loom"))
		// The worktree was orphaned by the deletion: set aside, never removed,
		// and re-cut. The set-aside directory is then an unregistered
		// directory, reported on every open until a person deals with it.
		st := m.git.Worktree(ctx, repo.WorktreePath)
		assert.True(t, st.Attached)
		aside, _ := filepath.Glob(repo.WorktreePath + ".detached-*")
		require.Len(t, aside, 1, "the old directory was kept")
		reports := only(findings, LevelReport)
		require.Len(t, reports, 1)
		assert.Contains(t, reports[0].Message, "not registered")
		require.NoError(t, os.RemoveAll(aside[0])) // the person reviewed it
	})

	t.Run("worktree deleted by hand → re-cut", func(t *testing.T) {
		require.NoError(t, os.RemoveAll(repo.WorktreePath))
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		repaired := only(findings, LevelRepaired)
		require.Len(t, repaired, 1)
		assert.Contains(t, repaired[0].Message, "re-cut")
		assert.FileExists(t, filepath.Join(repo.WorktreePath, "README.md"))
	})

	t.Run("uncommitted changes → reported, untouched", func(t *testing.T) {
		mine := filepath.Join(repo.WorktreePath, "mine.txt")
		require.NoError(t, os.WriteFile(mine, []byte("work in progress\n"), 0o644))
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		reports := only(findings, LevelReport)
		require.Len(t, reports, 1)
		assert.Contains(t, reports[0].Message, "uncommitted")
		assert.FileExists(t, mine, "never deleted")
		require.NoError(t, os.Remove(mine))
	})

	t.Run("wrong branch → reported", func(t *testing.T) {
		git(t, repo.WorktreePath, "checkout", "-q", "-b", "elsewhere")
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		reports := only(findings, LevelReport)
		require.Len(t, reports, 1)
		assert.Contains(t, reports[0].Message, "expected develop")
		git(t, repo.WorktreePath, "checkout", "-q", "develop")
		git(t, repo.WorktreePath, "branch", "-q", "-D", "elsewhere")
	})

	t.Run("unregistered directory under repos/ → reported", func(t *testing.T) {
		stray := filepath.Join(ws.RootPath, DirRepos, "stray")
		require.NoError(t, os.MkdirAll(stray, 0o755))
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		reports := only(findings, LevelReport)
		require.Len(t, reports, 1)
		assert.Contains(t, reports[0].Message, "not registered")
		assert.DirExists(t, stray)
		require.NoError(t, os.RemoveAll(stray))
	})

	t.Run("ref gone on the remote → reported", func(t *testing.T) {
		git(t, repo.WorktreePath, "push", "-q", "origin", "--delete", "develop")
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
		require.NoError(t, err)
		reports := only(findings, LevelReport)
		require.Len(t, reports, 1)
		assert.Contains(t, reports[0].Message, "no longer exists on the remote")
		git(t, repo.WorktreePath, "push", "-q", "origin", "develop") // put it back
	})

	t.Run("root directory gone → reported, nothing else attempted", func(t *testing.T) {
		require.NoError(t, os.Rename(ws.RootPath, ws.RootPath+".moved"))
		_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{Offline: true})
		require.NoError(t, err)
		require.Len(t, findings, 1)
		assert.Equal(t, LevelReport, findings[0].Level)
		assert.Contains(t, findings[0].Message, "moved or deleted")
		require.NoError(t, os.Rename(ws.RootPath+".moved", ws.RootPath))
	})
}

func TestRemoveRepo_RefusesDirty(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, _ := remoteAndCheckout(t, "loom")

	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)
	repo, err := m.AddRepo(ctx, "anuj", ws.ID, url, "")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(repo.WorktreePath, "wip.txt"), []byte("x"), 0o644))
	assert.Error(t, m.RemoveRepo(ctx, "anuj", ws.ID, "loom"), "uncommitted work is never deleted")
	require.NoError(t, os.Remove(filepath.Join(repo.WorktreePath, "wip.txt")))

	require.NoError(t, m.RemoveRepo(ctx, "anuj", ws.ID, "loom"))
	assert.NoDirExists(t, repo.WorktreePath)
	repos, _ := store.Repos(ctx, "anuj", ws.ID)
	assert.Empty(t, repos)
}

func TestNames(t *testing.T) {
	assert.Equal(t, "loom", NameFromURL("git@github.com:mrg-ray/loom.git"))
	assert.Equal(t, "up-tera", NameFromURL("https://github.com/Teradata-PE/up-tera"))
	assert.Equal(t, "loom", NameFromURL("file:///tmp/x/loom.git/"))
	assert.Equal(t, "loom", NameFromURL(`file://C:\Users\me\remotes\loom.git`), "a Windows path names the repo, not the drive")
	assert.Equal(t, "loom", NameFromURL(`C:\src\loom`))
	assert.Equal(t, "git@github.com:Teradata-PE/loom.git", NormalizeURL("Teradata-PE/loom"))
	assert.Equal(t, "https://x/y.git", NormalizeURL("https://x/y.git"))
}

// A loop's checkout makes a plain clone beside the pool's bare ones and cuts
// the workspace's worktree from it, without registering anything. Opening the
// workspace registers it — its name, remote and branch read off the checkout
// — as a repair, which is silent: nothing about it needs a person. The plain
// clone is tera's clone from then on.
func TestCheck_AdoptsACheckoutALoopMadeWithoutAskingAnyone(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	url, _ := remoteAndCheckout(t, "loom")

	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)

	// What the loop did: a plain clone at <pool>/loom, a worktree from it.
	plain := filepath.Join(m.Pool.Root, "loom")
	require.NoError(t, os.MkdirAll(m.Pool.Root, 0o755))
	git(t, m.Pool.Root, "clone", "-q", url, plain)
	wt := filepath.Join(ws.RootPath, DirRepos, "loom")
	branch := strings.TrimSpace(git(t, plain, "symbolic-ref", "--short", "HEAD"))
	git(t, plain, "checkout", "-q", "--detach")
	git(t, plain, "worktree", "add", "-q", wt, branch)

	_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{})
	require.NoError(t, err)
	for _, f := range findings {
		assert.NotEqual(t, LevelReport, f.Level, "nothing here is for a person: %+v", f)
	}
	repos, err := store.Repos(ctx, "anuj", ws.ID)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "loom", repos[0].Name)
	assert.Equal(t, branch, repos[0].Ref)
	assert.Equal(t, wt, repos[0].WorktreePath)

	assert.True(t, m.Pool.HasClone("loom"), "the plain clone is tera's clone")
	assert.Equal(t, plain, m.Pool.ClonePath("loom"))
	_, err = os.Stat(filepath.Join(m.Pool.Root, "loom.git"))
	assert.True(t, os.IsNotExist(err), "no second clone was made")

	// The next open is quiet.
	_, findings, err = m.Open(ctx, "anuj", ws.ID, CheckOptions{})
	require.NoError(t, err)
	for _, f := range findings {
		assert.Equal(t, LevelOK, f.Level, "%+v", f)
	}
}

// The check says how far it has got, repo by repo, until every repo is done;
// and a remote that does not answer in time is reported, never waited on.
func TestCheck_ReportsProgressAndDoesNotWaitOnASlowRemote(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)
	for _, name := range []string{"loom", "cloud", "ui"} {
		url, _ := remoteAndCheckout(t, name)
		_, err := m.AddRepo(ctx, "anuj", ws.ID, url, "")
		require.NoError(t, err)
	}

	var mu sync.Mutex
	var heard []CheckProgress
	_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{Progress: func(p CheckProgress) {
		mu.Lock()
		heard = append(heard, p)
		mu.Unlock()
	}})
	require.NoError(t, err)
	require.NotEmpty(t, heard)
	last := heard[len(heard)-1]
	require.Equal(t, 3, last.Total)
	require.Equal(t, 3, last.Done, "the check never said every repo was done")
	for _, f := range findings {
		require.Equal(t, LevelOK, f.Level, "a clean workspace reported %+v", f)
	}

	// A remote given no time to answer is reported as not answering.
	_, findings, err = m.Open(ctx, "anuj", ws.ID, CheckOptions{RemoteTimeout: time.Nanosecond})
	require.NoError(t, err)
	slow := 0
	for _, f := range findings {
		if f.Level == LevelReport && strings.Contains(f.Message, "did not answer") {
			slow++
		}
	}
	require.Equal(t, 3, slow, "a remote out of time is not reported as such: %+v", findings)
}

// A workspace whose directory is gone blocks work until a person says where it
// went; a moved branch is only reported.
func TestCheck_SaysWhatBlocksWork(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	ws, err := m.Create(ctx, "anuj", "gone", KindSoftwareDelivery)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(ws.RootPath))
	_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{Offline: true})
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.True(t, findings[0].Blocks, "a missing workspace does not block: %+v", findings[0])
}

// A missing pool clone is re-cloned, and says so; one that cannot come back
// within its time blocks that repo and no more.
func TestCheck_ReCloningIsReportedAndBounded(t *testing.T) {
	_, store := openStores(t)
	m := newManager(t, store)
	ctx := context.Background()
	ws, err := m.Create(ctx, "anuj", "tera", KindSoftwareDelivery)
	require.NoError(t, err)
	url, _ := remoteAndCheckout(t, "loom")
	repo, err := m.AddRepo(ctx, "anuj", ws.ID, url, "")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(m.Pool.ClonePath(repo.Name)))

	var doings []string
	_, findings, err := m.Open(ctx, "anuj", ws.ID, CheckOptions{Offline: true, CloneTimeout: time.Nanosecond,
		Progress: func(p CheckProgress) { doings = append(doings, p.Doing) }})
	require.NoError(t, err)
	require.Contains(t, doings, "re-cloning")
	blocked := false
	for _, f := range findings {
		blocked = blocked || (f.Blocks && strings.Contains(f.Message, "did not finish"))
	}
	require.True(t, blocked, "a clone out of time does not block its repo: %+v", findings)
}
