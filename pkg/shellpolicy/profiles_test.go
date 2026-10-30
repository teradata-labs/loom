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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileViolation(t *testing.T) {
	sort := Readonly().Programs["sort"]
	for args, want := range map[string]string{
		"-k2 a":               "",
		"-o out a":            "-o",
		"-ro out a":           "-o",
		"-T /tmp a":           "-T",
		"--output=x a":        "--output",
		"--out x":             "--output",
		"--compress-prog=gz":  "--compress-program",
		"-- -o":               "",
		"--reverse --numeric": "",
	} {
		v := sort.violation(fields(args), true)
		if want == "" {
			assert.Empty(t, v, args)
		} else {
			assert.Contains(t, v, want, args)
		}
	}
	uniq := Readonly().Programs["uniq"]
	assert.Empty(t, uniq.violation(fields("-f 1 a"), true))
	assert.Empty(t, uniq.violation(fields("-c a"), true))
	assert.NotEmpty(t, uniq.violation(fields("a b"), true))
	assert.Empty(t, uniq.violation(fields("a b"), false), "positional counting needs every argument")
}

func TestGitProfile(t *testing.T) {
	git := Readonly().Programs["git"]
	for args, allowed := range map[string]bool{
		"status":                      true,
		"log -p --stat":               true,
		"diff HEAD~1":                 true,
		"show HEAD:f.txt":             true,
		"grep -c pattern":             true,
		"--no-pager log":              true,
		"-C sub status":               true,
		"branch --list -a":            true,
		"rev-parse --show-toplevel":   true,
		"-c core.pager=sh log":        false,
		"--git-dir=/x log":            false,
		"--work-tree /x status":       false,
		"--attr-source=HEAD diff":     false,
		"--exec-path=/x log":          false,
		"-p log":                      false,
		"log --ext-diff":              false,
		"diff --textconv":             false,
		"grep -O pattern":             false,
		"log --output=/tmp/x":         false,
		"push":                        false,
		"commit -m x":                 false,
		"branch -D x":                 false,
		"st":                          false,
		"":                            false,
		"config --global user.name x": false,
	} {
		v := git.violation(fields(args), true)
		assert.Equal(t, allowed, v == "", "git %s: %s", args, v)
	}
}

func TestGitHarden(t *testing.T) {
	git := Readonly().Programs["git"]
	prefix := []string{"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "--no-pager", "--attr-source=" + EmptyTreeHash}
	assert.Equal(t, append(append([]string{}, prefix...), "status", "-s"), git.Harden(fields("status -s")))
	assert.Equal(t, append(append([]string{}, prefix...), "-C", "sub", "log", "--no-ext-diff", "--no-textconv", "-p"),
		git.Harden(fields("-C sub log -p")))
	assert.Equal(t, prefix, git.Harden(nil))
	assert.Contains(t, git.Env, "GIT_CONFIG_GLOBAL=/dev/null")
}

func TestPolicyHelpers(t *testing.T) {
	ro := Readonly()
	require.NoError(t, ro.Validate())
	dev := ro.Extend("dev", []string{"make", "go", "curl"}, []string{"curl", "git"})
	require.NoError(t, dev.Validate())
	assert.Contains(t, dev.Programs, "make")
	assert.NotContains(t, dev.Programs, "curl", "never wins over allow")
	assert.NotContains(t, dev.Programs, "git")
	assert.True(t, dev.Never["git"])
	assert.Contains(t, ro.Programs, "git", "Extend leaves the original alone")

	bad := &Policy{Name: "bad", Programs: map[string]*Profile{"/bin/ls": {}, "rm": {}}, Never: map[string]bool{"rm": true}}
	err := bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"/bin/ls" is not a bare program name`)
	assert.Contains(t, err.Error(), `"rm" is both allowed and never allowed`)
	assert.Error(t, (&Policy{}).Validate())

	merged := MergeGrants([]any{&Grant{Programs: []string{"rm"}}, "not a grant", (*Grant)(nil), &Grant{Programs: []string{"rm", "npm"}, ReadPaths: []string{"/x"}}})
	require.NotNil(t, merged)
	assert.Equal(t, []string{"rm", "npm"}, merged.Programs)
	assert.Equal(t, []string{"/x"}, merged.ReadPaths)
	assert.Nil(t, MergeGrants([]any{"x"}))
	assert.Nil(t, MergeGrants(nil))

	assert.Equal(t, []string{"base64", "basename"}, ro.ProgramNames()[:2])
}

func TestWithinDirAndResolvePath(t *testing.T) {
	assert.True(t, WithinDir("/a", "/a/b"))
	assert.False(t, WithinDir("/a", "/ab"))
	assert.False(t, WithinDir("/a/b", "/a"))
	assert.True(t, WithinDir("/", "/x"))
	root := session(t).Roots.Read[0]
	assert.Equal(t, root+"/new/file.txt", ResolvePath(root, "new/file.txt"), "a path that does not exist yet resolves under its existing parent")
	assert.Equal(t, root+"/notes.txt", ResolvePath(root+"/sub", "../notes.txt"))
}

func fields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
