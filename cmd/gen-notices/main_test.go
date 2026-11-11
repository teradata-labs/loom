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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModule creates a module directory under root. When license is non-empty
// it is written as the module's LICENSE file; notice likewise for NOTICE.
func fakeModule(t *testing.T, root, name, license, notice string) string {
	t.Helper()
	dir := filepath.Join(root, name+"@v1.0.0")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if license != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(license+"\n"), 0o644))
	}
	if notice != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "NOTICE"), []byte(notice+"\n"), 0o644))
	}
	return dir
}

// pkg builds a pkgInfo for a third-party package rooted at its module.
func pkg(importPath, modPath, dir string) pkgInfo {
	p := pkgInfo{ImportPath: importPath}
	p.Module = &struct {
		Path    string
		Version string
		Dir     string
	}{Path: modPath, Version: "v1.0.0", Dir: dir}
	return p
}

// TestCollectPkgsUnionAcrossPlatforms checks that folding several platforms'
// package lists into one accumulator yields the UNION: a module linked on only
// one platform must still be attributed, a module linked on every platform is
// listed once, and stdlib / own-module / module-less packages are excluded.
func TestCollectPkgsUnionAcrossPlatforms(t *testing.T) {
	root := t.TempDir()
	common := fakeModule(t, root, "common", "MIT common", "")
	linuxOnly := fakeModule(t, root, "linuxonly", "BSD linuxonly", "linuxonly NOTICE")
	darwinOnly := fakeModule(t, root, "darwinonly", "ISC darwinonly", "")
	own := fakeModule(t, root, "loom", "Apache loom", "")

	stdlib := pkgInfo{ImportPath: "fmt", Standard: true}
	noModule := pkgInfo{ImportPath: "example.com/nomod"}

	tests := []struct {
		name      string
		platforms [][]pkgInfo
		want      []string // expected component ImportPaths, sorted
	}{
		{
			name: "single platform",
			platforms: [][]pkgInfo{
				{pkg("example.com/common", "example.com/common", common)},
			},
			want: []string{"example.com/common"},
		},
		{
			name: "module only on second platform is kept",
			platforms: [][]pkgInfo{
				{pkg("example.com/common", "example.com/common", common)},
				{
					pkg("example.com/common", "example.com/common", common),
					pkg("example.com/linuxonly", "example.com/linuxonly", linuxOnly),
				},
			},
			want: []string{"example.com/common", "example.com/linuxonly"},
		},
		{
			name: "disjoint platform-specific modules are all kept",
			platforms: [][]pkgInfo{
				{pkg("example.com/darwinonly", "example.com/darwinonly", darwinOnly)},
				{pkg("example.com/linuxonly", "example.com/linuxonly", linuxOnly)},
				{pkg("example.com/common", "example.com/common", common)},
			},
			want: []string{"example.com/common", "example.com/darwinonly", "example.com/linuxonly"},
		},
		{
			name: "stdlib, own module and module-less packages are excluded",
			platforms: [][]pkgInfo{
				{
					stdlib,
					noModule,
					pkg(ownModule+"/pkg/agent", ownModule, own),
					pkg("example.com/common", "example.com/common", common),
				},
			},
			want: []string{"example.com/common"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]component{}
			unlicensed := map[string]bool{}
			for _, pkgs := range tt.platforms {
				require.NoError(t, collectPkgs(pkgs, seen, unlicensed))
			}
			comps, err := finish(seen, unlicensed)
			require.NoError(t, err)

			got := make([]string, 0, len(comps))
			for _, c := range comps {
				got = append(got, c.ImportPath)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCollectPkgsVendoredThirdParty checks that own-module code vendored under
// third_party/<name>/ is attributed to that directory's license, once for all
// its packages, and that a vendored directory without a license is an error.
// The rest of the own module stays excluded.
func TestCollectPkgsVendoredThirdParty(t *testing.T) {
	own := t.TempDir()
	vend := filepath.Join(own, vendoredDir, "golua")
	require.NoError(t, os.MkdirAll(filepath.Join(vend, "runtime"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vend, "LICENSE"), []byte("Apache golua\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(own, vendoredDir, "nolicense"), 0o755))

	t.Run("vendored packages are attributed to their directory", func(t *testing.T) {
		seen := map[string]component{}
		unlicensed := map[string]bool{}
		require.NoError(t, collectPkgs([]pkgInfo{
			pkg(ownModule+"/pkg/agent", ownModule, own),
			pkg(ownModule+"/third_party/golua/runtime", ownModule, own),
			pkg(ownModule+"/third_party/golua/lib/base", ownModule, own),
		}, seen, unlicensed))
		comps, err := finish(seen, unlicensed)
		require.NoError(t, err)
		require.Len(t, comps, 1, "one component per vendored directory; the own module stays excluded")
		assert.Equal(t, ownModule+"/third_party/golua", comps[0].ImportPath)
		assert.Equal(t, "vendored", comps[0].Version)
		assert.Equal(t, "Apache golua", comps[0].License)
	})

	t.Run("vendored directory without a license is an error", func(t *testing.T) {
		seen := map[string]component{}
		unlicensed := map[string]bool{}
		require.NoError(t, collectPkgs([]pkgInfo{pkg(ownModule+"/third_party/nolicense/x", ownModule, own)}, seen, unlicensed))
		_, err := finish(seen, unlicensed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ownModule+"/third_party/nolicense")
	})
}

// TestCollectPkgsCarriesLicenseAndNotice checks the verbatim license text and
// NOTICE are attached to the component.
func TestCollectPkgsCarriesLicenseAndNotice(t *testing.T) {
	root := t.TempDir()
	dir := fakeModule(t, root, "withnotice", "Apache text", "upstream NOTICE")

	seen := map[string]component{}
	unlicensed := map[string]bool{}
	require.NoError(t, collectPkgs([]pkgInfo{pkg("example.com/withnotice", "example.com/withnotice", dir)}, seen, unlicensed))
	comps, err := finish(seen, unlicensed)
	require.NoError(t, err)
	require.Len(t, comps, 1)
	assert.Equal(t, "Apache text", comps[0].License)
	assert.Equal(t, "upstream NOTICE", comps[0].Notice)
}

// TestFinish checks that any distributed component without a license file is
// a hard error naming the component, never a silent skip.
func TestFinish(t *testing.T) {
	root := t.TempDir()
	licensed := fakeModule(t, root, "licensed", "MIT licensed", "")
	unlicensedDir := fakeModule(t, root, "nolicense", "", "")

	tests := []struct {
		name    string
		pkgs    []pkgInfo
		wantErr []string // substrings the error must contain; nil means no error
		wantN   int
	}{
		{
			name:  "all licensed",
			pkgs:  []pkgInfo{pkg("example.com/licensed", "example.com/licensed", licensed)},
			wantN: 1,
		},
		{
			name:    "unlicensed package errors",
			pkgs:    []pkgInfo{pkg("example.com/nolicense", "example.com/nolicense", unlicensedDir)},
			wantErr: []string{"no license file found for 1", "example.com/nolicense v1.0.0"},
		},
		{
			name: "one unlicensed among licensed still errors",
			pkgs: []pkgInfo{
				pkg("example.com/licensed", "example.com/licensed", licensed),
				pkg("example.com/nolicense", "example.com/nolicense", unlicensedDir),
			},
			wantErr: []string{"no license file found for 1", "example.com/nolicense v1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]component{}
			unlicensed := map[string]bool{}
			require.NoError(t, collectPkgs(tt.pkgs, seen, unlicensed))
			comps, err := finish(seen, unlicensed)
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Len(t, comps, tt.wantN)
				return
			}
			require.Error(t, err)
			assert.Nil(t, comps)
			for _, s := range tt.wantErr {
				assert.Contains(t, err.Error(), s)
			}
		})
	}

	t.Run("unlicensed set alone errors", func(t *testing.T) {
		comps, err := finish(map[string]component{}, map[string]bool{"example.com/x v0.1.0": true})
		require.Error(t, err)
		assert.Nil(t, comps)
		assert.Contains(t, err.Error(), "example.com/x v0.1.0")
	})
}

// TestRenderThirdPartyReproducesGoLicense checks the Go standard library's
// license text is reproduced verbatim rather than linked.
func TestRenderThirdPartyReproducesGoLicense(t *testing.T) {
	comps := []component{{ImportPath: "example.com/a", Module: "example.com/a", Version: "v1.0.0", License: "MIT a"}}
	const goLicense = "Copyright 2009 The Go Authors.\n\nRedistribution and use ..."

	out := string(renderThirdParty(comps, goLicense))
	assert.Contains(t, out, "## The Go standard library")
	assert.Contains(t, out, "```\n"+goLicense+"\n```\n")
	assert.NotContains(t, out, "https://go.dev/LICENSE")
}

// TestGoStdlibLicense checks the active toolchain's LICENSE is found and is the
// Go BSD license.
func TestGoStdlibLicense(t *testing.T) {
	text, err := goStdlibLicense()
	require.NoError(t, err)
	assert.Contains(t, text, "The Go Authors")
	assert.Contains(t, text, "Redistribution and use in source and binary forms")
}

// TestGoListEnvPinsAmbientValues checks that goListEnv removes any inherited
// GOWORK, GOOS, GOARCH or CGO_ENABLED and sets exactly one pinned value for
// each, so the caller's environment cannot change what `go list` resolves.
func TestGoListEnvPinsAmbientValues(t *testing.T) {
	plat := platform{"linux", "amd64"}
	want := []string{"PATH=/bin", "GOWORK=off", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}

	tests := []struct {
		name string
		base []string
	}{
		{"nothing set", []string{"PATH=/bin"}},
		{"CGO_ENABLED=1", []string{"PATH=/bin", "CGO_ENABLED=1"}},
		{"CGO_ENABLED=0", []string{"CGO_ENABLED=0", "PATH=/bin"}},
		{"everything set", []string{"GOOS=windows", "PATH=/bin", "GOARCH=arm64", "CGO_ENABLED=1", "GOWORK=/x/go.work"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, want, goListEnv(tt.base, plat, "0"))
		})
	}
}

// TestGoListEnvKeepsSimilarKeys checks that only exact keys are removed: a
// variable whose name merely starts with a pinned key is kept.
func TestGoListEnvKeepsSimilarKeys(t *testing.T) {
	got := goListEnv([]string{"GOOSE=1", "CGO_ENABLED_X=1"}, platform{"darwin", "arm64"}, "1")
	assert.Equal(t, []string{"GOOSE=1", "CGO_ENABLED_X=1",
		"GOWORK=off", "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1"}, got)
}

// TestCollectIndependentOfAmbientCgo checks that the generated notices are the
// UNION of the cgo and non-cgo dependency sets, and are byte-identical whether
// CGO_ENABLED is unset, 0 or 1 in the caller's environment. The fake lister
// resolves through goListEnv and reads CGO_ENABLED from the env it would pass
// to `go list`, so an inherited value that leaked through would change the
// result.
func TestCollectIndependentOfAmbientCgo(t *testing.T) {
	root := t.TempDir()
	common := fakeModule(t, root, "common", "MIT common", "")
	cgoOnly := fakeModule(t, root, "sqlcipher", "MIT sqlcipher", "")
	pureOnly := fakeModule(t, root, "modernc", "BSD modernc", "modernc NOTICE")

	calls := map[string]int{}
	list := func(_ target, plat platform, cgo string) ([]pkgInfo, error) {
		env := goListEnv(os.Environ(), plat, cgo)
		effective := ""
		for _, kv := range env { // exec.Cmd uses the last value of a key
			if v, ok := strings.CutPrefix(kv, "CGO_ENABLED="); ok {
				effective = v
			}
		}
		calls[effective]++
		pkgs := []pkgInfo{pkg("example.com/common", "example.com/common", common)}
		if effective == "1" {
			pkgs = append(pkgs, pkg("example.com/sqlcipher", "example.com/sqlcipher", cgoOnly))
		} else {
			pkgs = append(pkgs, pkg("example.com/modernc", "example.com/modernc", pureOnly))
		}
		return pkgs, nil
	}

	render := func(t *testing.T) string {
		t.Helper()
		comps, err := collectWith(list)
		require.NoError(t, err)
		var paths []string
		for _, c := range comps {
			paths = append(paths, c.ImportPath)
		}
		assert.Equal(t, []string{"example.com/common", "example.com/modernc", "example.com/sqlcipher"}, paths,
			"the cgo and non-cgo sets must both be attributed")
		return string(renderThirdParty(comps, "GO LICENSE")) + string(renderNotice(comps))
	}

	var outputs []string
	for _, ambient := range []string{"unset", "0", "1"} {
		t.Run("CGO_ENABLED="+ambient, func(t *testing.T) {
			if ambient == "unset" {
				t.Setenv("CGO_ENABLED", "") // registers restore on cleanup
				require.NoError(t, os.Unsetenv("CGO_ENABLED"))
			} else {
				t.Setenv("CGO_ENABLED", ambient)
			}
			clear(calls)
			outputs = append(outputs, render(t))
			perMode := len(targets) * len(platforms)
			assert.Equal(t, map[string]int{"1": perMode, "0": perMode}, calls,
				"every target and platform must be resolved once per cgo mode")
		})
	}
	require.Len(t, outputs, 3)
	assert.Equal(t, outputs[0], outputs[1], "CGO_ENABLED=0 must not change the output")
	assert.Equal(t, outputs[0], outputs[2], "CGO_ENABLED=1 must not change the output")
}

// TestRenderThirdPartyCgoStatement checks the generated header does not claim
// every distributed binary is built with cgo: the runtime image is not.
func TestRenderThirdPartyCgoStatement(t *testing.T) {
	out := string(renderThirdParty([]component{{ImportPath: "example.com/a", License: "MIT"}}, "GO"))
	assert.Contains(t, out, "Release archives are built with cgo; the runtime image is not")
	assert.NotContains(t, out, "The binaries are built with cgo")
}
