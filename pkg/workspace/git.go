// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git runs the user's git. Credentials are never tera's: locally the process is
// the user's, so their ssh agent and credential helpers apply unchanged.
type Git struct {
	Bin string
}

func (g Git) bin() string {
	if g.Bin == "" {
		return "git"
	}
	return g.Bin
}

func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- the binary is this package's git and args are git verbs with workspace-row paths; no user text is spliced in
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), &gitError{args: args, stderr: strings.TrimSpace(errb.String()), err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	if e.stderr != "" {
		return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), e.stderr)
	}
	return fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
}

func (e *gitError) Unwrap() error { return e.err }

// ErrBranchInUse: git allows a branch in exactly one worktree. Two workspaces
// wanting the same branch of the same repo is a real conflict, reported as such
// rather than worked around.
var ErrBranchInUse = errors.New("workspace: branch is checked out in another worktree")

// ErrNotARepo is returned when a path is given that git does not recognise.
var ErrNotARepo = errors.New("workspace: not a git repository")

// IsRepo reports whether path is inside a git working tree.
func (g Git) IsRepo(ctx context.Context, path string) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return false
	}
	_, err := g.run(ctx, path, "rev-parse", "--git-dir")
	return err == nil
}

// RemoteURL is what a user-given path is read for. The path itself is not kept.
func (g Git) RemoteURL(ctx context.Context, path string) (string, error) {
	url, err := g.run(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("%s has no origin remote: %w", path, err)
	}
	return url, nil
}

// CurrentBranch is the ref a user-given path is on. A detached HEAD has no
// branch to derive, and the caller must name one.
func (g Git) CurrentBranch(ctx context.Context, path string) (string, error) {
	b, err := g.run(ctx, path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%s is not on a branch; pass a ref", path)
	}
	return b, nil
}

// WorktreeState is what the open check reads off a worktree directory.
type WorktreeState struct {
	Exists   bool
	Attached bool   // its .git file points at a gitdir that still exists
	Branch   string // "" when detached or unreadable
	Dirty    bool
}

// Worktree inspects a worktree directory without changing it.
func (g Git) Worktree(ctx context.Context, path string) WorktreeState {
	var st WorktreeState
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return st
	}
	st.Exists = true

	// A worktree's .git is a file: "gitdir: <clone>/worktrees/<name>". If that
	// target is gone, the worktree has been orphaned by a deleted clone.
	raw, err := os.ReadFile(filepath.Join(path, ".git")) // #nosec G304 -- path is a worktree this pool laid out; the read is its own .git pointer
	if err != nil {
		return st
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(string(raw), "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(path, gitdir)
	}
	if _, err := os.Stat(gitdir); err != nil { // #nosec G703 -- gitdir is the pointer the worktree's own .git file names
		return st
	}
	st.Attached = true

	if b, err := g.run(ctx, path, "symbolic-ref", "--short", "HEAD"); err == nil {
		st.Branch = b
	}
	if out, err := g.run(ctx, path, "status", "--porcelain"); err == nil {
		st.Dirty = out != ""
	}
	return st
}

// ---------------------------------------------------------------------------

// Pool is where tera keeps its own clones: one per repo, ever. Every
// workspace's worktree is cut from here, so deleting a user's checkout — or a
// workspace — never orphans another workspace's code.
//
// Clones are bare with the fetch refspec set, so any branch can be a worktree
// and no branch is pinned to the clone itself.
type Pool struct {
	Root string
	git  Git
}

func NewPool(root string, git Git) *Pool {
	return &Pool{Root: root, git: git}
}

// ClonePath is where tera's one clone of a repo lives. The pool makes bare
// clones at <name>.git; a plain clone at <name> — what a loop's checkout
// makes — is the same repo to tera and is used where it exists.
func (p *Pool) ClonePath(name string) string {
	bare := filepath.Join(p.Root, name+".git")
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err == nil {
		return bare
	}
	plain := filepath.Join(p.Root, name)
	if _, err := os.Stat(filepath.Join(plain, ".git")); err == nil {
		return plain
	}
	return bare
}

// HasClone reports whether the pool clone exists and looks like a repository.
func (p *Pool) HasClone(name string) bool {
	path := p.ClonePath(name)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// EnsureClone clones url into the pool if it is not there. Returns whether it
// had to.
func (p *Pool) EnsureClone(ctx context.Context, name, url string) (bool, error) {
	if p.HasClone(name) {
		return false, nil
	}
	if err := os.MkdirAll(p.Root, 0o750); err != nil {
		return false, err
	}
	dest := p.ClonePath(name)
	if _, err := p.git.run(ctx, "", "clone", "--bare", url, dest); err != nil {
		_ = os.RemoveAll(dest)
		return false, fmt.Errorf("clone %s: %w", url, err)
	}
	// A bare clone has no fetch refspec, so origin/<branch> would never exist
	// and no branch could be tracked. Give it the one a normal clone has.
	if _, err := p.git.run(ctx, dest, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return false, err
	}
	if _, err := p.git.run(ctx, dest, "fetch", "--quiet", "origin"); err != nil {
		return false, fmt.Errorf("fetch %s: %w", url, err)
	}
	return true, nil
}

// DefaultBranch is what the remote's HEAD points at, for a URL given with no ref.
func (p *Pool) DefaultBranch(ctx context.Context, name string) (string, error) {
	b, err := p.git.run(ctx, p.ClonePath(name), "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return b, nil
}

// Fetch refreshes the pool clone from its remote.
func (p *Pool) Fetch(ctx context.Context, name string) error {
	_, err := p.git.run(ctx, p.ClonePath(name), "fetch", "--quiet", "--prune", "origin")
	return err
}

// RefExistsRemote asks the remote whether a branch still exists. The one network
// call the open check makes; skipped offline.
func (p *Pool) RefExistsRemote(ctx context.Context, name, ref string) (bool, error) {
	// #nosec G204 -- the binary is this package's git; ref is the branch a workspace row names
	cmd := exec.CommandContext(ctx, p.git.bin(), "ls-remote", "--exit-code", "--heads", "origin", ref)
	cmd.Dir = p.ClonePath(name)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return false, nil // the remote answered: no such branch
	}
	return false, fmt.Errorf("remote unreachable: %s", strings.TrimSpace(errb.String()))
}

// AddWorktree cuts a worktree for ref at path, tracking origin/ref. If the
// branch exists only locally in the clone, it is checked out as-is.
func (p *Pool) AddWorktree(ctx context.Context, name, path, ref string) error {
	clone := p.ClonePath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	_ = p.Fetch(ctx, name) // best effort; the worktree can be cut from what is already there
	_, err := p.git.run(ctx, clone, "worktree", "add", "--track", "-B", ref, path, "origin/"+ref)
	if err == nil {
		return nil
	}
	if isBranchInUse(err) {
		return ErrBranchInUse
	}
	// No origin/<ref>: a branch that exists only in this clone.
	if _, err2 := p.git.run(ctx, clone, "worktree", "add", path, ref); err2 != nil {
		if isBranchInUse(err2) {
			return ErrBranchInUse
		}
		return fmt.Errorf("worktree for %s@%s: %w", name, ref, err)
	}
	return nil
}

// RemoveWorktree removes a clean worktree. A dirty one is refused: that is a
// human's uncommitted work, and nothing in tera deletes that.
func (p *Pool) RemoveWorktree(ctx context.Context, name, path string) error {
	_, err := p.git.run(ctx, p.ClonePath(name), "worktree", "remove", path)
	return err
}

// PruneWorktrees forgets worktrees whose directories are gone, so a re-cut at
// the same path is not refused as a duplicate.
func (p *Pool) PruneWorktrees(ctx context.Context, name string) error {
	_, err := p.git.run(ctx, p.ClonePath(name), "worktree", "prune")
	return err
}

func isBranchInUse(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already checked out") || strings.Contains(msg, "already used by worktree")
}

// ---------------------------------------------------------------------------

// NormalizeURL turns what a user typed into a clone URL. A full URL passes
// through; "org/repo" becomes the GitHub SSH form, which is how every repo in
// the delivery manifests is already written.
func NormalizeURL(input string) string {
	s := strings.TrimSpace(input)
	if strings.Contains(s, "://") || strings.Contains(s, "@") {
		return s
	}
	if parts := strings.Split(strings.Trim(s, "/"), "/"); len(parts) == 2 && !strings.HasPrefix(s, ".") {
		return "git@github.com:" + parts[0] + "/" + parts[1] + ".git"
	}
	return s
}

// NameFromURL is the repo's short name: the last path segment, without .git.
// It is how specs and prompts refer to the repo, so it has to be stable and
// human — "loom", never a URL.
func NameFromURL(url string) string {
	s := strings.TrimRight(strings.TrimSpace(url), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
