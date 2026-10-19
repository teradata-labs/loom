// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is what the open check concluded about one thing it looked at.
type Level string

const (
	LevelOK       Level = "ok"
	LevelRepaired Level = "repaired" // a pure mirror was rebuilt; nothing was lost
	LevelReport   Level = "report"   // carries state; a person decides
)

// Finding is one line of the open check.
type Finding struct {
	Repo    string // "" for the workspace itself
	Level   Level
	Message string
	// Blocks says work cannot go on in the workspace until a person acts:
	// its directory is gone, or a repo could not be rebuilt.
	Blocks bool
}

// CheckOptions controls the one network call the check makes, and who hears
// how far the check has got.
type CheckOptions struct {
	Offline bool
	// RemoteTimeout bounds each remote lookup; zero is DefaultRemoteTimeout. A
	// remote that does not answer in time is reported unreachable, never waited
	// on.
	RemoteTimeout time.Duration
	// CloneTimeout bounds rebuilding a missing pool clone; zero is
	// DefaultCloneTimeout. A clone that does not come back in time leaves its
	// repo blocked and the rest of the workspace usable.
	CloneTimeout time.Duration
	// Progress, when set, hears each repo as the check starts and finishes it.
	Progress func(CheckProgress)
}

// CheckProgress is how far the check has got: repos finished of all, and what
// it is doing now.
type CheckProgress struct {
	Done, Total int
	Repo        string
	Doing       string
}

// DefaultRemoteTimeout is how long one remote lookup may take.
const DefaultRemoteTimeout = 10 * time.Second

// DefaultCloneTimeout is how long rebuilding one pool clone may take.
const DefaultCloneTimeout = 5 * time.Minute

// Check verifies the world before a session binds to it.
//
// The rule between repair and report: repair what is a pure mirror, report
// what carries state. A pool clone and a clean worktree can be rebuilt from the
// remote with nothing lost, so the check just does it. Anything holding a
// human's or a run's changes — dirty files, a moved branch, a diverged ref — is
// reported and left exactly as it is, because repair there is deletion.
func (m *Manager) Check(ctx context.Context, ws *Workspace, opts CheckOptions) ([]Finding, error) {
	var findings []Finding
	add := func(repo string, level Level, format string, args ...any) {
		findings = append(findings, Finding{Repo: repo, Level: level, Message: fmt.Sprintf(format, args...)})
	}

	// The root. Missing means moved or deleted; guessing a new location would
	// be worse than asking.
	if fi, err := os.Stat(ws.RootPath); err != nil || !fi.IsDir() {
		add("", LevelReport, "workspace directory missing at %s — moved or deleted; set the new path", ws.RootPath)
		findings[len(findings)-1].Blocks = true
		return findings, nil
	}
	// The six directories are structure, not state.
	for _, d := range Layout {
		p := filepath.Join(ws.RootPath, d)
		if _, err := os.Stat(p); err != nil {
			if err := os.MkdirAll(p, 0o750); err != nil {
				add("", LevelReport, "could not create %s: %v", d, err)
			} else {
				add("", LevelRepaired, "%s/ was missing — created", d)
			}
		}
	}

	repos, err := m.Store.Repos(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, r := range repos {
		registered[r.Name] = true
	}
	findings = append(findings, m.checkRepos(ctx, repos, opts)...)

	// Directories under repos/ that no row knows about. Not deleted: they may
	// be somebody's work. Not adopted: that is a decision.
	if entries, err := os.ReadDir(filepath.Join(ws.RootPath, DirRepos)); err == nil {
		for _, e := range entries {
			if !e.IsDir() || registered[e.Name()] {
				continue
			}
			// A checkout on a branch with a remote is a repo of this
			// workspace that nobody wrote down: a loop's checkout makes them.
			// Writing it down is a repair; only what cannot be read as a
			// repo is for a person.
			if repo, ok := m.adopt(ctx, ws, e.Name()); ok {
				add(e.Name(), LevelRepaired, "found on disk on %s and registered", repo.Ref)
				continue
			}
			add(e.Name(), LevelReport, "repos/%s exists on disk but is not registered — adopt it or remove it", e.Name())
		}
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Repo < findings[j].Repo })
	return findings, nil
}

// adopt registers a checkout found under repos/: its name is its directory,
// its url its origin, its ref the branch it is on.
//
// Not adopted: a copy the check itself set aside for a person to review, and
// a checkout of a remote already registered under another name — that is a
// second copy, not a repo.
func (m *Manager) adopt(ctx context.Context, ws *Workspace, name string) (*Repo, bool) {
	if strings.Contains(name, ".detached-") {
		return nil, false
	}
	path := filepath.Join(ws.RootPath, DirRepos, name)
	if !m.git.IsRepo(ctx, path) {
		return nil, false
	}
	url, err := m.git.RemoteURL(ctx, path)
	if err != nil || url == "" {
		return nil, false
	}
	if existing, err := m.Store.Repos(ctx, ws.ID); err == nil {
		for _, r := range existing {
			if NormalizeURL(r.URL) == NormalizeURL(url) {
				return nil, false
			}
		}
	}
	ref, err := m.git.CurrentBranch(ctx, path)
	if err != nil || ref == "" {
		return nil, false
	}
	repo := &Repo{WorkspaceID: ws.ID, Name: name, URL: url, Ref: ref, WorktreePath: path}
	if err := m.Store.AddRepo(ctx, repo); err != nil {
		return nil, false
	}
	return repo, true
}

// checkRepos checks every repo: what is on disk first, one repo after another,
// since repairs touch the shared pool and take no time when nothing needs
// repairing; then the remotes, all at once, since each is a wait on the
// network and none depends on another.
func (m *Manager) checkRepos(ctx context.Context, repos []*Repo, opts CheckOptions) []Finding {
	total := len(repos)
	var mu sync.Mutex
	done := 0
	tell := func(repo, doing string, finished bool) {
		if opts.Progress == nil {
			return
		}
		mu.Lock()
		if finished {
			done++
		}
		p := CheckProgress{Done: done, Total: total, Repo: repo, Doing: doing}
		mu.Unlock()
		opts.Progress(p)
	}

	results := make([][]Finding, len(repos))
	remote := make([]bool, len(repos))
	for i, r := range repos {
		tell(r.Name, "checking what is on disk", false)
		results[i], remote[i] = m.checkRepo(ctx, r, opts, func(doing string) { tell(r.Name, doing, false) })
		if !remote[i] || opts.Offline {
			tell(r.Name, "checked", true)
		}
	}
	if !opts.Offline {
		timeout := opts.RemoteTimeout
		if timeout <= 0 {
			timeout = DefaultRemoteTimeout
		}
		var wg sync.WaitGroup
		for i, r := range repos {
			if !remote[i] {
				continue
			}
			wg.Add(1)
			go func(i int, r *Repo) {
				defer wg.Done()
				tell(r.Name, "asking the remote", false)
				rctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				exists, err := m.Pool.RefExistsRemote(rctx, r.Name, r.Ref)
				switch {
				case rctx.Err() != nil:
					results[i] = append(results[i], Finding{Repo: r.Name, Level: LevelReport,
						Message: fmt.Sprintf("remote did not answer within %s — read-only until it does", timeout)})
				case err != nil:
					results[i] = append(results[i], Finding{Repo: r.Name, Level: LevelReport,
						Message: fmt.Sprintf("remote unreachable — read-only until it is: %v", err)})
				case !exists:
					results[i] = append(results[i], Finding{Repo: r.Name, Level: LevelReport,
						Message: fmt.Sprintf("%s no longer exists on the remote — pick a ref", r.Ref)})
				}
				tell(r.Name, "checked", true)
			}(i, r)
		}
		wg.Wait()
	}

	var out []Finding
	for i, r := range repos {
		if len(results[i]) == 0 {
			results[i] = []Finding{{Repo: r.Name, Level: LevelOK, Message: "on " + r.Ref}}
		}
		out = append(out, results[i]...)
	}
	return out
}

// checkRepo is what is on disk for one repo: the pool clone, the worktree, and
// the state the worktree carries. It says whether the remote is still to be
// asked, which it is for a repo whose worktree stands where it should.
func (m *Manager) checkRepo(ctx context.Context, r *Repo, opts CheckOptions, doing func(string)) ([]Finding, bool) {
	var out []Finding
	add := func(level Level, format string, args ...any) {
		out = append(out, Finding{Repo: r.Name, Level: level, Message: fmt.Sprintf(format, args...)})
	}
	// A repo that could not be rebuilt cannot be worked in.
	block := func(format string, args ...any) {
		out = append(out, Finding{Repo: r.Name, Level: LevelReport, Message: fmt.Sprintf(format, args...), Blocks: true})
	}

	// 1. The pool clone. A mirror of the remote: rebuild it.
	if !m.Pool.HasClone(r.Name) {
		doing("re-cloning")
		limit := opts.CloneTimeout
		if limit <= 0 {
			limit = DefaultCloneTimeout
		}
		cctx, cancel := context.WithTimeout(ctx, limit)
		_, err := m.Pool.EnsureClone(cctx, r.Name, r.URL)
		timedOut := cctx.Err() != nil
		cancel()
		if timedOut {
			block("pool clone missing and re-cloning did not finish within %s", limit)
			return out, false
		}
		if err != nil {
			block("pool clone missing and could not be re-cloned: %v", err)
			return out, false
		}
		add(LevelRepaired, "pool clone was missing — re-cloned from %s", r.URL)
	}

	// 2. The worktree.
	st := m.git.Worktree(ctx, r.WorktreePath)
	switch {
	case !st.Exists:
		// Deleted by hand. git still lists it, so prune before re-cutting.
		_ = m.Pool.PruneWorktrees(ctx, r.Name)
		if err := m.Pool.AddWorktree(ctx, r.Name, r.WorktreePath, r.Ref); err != nil {
			if err == ErrBranchInUse {
				block("worktree missing and %s is checked out in another workspace", r.Ref)
			} else {
				block("worktree missing and could not be re-cut: %v", err)
			}
			return out, false
		}
		add(LevelRepaired, "worktree was missing — re-cut from %s", r.Ref)
		return out, false

	case !st.Attached:
		// The clone it belonged to is gone (and has just been re-cloned above).
		// The directory may hold work, so it is set aside, never removed.
		aside := r.WorktreePath + ".detached-" + time.Now().Format("20060102-150405")
		if err := os.Rename(r.WorktreePath, aside); err != nil {
			block("worktree is detached from its clone and could not be set aside: %v", err)
			return out, false
		}
		_ = m.Pool.PruneWorktrees(ctx, r.Name)
		if err := m.Pool.AddWorktree(ctx, r.Name, r.WorktreePath, r.Ref); err != nil {
			block("worktree detached; old directory kept at %s; re-cut failed: %v", aside, err)
			return out, false
		}
		add(LevelRepaired, "worktree was detached from its clone — re-cut; old directory kept at %s", aside)
		return out, false
	}

	// 3. State the worktree carries. Reported, never touched.
	if st.Branch != r.Ref {
		add(LevelReport, "on %s, expected %s — a run may have moved it", st.Branch, r.Ref)
	}
	if st.Dirty {
		add(LevelReport, "uncommitted changes — left alone")
	}

	// 4. The remote is asked by the caller, with every other repo's at once.
	return out, true
}
