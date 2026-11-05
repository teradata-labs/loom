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
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// maxDirs bounds how many working directories the analyzer follows for one
// command. Past it, static enforcement refuses the command.
const maxDirs = 64

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
	a.paths = append(a.paths, pathCheck{path: path, n: n, outer: outer})
}

// writePath records a path the command writes.
func (a *analyzer) writePath(path string, n syntax.Node, outer *syntax.Pos) {
	if devicePaths[path] {
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
func (a *analyzer) workingDirs() []workDir {
	start := workDir{logical: filepath.Clean(a.opt.WorkingDir), physical: ResolvePath("/", a.opt.WorkingDir)}
	dirs := []workDir{start}
	seen := map[workDir]bool{start: true}
	asked := make(map[int]bool, len(a.cds))
	overflow := false
	for changed := true; changed; {
		changed = false
		for ti, t := range a.cds {
			for _, d := range dirs[:len(dirs):len(dirs)] {
				for _, c := range cdCandidates(d, t.path) {
					if fi, err := os.Stat(c.physical); err != nil || !fi.IsDir() {
						continue // cd fails and the directory stays
					}
					if !inRoots(c.physical, a.opt.Roots.Read) {
						if !asked[ti] {
							a.add(Ask, t.n, t.outer, t.path, "reads outside the session")
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
					changed = true
				}
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
	return !(strings.HasPrefix(target, "/") || target == "." || target == ".." ||
		strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../"))
}

// finishPaths judges every recorded path from every directory the command
// may be in. A read of an existing path outside the read roots is an Ask
// granting each resolved path; a write outside the write roots is a Deny.
func (a *analyzer) finishPaths() {
	if len(a.paths) == 0 && len(a.cds) == 0 {
		return
	}
	dirs := a.workingDirs()
	for _, c := range a.paths {
		bases := dirs
		if filepath.IsAbs(c.path) {
			bases = dirs[:1]
		}
		var outside []string
		for _, d := range bases {
			full := ResolvePath(d.physical, c.path)
			if c.write {
				if !inRoots(full, a.opt.Roots.Write) {
					a.add(Deny, c.n, c.outer, c.path, "writes outside the session")
					break
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
			a.add(Ask, c.n, c.outer, c.path, "reads outside the session")
			for _, full := range outside {
				a.grant.add("read", full)
			}
		}
	}
}
