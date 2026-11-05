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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file hold the commands from the PR #430 review
// (Josh-Schoen, N1-N10) and the vectors found while fixing them. Each unsafe
// command did the harmful thing in bash 5.2 while the analyzer allowed it.

// escapeSession makes the layout the review's bash runs used: the session
// directory w (the working directory and the only root) with sub/,
// victim.txt and a.txt, a sibling o/ outside the session holding secret.txt
// and ssh/k, and w/link, a symlink to o/ssh.
func escapeSession(t *testing.T) (Options, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	w := filepath.Join(base, "w")
	require.NoError(t, os.MkdirAll(filepath.Join(w, "sub"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "o", "ssh"), 0o700))
	for _, f := range []string{filepath.Join(base, "o", "secret.txt"), filepath.Join(base, "o", "ssh", "k"),
		filepath.Join(w, "victim.txt"), filepath.Join(w, "a.txt")} {
		require.NoError(t, os.WriteFile(f, []byte("x\n"), 0o600))
	}
	require.NoError(t, os.Symlink(filepath.Join(base, "o", "ssh"), filepath.Join(w, "link")))
	return Options{WorkingDir: w, Roots: Roots{Read: []string{w}, Write: []string{w}}}, base
}

type bypassCase struct {
	cmd    string
	want   VerdictKind // enforcement: runtime
	static VerdictKind // enforcement: static
	why    string      // a substring of the static reason, when not Allow
}

// runBypassCases judges each command under both enforcement modes in the
// escapeSession layout.
func runBypassCases(t *testing.T, cases []bypassCase) {
	t.Helper()
	opt, _ := escapeSession(t)
	pol := Readonly()
	for _, tc := range cases {
		for _, static := range []bool{false, true} {
			o := opt
			o.Static = static
			v := pol.Analyze(tc.cmd, o)
			want := tc.want
			if static {
				want = tc.static
			}
			if !assert.Equal(t, want, v.Kind, "%q (static=%v): %s", tc.cmd, static, v.Reason()) {
				continue
			}
			if static && tc.why != "" {
				assert.Contains(t, v.Reason(), tc.why, "%q (static)", tc.cmd)
			}
			if v.Kind == Ask {
				assert.NotNil(t, v.Grant, "%q: an Ask carries what approving it allows", tc.cmd)
			} else {
				assert.Nil(t, v.Grant, "%q: only an Ask carries a grant", tc.cmd)
			}
		}
	}
}

// N3: a name is a function only after a top-level declaration that runs
// before the call, and only until something may unset it. command, exec and
// builtin skip functions.
func TestAnalyze_FunctionShadowing(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `rm victim.txt; rm() { :; }`, want: Ask, static: Ask, why: "rm (1:1)"},
		{cmd: `if false; then rm() { :; }; fi; rm a.txt`, want: Ask, static: Ask, why: "rm (1:"},
		{cmd: `rm() { :; }; command rm a.txt`, want: Ask, static: Ask, why: "rm (1:"},
		{cmd: `rm() { :; }; exec rm a.txt`, want: Ask, static: Ask},
		{cmd: `rm() { :; }; unset -f rm; rm a.txt`, want: Ask, static: Ask, why: "rm (1:"},
		{cmd: `rm() { :; }; unset rm; rm a.txt`, want: Ask, static: Ask},
		{cmd: `rm() { :; }; for i in 1 2; do rm a.txt; eval 'unset -f rm'; done`, want: Ask, static: Ask},
		{cmd: `rm() { :; }; unset "$f"; rm a.txt`, want: Ask, static: Ask},
		{cmd: `rm() { :; } & rm a.txt`, want: Ask, static: Ask},
		{cmd: `false && rm() { :; }; rm a.txt`, want: Ask, static: Ask},
		{cmd: `(rm() { :; }); rm a.txt`, want: Ask, static: Ask},
		{cmd: `eval 'rm() { :; }'; rm a.txt`, want: Ask, static: Ask},
		{cmd: `f() { rm a.txt; }; rm() { :; }; f`, want: Ask, static: Ask},
		{cmd: `rm() { :; }; unset -v rm; rm a.txt`, want: Allow, static: Allow},
		{cmd: `rm() { echo fake; }; rm nope; f() { rm x; }; f; eval 'rm y'`, want: Allow, static: Allow},
	})
}
