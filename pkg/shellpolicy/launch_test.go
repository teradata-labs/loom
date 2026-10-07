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
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func TestCheckLaunch(t *testing.T) {
	opt := session(t)
	root := opt.Roots.Read[0]
	hosts, err := filepath.EvalSymlinks("/etc/hosts")
	require.NoError(t, err)
	pol := Readonly()
	for _, tc := range []struct {
		name  string
		argv  []string
		dir   string
		grant *Grant
		block string // a substring of the reason; "" means allowed
	}{
		{name: "listed", argv: []string{"ls", "-la"}},
		{name: "off-list", argv: []string{"rm", "-rf", "nope"}, block: "not on the shell allowlist"},
		{name: "off-list granted", argv: []string{"rm", "-rf", "nope"}, grant: &Grant{Programs: []string{"rm"}}},
		{name: "computed find -exec", argv: []string{"find", ".", "-exec", "rm", "{}", ";"}, block: "-exec"},
		{name: "sort -o", argv: []string{"sort", "-o", "x", "a"}, block: "-o"},
		{name: "profile waived by grant", argv: []string{"sort", "-o", "x", "a"}, grant: &Grant{Programs: []string{"sort"}}},
		{name: "git log", argv: []string{"git", "log", "-p"}},
		{name: "git -c", argv: []string{"git", "-c", "core.pager=sh", "log"}, block: "git -c"},
		{name: "git push", argv: []string{"git", "push"}, block: "git push"},
		{name: "read outside", argv: []string{"cat", "/etc/hosts"}, block: "outside the session"},
		{name: "read granted file", argv: []string{"cat", "/etc/hosts"}, grant: &Grant{ReadPaths: []string{hosts}}},
		{name: "read granted dir", argv: []string{"cat", "/etc/hosts"}, grant: &Grant{ReadPaths: []string{filepath.Dir(hosts)}}},
		{name: "relative read escaping", argv: []string{"cat", strings.Repeat("../", 30) + "etc/hosts"}, block: "outside the session"},
		{name: "cwd outside", argv: []string{"ls"}, dir: "/", block: "outside the session"},
		{name: "cwd granted", argv: []string{"ls"}, dir: "/", grant: &Grant{ReadPaths: []string{"/"}}},
		{name: "by path", argv: []string{"/bin/rm", "x"}, block: "named by path"},
		{name: "by path granted", argv: []string{"/bin/rm", "x"}, grant: &Grant{Programs: []string{"/bin/rm"}}},
		{name: "own git in cwd", argv: []string{"./git", "log"}, block: "named by path"},
		{name: "never, even granted", argv: []string{"sudo", "ls"}, grant: &Grant{Programs: []string{"sudo"}}, block: "never allowed"},
		{name: "never by path", argv: []string{"/usr/bin/sudo"}, block: "never allowed"},
		{name: "no path args", argv: []string{"basename", "/etc/hosts"}},
		{name: "empty", argv: nil, block: "no program"},
	} {
		dir := tc.dir
		if dir == "" {
			dir = root
		}
		err := pol.CheckLaunch(tc.argv, dir, opt.Roots, tc.grant)
		if tc.block == "" {
			assert.NoError(t, err, tc.name)
			continue
		}
		var le *LaunchError
		require.True(t, errors.As(err, &le), "%s: want a block, got %v", tc.name, err)
		assert.Contains(t, le.Reason, tc.block, tc.name)
	}
}

func TestCheckBuiltinOpenListDir(t *testing.T) {
	opt := session(t)
	root := opt.Roots.Read[0]
	pol := Readonly()
	assert.NoError(t, pol.CheckBuiltin("echo", nil))
	assert.Error(t, pol.CheckBuiltin("alias", nil))
	assert.NoError(t, pol.CheckBuiltin("alias", &Grant{Builtins: []string{"alias"}}))
	assert.ErrorContains(t, pol.CheckBuiltin("kill", &Grant{Builtins: []string{"kill"}}), "not supported")

	assert.NoError(t, pol.CheckOpen("out.txt", root, true, opt.Roots, nil))
	assert.NoError(t, pol.CheckOpen("/dev/null", root, true, opt.Roots, nil))
	assert.ErrorContains(t, pol.CheckOpen("/etc/x", root, true, opt.Roots, &Grant{ReadPaths: []string{"/"}}), "writes outside",
		"a read grant never covers a write")
	assert.ErrorContains(t, pol.CheckOpen(strings.Repeat("../", 30)+"etc/hosts", root, false, opt.Roots, nil), "reads outside")
	assert.NoError(t, pol.CheckOpen("/etc/hosts", root, false, opt.Roots, &Grant{ReadPaths: []string{ResolvePath("/", "/etc")}}))

	assert.NoError(t, pol.CheckListDir(root, opt.Roots, nil))
	assert.Error(t, pol.CheckListDir("/etc", opt.Roots, nil))
	assert.NoError(t, pol.CheckListDir("/etc", opt.Roots, &Grant{ReadPaths: []string{ResolvePath("/", "/etc")}}))
}

// Every builtin the interpreter names is either implemented (and so judged by
// the policy) or refused as unsupported, and implementedBuiltins says which.
// A version bump that implements or drops one fails here.
func TestImplementedBuiltinsMatchInterpreter(t *testing.T) {
	names := []string{
		"alias", "bg", "cd", "command", "false", "fc", "fg", "getopts", "hash", "jobs", "kill",
		"newgrp", "pwd", "read", "true", "umask", "unalias", "wait", "break", ":", "continue",
		".", "eval", "exec", "exit", "return", "set", "shift", "times", "trap", "unset",
		"source", "bind", "builtin", "caller", "compgen", "complete", "compopt", "dirs",
		"disown", "enable", "history", "help", "logout", "mapfile", "readarray", "popd",
		"pushd", "shopt", "suspend", "type", "ulimit", "echo", "printf", "test", "[",
	}
	for _, name := range names {
		require.True(t, interp.IsBuiltin(name), "%s is no longer an interpreter builtin", name)
		var out bytes.Buffer
		file, err := syntax.NewParser().Parse(strings.NewReader(name+" x 2>&1 </dev/null || true"), "")
		require.NoError(t, err)
		r, err := interp.New(interp.StdIO(strings.NewReader(""), &out, &out),
			interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(context.Context, []string) error { return interp.ExitStatus(127) }
			}))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = r.Run(ctx, file)
		cancel()
		unsupported := strings.Contains(out.String(), "unsupported builtin")
		assert.Equal(t, !unsupported, implementedBuiltins[name], "%s: interpreter says %q", name, out.String())
	}
}
