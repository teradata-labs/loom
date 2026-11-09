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

package shellpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

// maxDirs bounds how many working directories the analyzer follows for one
// command. Past it, static enforcement refuses the command.
const maxDirs = 64

// maxCdTargets bounds how many literal cd and pushd targets the analyzer
// records for one command. Following them takes time that grows with their
// number (a 40,000-character chain of "cd ..;" took seconds), and the hook
// runs before shell_execute's own command-length limit, so past it the
// command is refused in both modes.
const maxCdTargets = 64

// pathCheck is a path the command reads or writes, judged once every
// directory the command can change to is known.
type pathCheck struct {
	path  string
	write bool
	n     syntax.Node
	outer *syntax.Pos
}

// cdTarget is a literal directory a cd or pushd changes to.
type cdTarget struct {
	path  string
	n     syntax.Node
	outer *syntax.Pos
}

// dirOperand is an operand of a program whose profile refuses directory
// operands (Profile.NoDirOperands), judged once every directory the command
// can change to is known. glob marks a glob pattern (static only), whose
// matches are judged.
type dirOperand struct {
	path  string
	glob  bool
	prog  string
	n     syntax.Node
	outer *syntax.Pos
}

// dirOperand records an operand of prog that must not be a directory.
func (a *analyzer) dirOperand(path string, glob bool, prog string, n syntax.Node, outer *syntax.Pos) {
	if path == "" || devicePaths[path] || strings.HasPrefix(path, "~") {
		return
	}
	a.dirOps = append(a.dirOps, dirOperand{path: path, glob: glob, prog: prog, n: n, outer: outer})
}

// repoCheck is a call whose profile judges the directory it runs in.
type repoCheck struct {
	check func(dir string, args []string) string
	args  []string
	prog  string
	n     syntax.Node
	outer *syntax.Pos
}

// finishRepoChecks judges each recorded call from every directory the
// command may be in: the first violation is an Ask granting the program.
func (a *analyzer) finishRepoChecks(dirs []workDir) {
	for _, c := range a.repoChecks {
		for _, d := range dirs {
			if v := c.check(d.physical, c.args); v != "" {
				a.ask(c.n, c.outer, c.prog, v, "program", c.prog)
				break
			}
		}
	}
}

// workDir is one directory the command may be in: the logical path bash's
// cd -L computes, and the directory it really is.
type workDir struct {
	logical, physical string
}

// readPath records a path the command reads.
func (a *analyzer) readPath(path string, n syntax.Node, outer *syntax.Pos) {
	if path == "" || devicePaths[path] || strings.HasPrefix(path, "~") {
		return
	}
	if netDevice(path) {
		a.add(Deny, n, outer, path, "opens a network connection")
		return
	}
	a.paths = append(a.paths, pathCheck{path: path, n: n, outer: outer})
}

// writePath records a path the command writes.
func (a *analyzer) writePath(path string, n syntax.Node, outer *syntax.Pos) {
	if devicePaths[path] {
		return
	}
	if netDevice(path) {
		a.add(Deny, n, outer, path, "opens a network connection")
		return
	}
	a.paths = append(a.paths, pathCheck{path: path, write: true, n: n, outer: outer})
}

// chdir judges cd and pushd. Under static enforcement a target the analyzer
// cannot follow is refused: none ($HOME), "-" ($OLDPWD), a stack rotation
// (pushd +N/-N, or pushd with no directory) or a computed word. Every literal
// target is recorded, so relative paths are judged from every directory the
// command can reach, whatever order the cd commands run in.
func (a *analyzer) chdir(x *syntax.CallExpr, name string, rest []*syntax.Word, outer *syntax.Pos) {
	targets := 0
	afterDashes := false
	for _, w := range rest {
		s, ok := literal(w)
		if !ok {
			if a.opt.Static {
				a.add(Deny, w, outer, wordText(w), "a path computed at run time cannot be checked")
			}
			return
		}
		switch {
		case !afterDashes && s == "--":
			afterDashes = true
			continue
		case !afterDashes && s == "-":
			if a.opt.Static {
				a.add(Deny, x, outer, name+" -", "returns to $OLDPWD, which static enforcement cannot see")
			}
			return
		case !afterDashes && name == "pushd" && len(s) > 1 && (s[0] == '+' || s[0] == '-') && isDigits(s[1:]):
			if a.opt.Static {
				a.add(Deny, x, outer, name+" "+s, "rotates the directory stack, which static enforcement does not follow")
			}
			return
		case !afterDashes && strings.HasPrefix(s, "-"):
			continue // -L, -P, -e, -@, pushd -n
		}
		targets++
		if len(a.cds) >= maxCdTargets {
			if !a.tooManyCds {
				a.tooManyCds = true
				a.add(Deny, w, outer, name, "the command has more than "+itoa(maxCdTargets)+" cd or pushd targets, more than the policy follows")
			}
			continue
		}
		a.cds = append(a.cds, cdTarget{path: s, n: w, outer: outer})
	}
	if targets == 0 && a.opt.Static {
		why := "with no directory goes to $HOME, which static enforcement cannot see"
		if name == "pushd" {
			why = "with no directory swaps the directory stack, which static enforcement does not follow"
		}
		a.add(Deny, x, outer, name, why)
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// workingDirs returns every directory the command may be in: the working
// directory and, repeatedly, every directory a recorded cd target reaches
// from one already found. A cd target outside the read roots is an Ask.
// Each directory is followed through each target once, so the work is at
// most maxDirs times maxCdTargets candidate checks.
func (a *analyzer) workingDirs() []workDir {
	start := workDir{logical: filepath.Clean(a.opt.WorkingDir), physical: ResolvePath("/", a.opt.WorkingDir)}
	dirs := []workDir{start}
	seen := map[workDir]bool{start: true}
	asked := make(map[int]bool, len(a.cds))
	overflow := false
	for i := 0; i < len(dirs); i++ {
		d := dirs[i]
		for ti, t := range a.cds {
			for _, c := range cdCandidates(d, t.path) {
				if fi, err := os.Stat(c.physical); err != nil || !fi.IsDir() {
					continue // cd fails and the directory stays
				}
				if !inRoots(c.physical, a.opt.Roots.Read) {
					if !asked[ti] {
						a.add(Ask, t.n, t.outer, t.path, "reads outside the read roots")
						asked[ti] = true
					}
					a.grant.add("read", c.physical)
				}
				if seen[c] {
					continue
				}
				if len(dirs) >= maxDirs {
					overflow = true
					continue
				}
				seen[c] = true
				dirs = append(dirs, c)
			}
		}
	}
	if overflow && a.opt.Static {
		a.add(Deny, nil, nil, "cd", "the command can change to more directories than static enforcement follows")
	}
	return dirs
}

// cdCandidates returns the directories cd target can reach from d: bash's
// logical result (cd -L), the physical one (cd -P, and -L when the logical
// one fails), and the same from each CDPATH entry for a target bash looks up
// in CDPATH.
func cdCandidates(d workDir, target string) []workDir {
	if filepath.IsAbs(target) {
		logical := filepath.Clean(target)
		return []workDir{{logical, ResolvePath("/", logical)}, {ResolvePath("/", target), ResolvePath("/", target)}}
	}
	bases := []workDir{d}
	if cdpathSearched(target) {
		for _, e := range filepath.SplitList(os.Getenv("CDPATH")) {
			switch {
			case e == "":
				// The current directory, already in bases.
			case filepath.IsAbs(e):
				bases = append(bases, workDir{filepath.Clean(e), ResolvePath("/", e)})
			default:
				bases = append(bases, workDir{filepath.Join(d.logical, e), ResolvePath(d.physical, e)})
			}
		}
	}
	out := make([]workDir, 0, 2*len(bases))
	for _, b := range bases {
		logical := filepath.Join(b.logical, target)
		physical := ResolvePath(b.physical, target)
		out = append(out, workDir{logical, ResolvePath("/", logical)}, workDir{physical, physical})
	}
	return out
}

// cdpathSearched reports whether bash looks target up in CDPATH: it does
// unless the target is absolute or starts with a "." or ".." component.
func cdpathSearched(target string) bool {
	return !strings.HasPrefix(target, "/") && target != "." && target != ".." &&
		!strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../")
}

// finishPaths judges every recorded path from every directory the command
// may be in. A read of an existing path outside the read roots is an Ask
// granting each resolved path; a write outside the write roots is a Deny.
func (a *analyzer) finishPaths() {
	if len(a.paths) == 0 && len(a.cds) == 0 && len(a.globs) == 0 && len(a.dirOps) == 0 && len(a.repoChecks) == 0 {
		return
	}
	dirs := a.workingDirs()
	a.finishGlobs(dirs)
	a.finishDirOperands(dirs)
	a.finishRepoChecks(dirs)
	for _, c := range a.paths {
		bases := dirs
		if filepath.IsAbs(c.path) {
			bases = dirs[:1]
		}
		var outside, gitWrites []string
		for _, d := range bases {
			full := ResolvePath(d.physical, c.path)
			if c.write {
				full = resolveWritePath(d.physical, c.path)
				if !inRoots(full, a.opt.Roots.Write) {
					a.add(Deny, c.n, c.outer, c.path, "writes outside the write roots")
					gitWrites = nil
					break
				}
				if inGitDir(full) {
					gitWrites = append(gitWrites, full)
				}
				continue
			}
			if _, err := os.Stat(full); err != nil {
				continue // only an existing path can be read
			}
			if !inRoots(full, a.opt.Roots.Read) {
				outside = append(outside, full)
			}
		}
		if len(outside) > 0 {
			a.add(Ask, c.n, c.outer, c.path, "reads outside the read roots")
			for _, full := range outside {
				a.grant.add("read", full)
			}
		}
		if len(gitWrites) > 0 {
			a.add(Ask, c.n, c.outer, c.path, gitWriteWhy)
			for _, full := range gitWrites {
				a.grant.add("write", full)
			}
		}
	}
}

// maxSymlinkHops bounds how many final symlinks resolveWritePath follows,
// as the kernel's limit does; past it the open fails.
const maxSymlinkHops = 40

// resolveWritePath is ResolvePath for a path the command opens for writing.
// ResolvePath leaves a final symlink whose target does not exist yet as the
// symlink's own path, but > and >> follow it and create the target, so the
// target is what the write reaches: each final symlink is followed, its
// target resolved like the rest (relative to the symlink's directory, with
// a ".." after a symlink leaving that symlink's target).
func resolveWritePath(dir, path string) string {
	full := ResolvePath(dir, path)
	for i := 0; i < maxSymlinkHops; i++ {
		fi, err := os.Lstat(full)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return full
		}
		target, err := os.Readlink(full)
		if err != nil {
			return full
		}
		if !filepath.IsAbs(target) {
			target = filepath.Dir(full) + string(filepath.Separator) + target
		}
		full = ResolvePath("/", target)
	}
	return full
}

// gitWriteWhy says why a write into a git directory needs approval.
const gitWriteWhy = "writes into a git directory, whose config and attributes can make git run programs"

// inGitDir reports whether path (resolved) lies in a git directory, or is
// one: a component named .git (a repository's directory, or the .git file
// of a linked worktree), or an existing ancestor that git takes for a
// repository (a HEAD file, and objects and refs directories), such as a
// bare repository. A write there can define a filter in config and name it
// in info/attributes or core.attributesFile, which gitHarden's
// --attr-source does not replace, so git diff and git status run it.
func inGitDir(path string) bool {
	for p := filepath.Clean(path); ; {
		if filepath.Base(p) == ".git" || isGitDir(p) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// isGitDir reports whether dir looks like a git directory to git: it holds
// a HEAD file and objects and refs directories.
func isGitDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || fi.IsDir() {
		return false
	}
	return isDir(filepath.Join(dir, "objects")) && isDir(filepath.Join(dir, "refs"))
}

// maxGlobMatches bounds how many names one glob may match under static
// enforcement before the command is refused.
const maxGlobMatches = 4096

// errTooManyMatches is returned by globExpand past maxGlobMatches.
var errTooManyMatches = errors.New("matches too many names")

// globCheck is a glob argument expanded and path-checked under static
// enforcement, where no runner checks the names it matches.
type globCheck struct {
	pat   string // the word as written: unquoted literal text with its escapes
	n     syntax.Node
	outer *syntax.Pos
}

// staticGlob records a glob argument for finishPaths to expand. A ".." after
// a glob component is refused: what it climbs out of depends on what the
// glob matches when the command runs.
func (a *analyzer) staticGlob(w *syntax.Word, outer *syntax.Pos) {
	pat := globPattern(w)
	globbed := false
	for _, comp := range strings.Split(pat, "/") {
		if globbed && unescapeUnquoted(comp) == ".." {
			a.add(Deny, w, outer, wordText(w), "a .. after a glob climbs out of whatever the glob matches, which static enforcement cannot check")
			return
		}
		if pattern.HasMeta(comp, 0) {
			globbed = true
		}
	}
	a.globs = append(a.globs, globCheck{pat: pat, n: w, outer: outer})
}

// globPattern returns a glob word as written: its unquoted literal text with
// its escapes. globDir accepted the word, so every part is a Lit.
func globPattern(w *syntax.Word) string {
	var raw strings.Builder
	for _, p := range w.Parts {
		raw.WriteString(p.(*syntax.Lit).Value)
	}
	return raw.String()
}

// finishDirOperands judges each recorded directory operand from every
// directory the command may be in: an existing directory, or a glob match
// that is one, is an Ask granting the program. A glob that cannot be
// expanded is refused by finishGlobs.
func (a *analyzer) finishDirOperands(dirs []workDir) {
	for _, o := range a.dirOps {
		bases := dirs
		if filepath.IsAbs(o.path) {
			bases = dirs[:1]
		}
		found := ""
		for _, d := range bases {
			names := []string{o.path}
			if o.glob {
				m, err := globExpand(d.physical, o.path, maxGlobMatches)
				if err != nil {
					continue
				}
				names = m
			}
			for _, nm := range names {
				if isDir(ResolvePath(d.physical, nm)) {
					found = nm
					break
				}
			}
			if found != "" {
				break
			}
		}
		if found != "" {
			a.ask(o.n, o.outer, found, dirOperandWhy(o.prog), "program", o.prog)
		}
	}
}

// dirOperandWhy says why a directory operand of prog needs approval.
func dirOperandWhy(prog string) string {
	return prog + " compares the files in a directory, and follows the symlinks in it out of the read roots"
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// globExpand returns the names pat matches from dir, as relative or absolute
// paths built from the pattern's own components. It matches more than bash
// does, never less: wildcards match a leading dot, and a component that
// starts with "." also matches "." and ".." (bash before 5.2 does that).
func globExpand(dir, pat string, limit int) ([]string, error) {
	cur := []string{""}
	if strings.HasPrefix(pat, "/") {
		cur = []string{"/"}
	}
	for _, comp := range strings.Split(pat, "/") {
		if comp == "" {
			continue
		}
		if !pattern.HasMeta(comp, 0) {
			lit := unescapeUnquoted(comp)
			for i := range cur {
				cur[i] = joinGlob(cur[i], lit)
			}
			continue
		}
		expr, err := pattern.Regexp(comp, pattern.Filenames|pattern.EntireString|pattern.GlobLeadingDot)
		if err != nil {
			return nil, err
		}
		rx, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		var next []string
		for _, c := range cur {
			names := []string{}
			if strings.HasPrefix(comp, ".") {
				names = append(names, ".", "..")
			}
			from := c
			if from == "" {
				from = "."
			}
			entries, _ := os.ReadDir(ResolvePath(dir, from)) // an unreadable directory matches nothing
			for _, e := range entries {
				names = append(names, e.Name())
			}
			for _, name := range names {
				if rx.MatchString(name) {
					next = append(next, joinGlob(c, name))
					if len(next) > limit {
						return nil, errTooManyMatches
					}
				}
			}
		}
		cur = next
	}
	return cur, nil
}

func joinGlob(dir, name string) string {
	switch dir {
	case "":
		return name
	case "/":
		return "/" + name
	}
	return dir + "/" + name
}

// finishGlobs expands each recorded glob from every directory the command
// may be in and judges each name it matches like a literal path.
func (a *analyzer) finishGlobs(dirs []workDir) {
	for _, g := range a.globs {
		var outside []string
		for _, d := range dirs {
			matches, err := globExpand(d.physical, g.pat, maxGlobMatches)
			if err != nil {
				a.add(Deny, g.n, g.outer, g.pat, "a glob that "+err.Error()+" cannot be checked")
				outside = nil
				break
			}
			for _, m := range matches {
				full := ResolvePath(d.physical, m)
				if _, err := os.Stat(full); err != nil {
					continue
				}
				if !inRoots(full, a.opt.Roots.Read) {
					outside = append(outside, full)
				}
			}
		}
		if len(outside) > 0 {
			a.add(Ask, g.n, g.outer, g.pat, "matches names outside the read roots")
			for _, full := range outside {
				a.grant.add("read", full)
			}
		}
	}
}
