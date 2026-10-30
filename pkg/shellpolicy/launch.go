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
)

// LaunchError is a program launch or builtin the policy refuses at run time.
type LaunchError struct {
	// Name is the program or builtin, as the command named it.
	Name string
	// Reason says which rule it broke.
	Reason string
}

func (e *LaunchError) Error() string { return "blocked " + e.Name + ": " + e.Reason }

// CheckLaunch judges one program launch while the command runs: argv is the
// fully expanded argument list, dir the shell's current directory, and g what
// a person approved for this call (nil when nothing was). It returns nil when
// the launch may proceed, else a *LaunchError.
func (p *Policy) CheckLaunch(argv []string, dir string, roots Roots, g *Grant) error {
	if len(argv) == 0 {
		return &LaunchError{Name: "(empty)", Reason: "no program"}
	}
	name := argv[0]
	if p.Never[filepath.Base(name)] {
		return &LaunchError{Name: name, Reason: "never allowed by the shell policy"}
	}
	resolvedDir := ResolvePath("/", dir)
	if !inRoots(resolvedDir, roots.Read) && !g.allowsRead(resolvedDir) {
		return &LaunchError{Name: name, Reason: "runs in " + dir + ", outside the session"}
	}
	if g.allowsProgram(name) {
		return nil // approved for this call, with any arguments
	}
	if strings.ContainsRune(name, '/') {
		return &LaunchError{Name: name, Reason: "a program named by path runs only when approved"}
	}
	prof, ok := p.Programs[name]
	if !ok {
		return &LaunchError{Name: name, Reason: "not on the shell allowlist and not approved for this call"}
	}
	if v := prof.violation(argv[1:], true); v != "" {
		return &LaunchError{Name: name, Reason: v}
	}
	if prof.NoPathArgs {
		return nil
	}
	for _, c := range pathCandidates(argv[1:]) {
		if err := checkReadAt(name, c, dir, roots, g); err != nil {
			return err
		}
	}
	return nil
}

// CheckBuiltin judges a shell builtin while the command runs.
func (p *Policy) CheckBuiltin(name string, g *Grant) error {
	if !implementedBuiltins[name] {
		return &LaunchError{Name: name, Reason: "not supported by the jailed shell"}
	}
	if p.Builtins[name] || g.allowsBuiltin(name) {
		return nil
	}
	return &LaunchError{Name: name, Reason: "a shell builtin outside the policy, not approved for this call"}
}

// CheckOpen judges a file the shell itself opens (a redirect or source): path
// as written, dir the shell's current directory, write whether it is opened
// for writing.
func (p *Policy) CheckOpen(path, dir string, write bool, roots Roots, g *Grant) error {
	if devicePaths[path] {
		return nil
	}
	full := ResolvePath(dir, path)
	if write {
		if inRoots(full, roots.Write) {
			return nil
		}
		return &LaunchError{Name: path, Reason: "writes outside the session"}
	}
	if inRoots(full, roots.Read) || g.allowsRead(full) {
		return nil
	}
	return &LaunchError{Name: path, Reason: "reads outside the session"}
}

// CheckListDir judges a directory the shell lists to expand a glob.
func (p *Policy) CheckListDir(dir string, roots Roots, g *Grant) error {
	full := ResolvePath("/", dir)
	if inRoots(full, roots.Read) || g.allowsRead(full) {
		return nil
	}
	return &LaunchError{Name: dir, Reason: "lists a directory outside the session"}
}

func checkReadAt(name, path, dir string, roots Roots, g *Grant) error {
	if devicePaths[path] {
		return nil
	}
	full := ResolvePath(dir, path)
	if _, err := os.Stat(full); err != nil {
		return nil
	}
	if inRoots(full, roots.Read) || g.allowsRead(full) {
		return nil
	}
	return &LaunchError{Name: name, Reason: "reads " + path + ", outside the session"}
}
