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

// gen-notices writes NOTICE and THIRD-PARTY-NOTICES.md from the packages that
// are actually compiled into the binaries we distribute.
//
// Two obligations drive this. Every permissive license in the tree (MIT, BSD,
// ISC, Apache-2.0, MPL-2.0) requires the license text and copyright notice to
// travel with the distribution — THIRD-PARTY-NOTICES.md carries them. And
// Apache-2.0 §4(d) requires that NOTICE files found in the works we
// redistribute be reproduced in our own distribution — NOTICE carries those.
//
// Scope is the distributed set, not the module graph: `go list -deps` over
// each release binary with that binary's own build tags. Test-only and
// build-time modules never reach a user and are excluded on purpose.
//
// License text is copied VERBATIM from the dependency's own source. Nothing
// here classifies or normalizes a license — an SPDX identifier is a summary,
// and a summary is not what these licenses require us to ship.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// target is one shipped binary and the build tags the release pipeline uses
// for it. Keep in sync with .github/workflows/release.yml.
type target struct {
	pkg  string
	tags string
}

var targets = []target{
	{"./cmd/loom", "fts5"},
	{"./cmd/looms", "fts5"},
	{"./cmd/loom-standalone", "fts5,standalone"},
}

// ownModule is excluded: it is the work being distributed, not a third party.
const ownModule = "github.com/teradata-labs/loom"

// isLicenseFile reports whether a filename is a license text. Matching is by
// PREFIX, not an exact list: dependencies use names this file cannot enumerate
// (LICENSE-APACHE-2.0.txt, COPYING.LESSER, LICENCE.md), and a name we fail to
// recognize is a component whose license we would fail to ship.
func isLicenseFile(name string) bool {
	u := strings.ToUpper(name)
	for _, p := range []string{"LICENSE", "LICENCE", "COPYING"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// preferLicense picks deterministically when a directory holds several license
// files, so regenerating never produces a spurious diff.
func preferLicense(names []string) string {
	sort.Slice(names, func(i, j int) bool {
		li, lj := len(names[i]), len(names[j])
		if li != lj {
			return li < lj // "LICENSE" before "LICENSE-APACHE-2.0.txt"
		}
		return names[i] < names[j]
	})
	return names[0]
}

// component is one distributed unit of third-party code. It is usually a
// module, but a package carrying its own LICENSE is listed separately — that
// file governs it, and rolling it up to the module would attribute it wrongly.
type component struct {
	ImportPath  string // package path the license was found for
	Module      string
	Version     string
	LicensePath string
	License     string // verbatim text
	Notice      string // verbatim NOTICE text, when the module ships one
}

type pkgInfo struct {
	ImportPath string
	Standard   bool
	Module     *struct {
		Path    string
		Version string
		Dir     string
	}
}

func main() {
	outDir := flag.String("dir", ".", "repository root to write NOTICE and THIRD-PARTY-NOTICES.md into")
	check := flag.Bool("check", false, "exit non-zero if the generated files differ from what is on disk")
	flag.Parse()

	comps, err := collect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-notices:", err)
		os.Exit(1)
	}
	if len(comps) == 0 {
		fmt.Fprintln(os.Stderr, "gen-notices: no third-party components found — refusing to write empty notices")
		os.Exit(1)
	}

	files := map[string][]byte{
		"THIRD-PARTY-NOTICES.md": renderThirdParty(comps),
		"NOTICE":                 renderNotice(comps),
	}

	drift := false
	for name, want := range files {
		path := filepath.Join(*outDir, name)
		if *check {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
				fmt.Fprintf(os.Stderr, "gen-notices: %s is out of date — run `just notices`\n", name)
				drift = true
			}
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen-notices:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", path)
	}
	if drift {
		os.Exit(1)
	}
	if !*check {
		withNotice := 0
		for _, c := range comps {
			if c.Notice != "" {
				withNotice++
			}
		}
		fmt.Printf("%d third-party components; %d carry an upstream NOTICE\n", len(comps), withNotice)
	}
}

// collect resolves every third-party package linked into the release binaries
// and finds the license governing each.
func collect() ([]component, error) {
	seen := map[string]component{} // license file path → component
	unlicensed := map[string]bool{}
	for _, t := range targets {
		pkgs, err := listDeps(t)
		if err != nil {
			return nil, fmt.Errorf("listing deps of %s: %w", t.pkg, err)
		}
		for _, p := range pkgs {
			if p.Standard || p.Module == nil || p.Module.Dir == "" {
				continue // stdlib is covered by the Go toolchain notice below
			}
			if p.Module.Path == ownModule || strings.HasPrefix(p.Module.Path, ownModule+"/") {
				continue
			}
			licPath, err := findLicense(p.ImportPath, p.Module.Dir)
			if err != nil {
				return nil, fmt.Errorf("searching for license of %s: %w", p.ImportPath, err)
			}
			if licPath == "" {
				// Never skip quietly. A component we ship without its license
				// text is the exact compliance failure this tool exists to
				// prevent, and a silent skip hides it.
				unlicensed[p.Module.Path+" "+p.Module.Version] = true
				continue
			}
			if _, ok := seen[licPath]; ok {
				continue
			}
			text, err := os.ReadFile(licPath)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", licPath, err)
			}
			// Attribute to the directory the license actually governs, so a
			// package with its own LICENSE is not filed under its parent.
			govern := p.Module.Path
			if rel, err := filepath.Rel(p.Module.Dir, filepath.Dir(licPath)); err == nil && rel != "." {
				govern = p.Module.Path + "/" + filepath.ToSlash(rel)
			}
			seen[licPath] = component{
				ImportPath:  govern,
				Module:      p.Module.Path,
				Version:     p.Module.Version,
				LicensePath: licPath,
				License:     strings.TrimRight(string(text), "\n"),
				Notice:      readNotice(p.Module.Dir),
			}
		}
	}

	if len(unlicensed) > 0 {
		var names []string
		for n := range unlicensed {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("no license file found for %d distributed component(s):\n  %s\n"+
			"each must be attributed before release — add its filename to isLicenseFile, "+
			"or resolve the dependency", len(names), strings.Join(names, "\n  "))
	}

	out := make([]component, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out, nil
}

func listDeps(t target) ([]pkgInfo, error) {
	args := []string{"list", "-deps", "-json"}
	if t.tags != "" {
		args = append(args, "-tags", t.tags)
	}
	args = append(args, t.pkg)

	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var pkgs []pkgInfo
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var p pkgInfo
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// findLicense walks up from the package directory to the module root, so a
// package that ships its own LICENSE is governed by that file rather than by
// the module's.
func findLicense(importPath, moduleDir string) (string, error) {
	rel := ""
	if i := strings.Index(importPath, "/"); i >= 0 {
		rel = importPath
	}
	_ = rel

	dir := moduleDir
	// Reconstruct the package directory from the module dir when possible.
	if sub := packageSubdir(importPath, moduleDir); sub != "" {
		dir = filepath.Join(moduleDir, sub)
	}
	for {
		entries, err := os.ReadDir(dir)
		if err == nil {
			var found []string
			for _, e := range entries {
				if !e.IsDir() && isLicenseFile(e.Name()) {
					found = append(found, e.Name())
				}
			}
			if len(found) > 0 {
				return filepath.Join(dir, preferLicense(found)), nil
			}
		}
		if dir == moduleDir || len(dir) <= len(moduleDir) {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// packageSubdir returns the package's path relative to its module root by
// matching the module directory's trailing path against the import path.
func packageSubdir(importPath, moduleDir string) string {
	base := filepath.Base(moduleDir)
	if i := strings.Index(base, "@"); i > 0 {
		base = base[:i]
	}
	if idx := strings.LastIndex(importPath, "/"+base+"/"); idx >= 0 {
		return importPath[idx+len(base)+2:]
	}
	return ""
}

func readNotice(moduleDir string) string {
	for _, name := range []string{"NOTICE", "NOTICE.txt", "NOTICE.md"} {
		if b, err := os.ReadFile(filepath.Join(moduleDir, name)); err == nil {
			return strings.TrimRight(string(b), "\n")
		}
	}
	return ""
}

const generatedBanner = "<!-- Generated by `just notices` (cmd/gen-notices). Do not edit by hand. -->"

func renderThirdParty(comps []component) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", generatedBanner)
	b.WriteString("# Third-Party Notices\n\n")
	b.WriteString("Loom is distributed with the third-party open-source components listed below.\n")
	b.WriteString("Each is reproduced with the license text supplied by its author.\n\n")
	b.WriteString("This file covers the components compiled into the distributed binaries\n")
	b.WriteString("(`loom`, `looms`, `loom-standalone`), not the full development dependency\n")
	b.WriteString("graph — test-only and build-time modules are not distributed and are excluded.\n\n")

	// Group by identical license text so a shared license is printed once.
	type group struct {
		text  string
		comps []component
	}
	byHash := map[string]*group{}
	var order []string
	for _, c := range comps {
		sum := sha256.Sum256([]byte(c.License))
		h := hex.EncodeToString(sum[:8])
		if _, ok := byHash[h]; !ok {
			byHash[h] = &group{text: c.License}
			order = append(order, h)
		}
		byHash[h].comps = append(byHash[h].comps, c)
	}
	sort.Slice(order, func(i, j int) bool {
		return byHash[order[i]].comps[0].ImportPath < byHash[order[j]].comps[0].ImportPath
	})

	fmt.Fprintf(&b, "%d components, %d distinct license texts.\n\n", len(comps), len(order))
	b.WriteString("## Components\n\n")
	b.WriteString("| Component | Version |\n|---|---|\n")
	for _, c := range comps {
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", c.ImportPath, c.Version)
	}

	b.WriteString("\n## The Go standard library\n\n")
	b.WriteString("The Go standard library and runtime are statically linked into every binary.\n")
	b.WriteString("They are distributed under the BSD 3-Clause license, Copyright (c) 2009 The Go\n")
	b.WriteString("Authors. See https://go.dev/LICENSE.\n")

	b.WriteString("\n## Bundled C libraries\n\n")
	b.WriteString("The binaries are built with cgo. `github.com/mutecomm/go-sqlcipher` is MIT as a\n")
	b.WriteString("Go module, but compiles in C source from three upstream projects whose licenses\n")
	b.WriteString("are reproduced in that module's own license text below:\n\n")
	b.WriteString("- `mattn/go-sqlite3` (SQLite driver and amalgamation) — MIT\n")
	b.WriteString("- `sqlcipher/sqlcipher` (encrypted SQLite) — BSD 3-Clause, Copyright (c) 2008 ZETETIC LLC\n")
	b.WriteString("- `libtom/libtomcrypt` (AES, HMAC, PRNG) — public domain\n")

	b.WriteString("\n## License texts\n")
	for _, h := range order {
		g := byHash[h]
		b.WriteString("\n---\n\n### ")
		names := make([]string, 0, len(g.comps))
		for _, c := range g.comps {
			names = append(names, "`"+c.ImportPath+"`")
		}
		if len(names) == 1 {
			b.WriteString(names[0] + "\n\n")
		} else {
			fmt.Fprintf(&b, "%s\n\nand %d further component(s) under the identical license text:\n\n",
				names[0], len(names)-1)
			for _, n := range names[1:] {
				b.WriteString("- " + n + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("```\n")
		b.WriteString(g.text)
		b.WriteString("\n```\n")
	}
	return []byte(b.String())
}

func renderNotice(comps []component) []byte {
	var b strings.Builder
	b.WriteString("Loom\n")
	fmt.Fprintf(&b, "Copyright %d Teradata\n\n", time.Now().Year())
	b.WriteString("This product includes software developed at Teradata.\n\n")
	b.WriteString("Licensed under the Apache License, Version 2.0. See the LICENSE file.\n\n")
	b.WriteString(strings.Repeat("-", 76) + "\n\n")
	b.WriteString("This product bundles third-party software. Attribution notices required by\n")
	b.WriteString("Apache License 2.0 section 4(d) are reproduced below, verbatim from the\n")
	b.WriteString("NOTICE files of the redistributed works.\n\n")
	b.WriteString("Full license texts for every bundled component are in THIRD-PARTY-NOTICES.md.\n")

	// Only distinct module-level NOTICE files, one entry per module.
	type nz struct{ mod, ver, text string }
	seen := map[string]bool{}
	var list []nz
	for _, c := range comps {
		if c.Notice == "" || seen[c.Module] {
			continue
		}
		seen[c.Module] = true
		list = append(list, nz{c.Module, c.Version, c.Notice})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod < list[j].mod })

	for _, n := range list {
		b.WriteString("\n" + strings.Repeat("-", 76) + "\n\n")
		fmt.Fprintf(&b, "%s %s\n\n", n.mod, n.ver)
		b.WriteString(n.text + "\n")
	}
	return []byte(b.String())
}
