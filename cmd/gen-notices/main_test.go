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
