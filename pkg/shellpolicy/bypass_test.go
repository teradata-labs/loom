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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
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

// N1: relative paths after cd resolve against every directory the command
// can change to, and under static a cd the analyzer cannot follow is refused.
func TestAnalyze_ChangedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `cd sub && cat ../../o/secret.txt`, want: Ask, static: Ask, why: "../../o/secret.txt (1:11): reads outside the read roots"},
		{cmd: `pushd sub && cat ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `cat ../../o/secret.txt; cd sub`, want: Ask, static: Ask},
		{cmd: `eval 'cd sub'; cat ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `f() { cd sub; }; f; cat < ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `cd sub; cd ..; cd sub; cat ../../o/ssh/k`, want: Ask, static: Ask},
		{cmd: `cd /; echo x > y`, want: Deny, static: Deny, why: "y (1:14): writes outside the write roots"},
		{cmd: `cd && cat .ssh/config`, want: Allow, static: Deny, why: "cd (1:1)"},
		{cmd: `cd -P && cat x`, want: Allow, static: Deny},
		{cmd: `cd - && cat secret.txt`, want: Allow, static: Deny, why: "$OLDPWD"},
		{cmd: `popd; cat x`, want: Allow, static: Deny},
		{cmd: `pushd; cat x`, want: Allow, static: Deny},
		{cmd: `pushd +1; cat x`, want: Allow, static: Deny, why: "rotates the directory stack"},
		{cmd: `CDPATH=..; cd o && cat secret.txt`, want: Allow, static: Deny, why: "sets CDPATH"},
		{cmd: `export CDPATH=..`, want: Allow, static: Deny},
		{cmd: `cd sub && cat ../a.txt && ls; cd -- sub; cd -P sub`, want: Allow, static: Allow},
	})
}

// N1: an inherited CDPATH sends a bare cd target to another directory. An
// empty entry is the current directory and a relative one is relative to it.
func TestAnalyze_InheritedCDPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt, base := escapeSession(t)
	t.Setenv("CDPATH", ":sub:"+base)
	for _, static := range []bool{false, true} {
		opt.Static = static
		v := Readonly().Analyze(`cd o && cat secret.txt`, opt)
		assert.Equal(t, Ask, v.Kind, "static=%v: %s", static, v.Reason())
		assert.Contains(t, v.Reason(), "secret.txt (1:9): reads outside the read roots")
		assert.Contains(t, v.Reason(), "o (1:4): reads outside the read roots")
	}
}

// N2: a .. after a glob is refused under static, and every name a glob
// matches is path-checked, so a symlink it matches is followed. A literal ..
// after a symlink leaves the symlink's target, as the kernel opens it.
func TestAnalyze_GlobPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `cat */../../o/secret.txt`, want: Allow, static: Deny, why: "a .. after a glob"},
		{cmd: `cat s?b/../../o/secret.txt`, want: Allow, static: Deny},
		{cmd: `cat */k`, want: Allow, static: Ask, why: "*/k (1:5): matches names outside the read roots"},
		{cmd: `cat l*`, want: Allow, static: Ask},
		{cmd: `cd sub; cat ../l*/k`, want: Ask, static: Ask},
		{cmd: `cat .*/o/secret.txt`, want: Allow, static: Ask},
		{cmd: `cat link/../secret.txt`, want: Ask, static: Ask, why: "reads outside the read roots"},
		{cmd: `echo x > link/../new.txt`, want: Deny, static: Deny, why: "writes outside the write roots"},
		{cmd: `cat *.txt sub/* [av]*.txt`, want: Allow, static: Allow},
		{cmd: `[ -f a.txt ] && [ "$x" = y ]`, want: Allow, static: Allow},
	})
}

// A glob that matches too many names, or that bash cannot parse, cannot be
// checked under static.
func TestGlobExpand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt, _ := escapeSession(t)
	w := opt.WorkingDir
	got, err := globExpand(w, "*.txt", 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a.txt", "victim.txt"}, got)
	got, err = globExpand(w, "l*/k", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"link/k"}, got)
	got, err = globExpand(w, ".*", 10)
	require.NoError(t, err)
	assert.Subset(t, got, []string{".", ".."}, "a leading-dot component also matches . and ..")
	got, err = globExpand(w, w+"/s*", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{w + "/sub"}, got)
	_, err = globExpand(w, "*", 1)
	assert.ErrorIs(t, err, errTooManyMatches)

	o := opt
	o.Static = true
	for i := 0; i < maxGlobMatches+1; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(w, "sub", "f"+itoa(i)), nil, 0o600))
	}
	v := Readonly().Analyze(`cat sub/*`, o)
	assert.Equal(t, Deny, v.Kind, v.Reason())
	assert.Contains(t, v.Reason(), "matches too many names")
}

// N5: under static, a glob can expand to more files than a profile's
// positional limit, and no runner counts them.
func TestAnalyze_GlobPositional(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `uniq /dev/null victim.tx*`, want: Allow, static: Ask, why: "a glob may expand to more than 1 file argument(s)"},
		{cmd: `uniq -c victim.tx*`, want: Allow, static: Ask},
		{cmd: `uniq -c victim.txt; wc -l *.txt`, want: Allow, static: Allow},
	})
}

// N6: a file name attached to a short option is path-checked, and date's
// options that read a file are refused. A value attached to an option is not
// scanned for refused option letters.
func TestAnalyze_AttachedOptionValues(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `file -f../o/secret.txt`, want: Ask, static: Ask, why: "file (1:1): -f is outside the profile"}, // F2 refuses -f outright
		{cmd: `file -m../o/secret.txt victim.txt`, want: Ask, static: Ask},
		{cmd: `grep -f../o/secret.txt victim.txt`, want: Ask, static: Ask},
		{cmd: `grep -if../o/secret.txt victim.txt`, want: Ask, static: Ask},
		{cmd: `diff -X../o/secret.txt a.txt victim.txt`, want: Ask, static: Ask},
		{cmd: `base64 -i../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `date -f ../o/secret.txt`, want: Ask, static: Ask, why: "-f is outside the profile"},
		{cmd: `date -f../o/secret.txt`, want: Ask, static: Ask, why: "-f is outside the profile"},
		{cmd: `date --file=../o/secret.txt`, want: Ask, static: Ask, why: "--file"},
		{cmd: `date -r victim.txt`, want: Ask, static: Ask, why: "-r is outside the profile"},
		{cmd: `date --ref=victim.txt`, want: Ask, static: Ask, why: "--reference"},
		{cmd: `date -d'last sunday' +%s; grep -f victim.txt -e'x' a.txt; sort -t, -k2 -tso a.txt`, want: Allow, static: Allow},
	})
}

// N6 and N7 at run time: the runner applies the same argument rules to each
// launch.
func TestCheckLaunch_ReviewBypasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt, base := escapeSession(t)
	w := opt.WorkingDir
	secret := filepath.Join(base, "o", "secret.txt")
	pol := Readonly()
	for _, tc := range []struct {
		argv  []string
		block string // a substring of the reason; "" means allowed
	}{
		{argv: []string{"file", "-f" + secret}, block: "-f is outside the profile"}, // F2 refuses -f outright
		{argv: []string{"file", "-m../o/secret.txt", "victim.txt"}, block: "outside the read roots"},
		{argv: []string{"grep", "-f" + secret, "a.txt"}, block: "outside the read roots"},
		{argv: []string{"date", "-f", secret}, block: "-f is outside the profile"},
		{argv: []string{"date", "--file=" + secret}, block: "--file"},
		{argv: []string{"git", "grep", "-Otouch pwned", "a"}, block: "-O"},
		{argv: []string{"git", "grep", "--open-files=touch pwned", "a"}, block: "--open-files-in-pager"},
		{argv: []string{"git", "grep", "--op", "a"}, block: "--open-files-in-pager"},
		{argv: []string{"git", "grep", "-iOtouch", "a"}, block: "-O"},
		{argv: []string{"git", "diff", "--outp=x"}, block: "--output"},
		{argv: []string{"git", "diff", "--text"}},
		{argv: []string{"git", "grep", "--or", "-e", "a", "--only-matching"}},
		{argv: []string{"grep", "-f", "a.txt", "victim.txt"}},
		{argv: []string{"cat", "link/../secret.txt"}, block: "outside the read roots"},
	} {
		err := pol.CheckLaunch(tc.argv, w, opt.Roots, nil)
		if tc.block == "" {
			assert.NoError(t, err, "%v", tc.argv)
			continue
		}
		var le *LaunchError
		if assert.True(t, errors.As(err, &le), "%v: want a block, got %v", tc.argv, err) {
			assert.Contains(t, le.Reason, tc.block, "%v", tc.argv)
		}
	}
}

// N4: bash evaluates a subscript in a variable name, and in any value that
// arithmetic reads, and the subscript can hold a command substitution. Under
// static enforcement a name with a computed subscript, and arithmetic that
// reads a variable not proven to hold an integer, are refused.
func TestAnalyze_SubscriptArithmetic(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `x='a[$(touch p)]'; echo $(( x ))`, want: Allow, static: Deny, why: "x (1:29): arithmetic reads x"},
		{cmd: `x='a[$(touch p)]'; echo $(( $x ))`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; (( x ))`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; [[ $x -eq 1 ]]`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; [[ x -eq 1 ]]`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; let y=x`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; s=abc; echo ${s:x}`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; echo ${b[x]}`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; b[x]=1`, want: Allow, static: Deny},
		{cmd: `x='a[$(touch p)]'; echo ${!x}`, want: Allow, static: Deny, why: "indirect expansion"},
		{cmd: `echo $(( $(cat victim.txt) ))`, want: Allow, static: Deny, why: "cannot see"},
		{cmd: `set -- 'a[$(touch p)]'; echo $(( $1 ))`, want: Allow, static: Deny},
		{cmd: `test -v 'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `[ -v 'a[$(touch p)]' ]`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `[[ -v 'a[$(touch p)]' ]]`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `[[ -v $n ]]`, want: Allow, static: Deny},
		{cmd: `printf -v 'a[$(touch p)]' x`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `read 'a[$(touch p)]' <<< x`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `unset 'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `declare 'a[$(touch p)]=1'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `declare -n r='a[$(touch p)]'; echo $r`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `declare -i n; n='a[$(touch p)]'`, want: Allow, static: Deny, why: "declare -i"},
		{cmd: `local -ai n`, want: Allow, static: Deny},
		{cmd: `(( a[$(touch p)] ))`, want: Ask, static: Deny},
		{cmd: `n=0; read n <<< x; echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `n=0; declare -n r=n; r='a[$(touch p)]'; echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `n=0; f() { local n='a[$(touch p)]'; echo $(( n )); }; f`, want: Allow, static: Deny},
		{cmd: `n=0; : ${n:='a[$(touch p)]'}; echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `if false; then n=0; fi; echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `n=0 < missing.txt; echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `REPLY=0; read; echo $(( REPLY ))`, want: Allow, static: Deny},
		{cmd: `echo $(( n )); n=1`, want: Allow, static: Deny},
		{cmd: `for i in 1 x; do echo $(( i )); done`, want: Allow, static: Deny},
		{cmd: `for i in 1 2; do :; done; echo $(( i ))`, want: Allow, static: Deny},
		// Arithmetic over literals and variables that only ever hold integers.
		{cmd: `i=0; (( i++ )); echo $(( i + 1 )) $(( 2#101 + 0x1f + -3 ))`, want: Allow, static: Allow},
		{cmd: `for ((i=0; i<3; i++)); do echo $i ${a[i]}; done`, want: Allow, static: Allow},
		{cmd: `for i in 1 2 3; do echo $(( i * 2 )); done`, want: Allow, static: Allow},
		{cmd: `n=5; [[ $n -gt 3 ]] && echo $(( $# + $? + ${#n} + ${#a[@]} ))`, want: Allow, static: Allow},
		{cmd: `c=$(( 1 + 2 )); c=$(( c * 2 )); export c; echo $c`, want: Allow, static: Allow},
		{cmd: `a=(1 2 3); echo $(( a[1] + ${a[2]} )) ${a[@]} ${!a[@]} ${!PA*}`, want: Allow, static: Allow},
		{cmd: `(( total = 4 )); let half=total/2; echo $(( half ))`, want: Allow, static: Allow},
		{cmd: `[ "$x" -eq 1 ]; printf -v 'a[1]' x; test -v 'a[@]'; [[ -v HOME ]]`, want: Allow, static: Allow},
	})
}

// N8: eval -- and trap -- name the command they run, not "--"; trap options
// and a reset run nothing.
func TestAnalyze_NestedDashDash(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `eval -- 'rm a.txt'`, want: Ask, static: Ask, why: "rm (1:1): not on the shell allowlist"},
		{cmd: `trap -- 'rm a.txt' EXIT`, want: Ask, static: Ask, why: "rm (1:1): not on the shell allowlist"},
		{cmd: `eval -- "$c"`, want: Allow, static: Deny},
		{cmd: `trap -p; trap - EXIT; trap -l; trap -- - INT; eval --`, want: Allow, static: Allow},
	})
}

// N9: bash opens a network connection for a /dev/tcp or /dev/udp path, which
// does not exist on disk, so the read check never saw it.
func TestAnalyze_NetworkDevices(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `cat < /dev/tcp/127.0.0.1/80`, want: Deny, static: Deny, why: "/dev/tcp/127.0.0.1/80 (1:5): opens a network connection"},
		{cmd: `echo x > /dev/udp/127.0.0.1/53`, want: Deny, static: Deny},
		{cmd: `cat /dev/tcp/127.0.0.1/80`, want: Deny, static: Deny},
		{cmd: `cat 0<>/dev/tcp/127.0.0.1/80`, want: Deny, static: Deny},
		{cmd: `echo /dev/tcp/x; cat /dev/null`, want: Allow, static: Allow},
	})
	opt, _ := escapeSession(t)
	err := Readonly().CheckOpen("/dev/tcp/127.0.0.1/80", opt.WorkingDir, false, opt.Roots, &Grant{ReadPaths: []string{"/"}})
	assert.ErrorContains(t, err, "network connection", "no grant opens a network connection")
	err = Readonly().CheckLaunch([]string{"cat", "/dev/udp/127.0.0.1/53"}, opt.WorkingDir, opt.Roots, &Grant{ReadPaths: []string{"/"}})
	assert.ErrorContains(t, err, "network connection")
}

// Found while fixing the review: mapfile's -C callback is code, and set -k
// turns any NAME=value argument into an environment assignment
// (set -k; find . LD_PRELOAD=/tmp/x.so). Bash 5.2 ran both; the jailed shell
// supports neither.
func TestAnalyze_UnsupportedBuiltinOptions(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `mapfile -C 'touch p' -c 1 arr <<< x`, want: Deny, static: Deny, why: "mapfile -C"},
		{cmd: `readarray -tC'touch p' arr <<< x`, want: Deny, static: Deny},
		{cmd: `set -k; find . LD_PRELOAD=/tmp/x.so`, want: Deny, static: Deny, why: "set -k"},
		{cmd: `set -euk`, want: Deny, static: Deny},
		{cmd: `set -euo keyword`, want: Deny, static: Deny, why: "set -o keyword"},
		{cmd: `set -o "$o"`, want: Allow, static: Deny},
		{cmd: `set -euo pipefail; set -x; set +k; set -- -k; mapfile -t -c 1 arr < /dev/null`, want: Allow, static: Allow},
	})
}

// Shapes around the review fixes: arithmetic forms, quoting and loops.
func TestAnalyze_ReviewEdgeShapes(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `for ((i=0, j=0; i<j; i++)); do echo $(( (j) + "$i" )); done`, want: Allow, static: Allow},
		{cmd: `(( (k = 2) )); echo $(( k ))`, want: Allow, static: Allow},
		{cmd: `x=; y=( ); echo $(( x + y ))`, want: Allow, static: Allow},
		{cmd: `[[ a == b && 1 -lt 2 ]]`, want: Allow, static: Allow},
		{cmd: `echo $(( "$x$y" ))`, want: Allow, static: Deny},
		{cmd: `echo $(( ${x:-1} ))`, want: Allow, static: Deny},
		{cmd: `echo $(( 'q' ))`, want: Allow, static: Deny},
		{cmd: `select n in 1 2; do echo $(( n )); done`, want: Allow, static: Deny},
		{cmd: `for n; do echo $(( n )); done`, want: Allow, static: Deny},
		{cmd: `eval 'n=1; echo $(( n ))'`, want: Allow, static: Deny},
		{cmd: `n=1 & echo $(( n ))`, want: Allow, static: Deny},
		{cmd: `declare -n m=k; m=1; k=2; echo $(( k ))`, want: Allow, static: Deny},
		{cmd: `a=(1 x); echo $(( a[0] ))`, want: Allow, static: Deny},
		{cmd: `unset -v a; unset -- b; unset -fv c`, want: Allow, static: Allow},
		{cmd: `echo [a"]" [a']' [$x] "[" a[ x]`, want: Allow, static: Allow},
		{cmd: `[a"]"b`, want: Allow, static: Deny},
		{cmd: `trap`, want: Allow, static: Allow},
		{cmd: `mapfile -- arr < /dev/null; mapfile "$o" arr < /dev/null`, want: Ask, static: Deny},
		{cmd: `set -o; set -o "$x"`, want: Allow, static: Deny},
	})
}

// More directories than the analyzer follows are refused under static. One
// cd target reaches them all in a nested tree, so the limit on cd targets
// (TestAnalyze_TooManyCdTargets) does not stop the command first.
func TestAnalyze_TooManyDirectories(t *testing.T) {
	opt, _ := escapeSession(t)
	d := opt.WorkingDir
	for i := 0; i < maxDirs+2; i++ {
		d = filepath.Join(d, "d")
		require.NoError(t, os.Mkdir(d, 0o700))
	}
	cmd := "cd d; ls"
	v := Readonly().Analyze(cmd, opt)
	assert.Equal(t, Allow, v.Kind, "runtime: the runner checks each launch: %s", v.Reason())
	opt.Static = true
	v = Readonly().Analyze(cmd, opt)
	assert.Equal(t, Deny, v.Kind)
	assert.Contains(t, v.Reason(), "more directories than static enforcement follows")
}

// The tests below hold the commands from the second PR #430 review
// (Josh-Schoen, R1-R6, at 256b705f). Each unsafe command did the harmful
// thing in bash 5.2 (or dash, for R3) while the analyzer allowed it.

// R1: ${x@P} prompt-expands the value, and prompt expansion runs the command
// substitutions in it. Static enforcement refuses the @P operator. The
// jailed shell does not prompt-expand (TestInterpreter_PromptExpansionRunsNothing),
// so runtime gives no verdict. The other @ operators run nothing.
func TestAnalyze_PromptExpansion(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `x='$(touch p)'; echo "${x@P}"`, want: Allow, static: Deny, why: "${x@P} (1:"},
		{cmd: `x='$(cat ../o/secret.txt)'; echo "${x@P}"`, want: Allow, static: Deny, why: "prompt expansion"},
		{cmd: `a=('$(touch p)'); echo ${a[@]@P}`, want: Allow, static: Deny},
		{cmd: `eval 'echo ${x@P}'`, want: Allow, static: Deny},
		{cmd: `x='$(touch p)'; echo "${x}" "${x@Q}" "${x@E}" "${x@U}" "${x@A}" "${x@a}"`, want: Allow, static: Allow},
	})
}

// R1 under runtime: the jailed shell (mvdan.cc/sh's interpreter) leaves the
// value of ${x@P} as it is and starts nothing. An interpreter upgrade that
// implements prompt expansion fails here, and the analyzer must then refuse
// @P under runtime too.
func TestInterpreter_PromptExpansionRunsNothing(t *testing.T) {
	out, launches := runInterpreter(t, `x='$(touch p)'; echo "${x@P}"`)
	assert.Equal(t, "$(touch p)\n", out, "the interpreter prints the value unexpanded")
	assert.Empty(t, launches, "the interpreter starts no program for ${x@P}")
}

// R2: wait -p NAME (bash 5.1) assigns NAME, and a subscript in it is
// evaluated as arithmetic. Static enforcement judges the name like printf -v.
// The jailed shell refuses wait -p (TestInterpreter_WaitPRefused).
func TestAnalyze_WaitP(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `sleep 0 & wait -n -p 'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `wait -np 'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `wait -p'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `wait -fnp 'a[$(touch p)]' 1`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `wait "$o" 'a[$(touch p)]'`, want: Allow, static: Deny, why: "subscript"},
		{cmd: `wait "$o" "$v"`, want: Allow, static: Deny, why: "computed at run time"},
		{cmd: `wait -p "$v"`, want: Ask, static: Deny, why: "computed at run time"},
		{cmd: `wait -p PATH`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `sleep 0 & wait -n -p pid; wait "$!"; wait -- 1 'a[$(touch p)]'; wait`, want: Allow, static: Allow},
	})
}

// R2 under runtime: the jailed shell refuses wait -p, so no name is assigned.
func TestInterpreter_WaitPRefused(t *testing.T) {
	out, launches := runInterpreter(t, `wait -p 'a[$(touch p)]' 2>&1`)
	assert.Contains(t, out, "unsupported option", "the interpreter refuses wait -p")
	assert.Empty(t, launches, "the interpreter starts no program for wait -p")
}

// runInterpreter runs src in mvdan.cc/sh's interpreter with an exec handler
// that records each program launch and starts none. It returns the output
// and the launches.
func runInterpreter(t *testing.T, src string) (string, [][]string) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	require.NoError(t, err, "parse %q", src)
	var out bytes.Buffer
	var launches [][]string
	r, err := interp.New(interp.StdIO(strings.NewReader(""), &out, &out), interp.Dir(t.TempDir()),
		interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(_ context.Context, args []string) error {
				launches = append(launches, args)
				return interp.ExitStatus(127)
			}
		}))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Run(ctx, file)
	return out.String(), launches
}

// R4: --files0-from reads the names of the files to read from a file, which
// path confinement cannot see. sort, wc and du refuse it, in any spelling.
func TestAnalyze_Files0From(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `printf '../o/secret.txt\0' > l; sort --files0-from=l`, want: Ask, static: Ask, why: "sort (1:33): --files0-from is outside the profile"},
		{cmd: `sort --files0-from l`, want: Ask, static: Ask, why: "--files0-from"},
		{cmd: `sort --files0=l`, want: Ask, static: Ask, why: "--files0-from"},
		{cmd: `wc --files0-from=l`, want: Ask, static: Ask, why: "wc (1:1): --files0-from"},
		{cmd: `wc -c --files0-from l`, want: Ask, static: Ask},
		{cmd: `du --files0-from=l`, want: Ask, static: Ask, why: "du (1:1): --files0-from"},
		{cmd: `du -sh --files0 l`, want: Ask, static: Ask},
		{cmd: `sort -k2 a.txt; wc -l a.txt; du -sh .; sort -- --files0-from=l`, want: Allow, static: Allow},
	})
	opt, _ := escapeSession(t)
	for _, argv := range [][]string{
		{"sort", "--files0-from=l"}, {"wc", "--files0-from", "l"}, {"du", "--files0=l"},
	} {
		err := Readonly().CheckLaunch(argv, opt.WorkingDir, opt.Roots, nil)
		assert.ErrorContains(t, err, "--files0-from is outside the profile", "%v at run time", argv)
	}
}

// R5: grep -R (--dereference-recursive) follows every symlink it meets, so a
// symlink in the session leads it out of the roots. grep -r follows only the
// symlinks named on the command line, which path confinement checks.
func TestAnalyze_GrepDereferenceRecursive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `grep -R x .`, want: Ask, static: Ask, why: "grep (1:1): -R is outside the profile"},
		{cmd: `grep -Rn x .`, want: Ask, static: Ask, why: "-R is outside the profile"},
		{cmd: `grep -inR x .`, want: Ask, static: Ask},
		{cmd: `grep --dereference-recursive x .`, want: Ask, static: Ask, why: "--dereference-recursive"},
		{cmd: `grep --dereference x .`, want: Ask, static: Ask, why: "--dereference-recursive"},
		{cmd: `egrep -R x .; fgrep -R x .`, want: Ask, static: Ask},
		{cmd: `grep -r x .; grep -rn x .; grep -eR a.txt; grep -AR x a.txt; grep -- -R a.txt`, want: Allow, static: Allow},
	})
	opt, _ := escapeSession(t)
	err := Readonly().CheckLaunch([]string{"grep", "-Rn", "x", "."}, opt.WorkingDir, opt.Roots, nil)
	assert.ErrorContains(t, err, "-R is outside the profile", "grep -R at run time")
}

// R6: the time to follow cd targets grows with their number, and the hook
// runs before shell_execute's 40,000-character limit. Past maxCdTargets
// literal cd or pushd targets the command is refused in both modes, before
// any of them is followed.
func TestAnalyze_TooManyCdTargets(t *testing.T) {
	opt, _ := escapeSession(t)
	cmd := strings.Repeat("cd ..;", 40000/len("cd ..;"))
	pol := Readonly()
	for _, static := range []bool{false, true} {
		o := opt
		o.Static = static
		start := time.Now()
		v := pol.Analyze(cmd, o)
		t.Logf("static=%v: %d cd targets judged in %v", static, 40000/len("cd ..;"), time.Since(start))
		assert.Equal(t, Deny, v.Kind, "static=%v: %s", static, v.Reason())
		assert.Contains(t, v.Reason(), "more than "+itoa(maxCdTargets)+" cd or pushd targets", "static=%v", static)
	}
	within := strings.Repeat("cd sub; ", maxCdTargets) + "ls"
	for _, static := range []bool{false, true} {
		o := opt
		o.Static = static
		v := pol.Analyze(within, o)
		assert.Equal(t, Allow, v.Kind, "static=%v: %d cd targets are followed: %s", static, maxCdTargets, v.Reason())
	}
}

// R6, found while fixing it: with at most maxCdTargets targets the time
// still grew with the depth of the tree, because workingDirs checked every
// directory against every target on each pass and ResolvePath walked the
// whole prefix again for each component. 64 targets in a tree 80 levels
// deep did not finish in 120 s under -race; each directory is now followed
// through each target once, and ResolvePath is linear in the depth. The
// bound is generous so a slow CI host does not fail it.
func TestAnalyze_CdFollowingIsBounded(t *testing.T) {
	opt, _ := escapeSession(t)
	d := opt.WorkingDir
	for i := 0; i < 80; i++ {
		d = filepath.Join(d, "d")
		require.NoError(t, os.Mkdir(d, 0o700))
	}
	var b strings.Builder
	b.WriteString("cd d; cd ..; ")
	for i := 0; i < maxCdTargets-2; i++ {
		b.WriteString("cd x" + itoa(i) + "; ")
	}
	b.WriteString("cat a.txt")
	for _, static := range []bool{false, true} {
		o := opt
		o.Static = static
		start := time.Now()
		v := Readonly().Analyze(b.String(), o)
		elapsed := time.Since(start)
		t.Logf("static=%v: %v in %v", static, v.Kind, elapsed)
		assert.Less(t, elapsed, 30*time.Second, "static=%v: following %d cd targets is bounded", static, maxCdTargets)
		want := Ask // cd .. reaches the parent of the session
		if static {
			want = Deny // more than maxDirs directories
		}
		assert.Equal(t, want, v.Kind, "static=%v: %s", static, v.Reason())
	}
}

// ResolvePath resolves a symlink-free prefix with one Lstat per component.
// It returns what resolving each component with filepath.EvalSymlinks
// returns, for symlinks that are relative, absolute, chained, to a file,
// dangling or looping, and for a .. after a symlink.
func TestResolvePath_MatchesPerComponentEvalSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	reference := func(dir, path string) string {
		if !filepath.IsAbs(path) {
			path = dir + "/" + path
		}
		cur := "/"
		comps := strings.Split(path, "/")
		for i, c := range comps {
			switch c {
			case "", ".":
				continue
			case "..":
				cur = filepath.Dir(cur)
				continue
			}
			resolved, err := filepath.EvalSymlinks(filepath.Join(cur, c))
			if err != nil {
				return filepath.Join(cur, filepath.Join(comps[i:]...))
			}
			cur = resolved
		}
		return cur
	}
	opt, base := escapeSession(t)
	w := opt.WorkingDir
	require.NoError(t, os.MkdirAll(filepath.Join(w, "a", "b"), 0o700))
	require.NoError(t, os.Symlink("a/b", filepath.Join(w, "rel")))
	require.NoError(t, os.Symlink(filepath.Join(base, "o"), filepath.Join(w, "abs")))
	require.NoError(t, os.Symlink("rel", filepath.Join(w, "chain")))
	require.NoError(t, os.Symlink("victim.txt", filepath.Join(w, "file")))
	require.NoError(t, os.Symlink("missing", filepath.Join(w, "dangling")))
	require.NoError(t, os.Symlink("loop", filepath.Join(w, "loop")))
	for _, p := range []string{
		"a/b", "rel", "rel/..", "rel/../..", "chain/x", "abs/secret.txt", "abs/../w/a",
		"link/k", "link/../secret.txt", "file", "file/x", "dangling", "dangling/x", "loop", "loop/x",
		"missing/x/y", "./a/./b/", "../../..", "/", base + "/o/ssh",
	} {
		assert.Equal(t, reference(w, p), ResolvePath(w, p), "ResolvePath(w, %q)", p)
	}
}

// Follow-ups to R5: other programs that follow symlinks out of the roots
// while they walk a directory. In the escapeSession layout w/link points to
// o/ssh, outside the session. Each was checked on macOS's BSD tools:
// du -aL, ls -RL, find -L and find -LE listed o/ssh's names, grep -rS
// printed the secret, and diff printed it when given a directory that holds
// a symlink to it.

// launchCase is one launch for runLaunchCases: block is a substring of the
// reason, or "" for an allowed launch.
type launchCase struct {
	argv  []string
	block string
}

// runLaunchCases checks each argv at run time in the escapeSession layout.
func runLaunchCases(t *testing.T, cases []launchCase) {
	t.Helper()
	opt, _ := escapeSession(t)
	for _, tc := range cases {
		err := Readonly().CheckLaunch(tc.argv, opt.WorkingDir, opt.Roots, nil)
		if tc.block == "" {
			assert.NoError(t, err, "%v at run time", tc.argv)
			continue
		}
		assert.ErrorContains(t, err, tc.block, "%v at run time", tc.argv)
	}
}

// du -L (--dereference) follows every symlink; -H, -D and
// --dereference-args follow only the operands, which are path-checked.
func TestAnalyze_DuDereference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `du -L .`, want: Ask, static: Ask, why: "du (1:1): -L is outside the profile"},
		{cmd: `du -aL .`, want: Ask, static: Ask, why: "-L is outside the profile"},
		{cmd: `du -sLh .`, want: Ask, static: Ask},
		{cmd: `du --dereference .`, want: Ask, static: Ask, why: "--dereference is outside the profile"},
		{cmd: `du --dereference=x .`, want: Ask, static: Ask},
		{cmd: `du -a .; du -sh .; du -H .; du -D .; du --dereference-args .; du -d1 .; du -- -L`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"du", "-aL", "."}, block: "-L is outside the profile"},
		{argv: []string{"du", "--dereference", "."}, block: "--dereference"},
		{argv: []string{"du", "--dereference-args", "."}},
	})
}

// ls -L with -R (--recursive) lists the directories symlinks point to,
// recursively. Each alone follows nothing out of the roots: -R does not
// follow symlinks, and -L alone lists one level.
func TestAnalyze_LsRecursiveDereference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `ls -RL .`, want: Ask, static: Ask, why: "ls (1:1): ls -L with -R lists the directories symlinks point to"},
		{cmd: `ls -LR`, want: Ask, static: Ask},
		{cmd: `ls -R -L .`, want: Ask, static: Ask},
		{cmd: `ls -laRL`, want: Ask, static: Ask},
		{cmd: `ls --recursive --dereference .`, want: Ask, static: Ask},
		{cmd: `ls -R --dereference .`, want: Ask, static: Ask},
		{cmd: `ls --rec -L`, want: Ask, static: Ask},
		{cmd: `ls --deref -R`, want: Ask, static: Ask},
		{cmd: `ls -R .; ls -L .; ls -lL; ls -RH .; ls -R --dereference-command-line .; ls -R -- -L`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"ls", "-RL", "."}, block: "ls -L with -R"},
		{argv: []string{"ls", "--recursive", "--dereference"}, block: "ls -L with -R"},
		{argv: []string{"ls", "-R", "."}},
	})
}

// find -L and -follow follow every symlink, and BSD find takes its options
// in a group (-LE, -xL). find -H follows only the starting points, which
// path confinement resolves, so it stays allowed: find -H link asks because
// link itself leads out of the session.
func TestAnalyze_FindFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `find -L .`, want: Ask, static: Ask, why: "find (1:1): find -L is outside the profile"},
		{cmd: `find -LE .`, want: Ask, static: Ask, why: "find -LE is outside the profile"},
		{cmd: `find -xL . -name k`, want: Ask, static: Ask},
		{cmd: `find . -follow -name k`, want: Ask, static: Ask, why: "-follow is outside the profile"},
		{cmd: `find -H link`, want: Ask, static: Ask, why: "link (1:1): reads outside the read roots"},
		{cmd: `find . -name '*.txt' -type f; find -H .; find -P .; find -x . -ls; find . -links 1; find -E . -regex 'a.*'`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"find", "-L", "."}, block: "find -L is outside the profile"},
		{argv: []string{"find", "-xL", "."}, block: "find -xL is outside the profile"},
		{argv: []string{"find", ".", "-follow"}, block: "-follow"},
		{argv: []string{"find", "-H", "."}},
	})
}

// diff compares the files in a directory operand (and, with -r, in its
// subdirectories) and follows the symlinks it meets, so a symlink in the
// session leads it out. A directory operand is an Ask, whether or not -r is
// given; diff over files stays allowed.
func TestAnalyze_DiffDirectoryOperand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `diff sub sub`, want: Ask, static: Ask, why: "sub (1:1): diff compares the files in a directory"},
		{cmd: `diff -r . sub`, want: Ask, static: Ask, why: "diff compares the files in a directory"},
		{cmd: `diff -ru a.txt sub`, want: Ask, static: Ask},
		{cmd: `diff --recursive --from-file=sub a.txt`, want: Ask, static: Ask},
		{cmd: `cd sub; diff ../a.txt ..`, want: Ask, static: Ask},
		{cmd: `diff s* a.txt`, want: Allow, static: Ask, why: "diff compares the files in a directory"},
		{cmd: `diff a.txt victim.txt; diff -u a.txt - < victim.txt; diff -r a.txt victim.txt; diff missing a.txt`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"diff", "sub", "sub"}, block: "diff compares the files in a directory"},
		{argv: []string{"diff", "-r", "a.txt", "./sub/"}, block: "diff compares the files in a directory"},
		{argv: []string{"diff", "--to-file=sub", "a.txt"}, block: "diff compares the files in a directory"},
		{argv: []string{"diff", "-r", "a.txt", "victim.txt"}},
	})
	opt, _ := escapeSession(t)
	err := Readonly().CheckLaunch([]string{"diff", "sub", "sub"}, opt.WorkingDir, opt.Roots, &Grant{Programs: []string{"diff"}})
	assert.NoError(t, err, "an approved diff runs with any operands")
}

// BSD grep -S follows every symlink while it recurses (macOS's grep -rS
// printed the secret). GNU grep has no -S.
func TestAnalyze_GrepFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `grep -rS x .`, want: Ask, static: Ask, why: "grep (1:1): -S is outside the profile"},
		{cmd: `grep -S -r x .`, want: Ask, static: Ask},
		{cmd: `egrep -rnS x .`, want: Ask, static: Ask},
		{cmd: `grep -rs x .; grep -rO x .; grep -rp x .; grep -eS a.txt`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"grep", "-rS", "x", "."}, block: "-S is outside the profile"},
	})
}

// Follow-up to R4: GNU find -files0-from F (findutils 4.9) reads its
// starting points from F, which path confinement never sees, as sort
// --files0-from did. BSD find has no such option.
func TestAnalyze_FindFiles0From(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `printf '../o\0' > l; find -files0-from l -name secret.txt`, want: Ask, static: Ask, why: "find (1:22): -files0-from is outside the profile"},
		{cmd: `find -files0-from l`, want: Ask, static: Ask},
		{cmd: `find . -name files0-from`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"find", "-files0-from", "l"}, block: "-files0-from is outside the profile"},
	})
}

// The tests below hold the findings from the third PR #430 review
// (Josh-Schoen, F1-F4, at 76bce862).

// gitSession is escapeSession plus a repository: w/.git with info/ and a
// config, a bare repository w/meta (no .git in its name: git knows it by
// HEAD, objects/ and refs/), and symlinks w/cfg to .git/config and w/gd to
// .git.
func gitSession(t *testing.T) Options {
	t.Helper()
	opt, _ := escapeSession(t)
	w := opt.WorkingDir
	for _, d := range []string{".git/info", ".git/objects", ".git/refs", "meta/objects", "meta/refs"} {
		require.NoError(t, os.MkdirAll(filepath.Join(w, d), 0o700))
	}
	for _, f := range []string{".git/config", ".git/HEAD", "meta/HEAD", "meta/config"} {
		require.NoError(t, os.WriteFile(filepath.Join(w, f), []byte("x\n"), 0o600))
	}
	require.NoError(t, os.Symlink(filepath.Join(w, ".git", "config"), filepath.Join(w, "cfg")))
	require.NoError(t, os.Symlink(".git", filepath.Join(w, "gd")))
	return opt
}

// F1: a file the command writes into a git directory changes what git runs:
// .git/info/attributes and core.attributesFile name a filter, .git/config
// defines it, and gitHarden's --attr-source replaces only the worktree's
// attributes. git 2.50.1 with gitHarden's prefix ran a clean filter from
// .git/info/attributes. A write into a git directory is an Ask in both
// modes, granting that path; the run-time CheckOpen refuses it unless the
// path was granted. git's own writes (the index) are not the command's.
func TestAnalyze_WriteIntoGitDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt := gitSession(t)
	w := opt.WorkingDir
	pol := Readonly()
	for _, tc := range []struct {
		cmd  string
		want VerdictKind
		why  string
	}{
		{cmd: `printf '* filter=x\n' > .git/info/attributes`, want: Ask, why: ".git/info/attributes (1:23): writes into a git directory"},
		{cmd: `printf '[filter "x"]\n\tclean = touch pwned\n' >> .git/config`, want: Ask, why: "writes into a git directory"},
		{cmd: `echo x > sub/../.git/config`, want: Ask},
		{cmd: `echo x > cfg`, want: Ask},
		{cmd: `echo x > gd/info/attributes`, want: Ask},
		{cmd: `echo x > .git/new-file`, want: Ask},
		{cmd: `echo x > meta/config`, want: Ask, why: "meta/config (1:8): writes into a git directory"},
		{cmd: `cd .git && echo x > config`, want: Ask},
		{cmd: `echo x > .git`, want: Ask},
		{cmd: `echo x > .gitattributes; echo x > .github; echo x > sub/x.git; echo x > a.txt; cat .git/config`, want: Allow},
	} {
		for _, static := range []bool{false, true} {
			o := opt
			o.Static = static
			v := pol.Analyze(tc.cmd, o)
			if !assert.Equal(t, tc.want, v.Kind, "%q (static=%v): %s", tc.cmd, static, v.Reason()) {
				continue
			}
			if tc.why != "" {
				assert.Contains(t, v.Reason(), tc.why, "%q (static=%v)", tc.cmd, static)
			}
			if v.Kind == Ask {
				require.NotNil(t, v.Grant, "%q: an Ask carries a grant", tc.cmd)
				assert.NotEmpty(t, v.Grant.WritePaths, "%q: the grant names the path written", tc.cmd)
			}
		}
	}
	v := pol.Analyze(`echo x > .git/info/attributes`, opt)
	require.NotNil(t, v.Grant)
	assert.Equal(t, []string{filepath.Join(w, ".git", "info", "attributes")}, v.Grant.WritePaths, "the grant is the resolved path")

	for _, tc := range []struct {
		path  string
		grant *Grant
		block string
	}{
		{path: ".git/info/attributes", block: "writes into a git directory"},
		{path: "cfg", block: "writes into a git directory"},
		{path: "meta/config", block: "writes into a git directory"},
		{path: ".git/info/attributes", grant: &Grant{WritePaths: []string{filepath.Join(w, ".git", "info", "attributes")}}},
		{path: ".git/config", grant: &Grant{WritePaths: []string{filepath.Join(w, ".git", "info", "attributes")}}, block: "writes into a git directory"},
		{path: ".git/config", grant: &Grant{ReadPaths: []string{w}}, block: "writes into a git directory"},
		{path: "a.txt"},
		{path: ".gitattributes"},
	} {
		err := pol.CheckOpen(tc.path, w, true, opt.Roots, tc.grant)
		if tc.block == "" {
			assert.NoError(t, err, "write %s (grant %+v)", tc.path, tc.grant)
			continue
		}
		assert.ErrorContains(t, err, tc.block, "write %s (grant %+v)", tc.path, tc.grant)
	}
	assert.NoError(t, pol.CheckOpen(".git/config", w, false, opt.Roots, nil), "reading .git/config is not a write")
	merged := MergeGrants([]any{&Grant{WritePaths: []string{"/a"}}, &Grant{WritePaths: []string{"/a", "/b"}}})
	assert.Equal(t, []string{"/a", "/b"}, merged.WritePaths, "MergeGrants keeps each write path once")
}

// F2: file -f F (--files-from F) reads the names of the files to check from
// F, which path confinement never sees, as sort --files0-from did (R4).
// file 5.41 named an SSH key and a database outside the session.
func TestAnalyze_FileFilesFrom(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `printf '../o/ssh/k\n' > l; file -f l`, want: Ask, static: Ask, why: "file (1:28): -f is outside the profile"},
		{cmd: `file -bf l`, want: Ask, static: Ask},
		{cmd: `file -fl`, want: Ask, static: Ask},
		{cmd: `file --files-from l`, want: Ask, static: Ask, why: "--files-from is outside the profile"},
		{cmd: `file --files-from=l`, want: Ask, static: Ask},
		{cmd: `file --files=l`, want: Ask, static: Ask},
		{cmd: `file a.txt; file -b a.txt; file -m a.txt victim.txt; file -mf victim.txt; file -- -f`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"file", "-f", "l"}, block: "-f is outside the profile"},
		{argv: []string{"file", "--files-from=l"}, block: "--files-from"},
	})
}

// F3: jq's import and include load a module by path from the program text,
// and modulemeta reads one, so a session symlink leads them out of the
// roots (jq refuses .. itself). A program file (-f, --from-file) can hold
// them too. Each is an Ask.
func TestAnalyze_JqModules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `jq -n 'import "link/k" as $s; $s'`, want: Ask, static: Ask, why: "jq (1:1): jq import, include and modulemeta load modules by path"},
		{cmd: `jq -n 'include "link/k"; .'`, want: Ask, static: Ask},
		{cmd: `jq -n 'import # comment
"link/k" as $s; $s'`, want: Ask, static: Ask},
		{cmd: `jq -n --arg m lo/secret '$m | modulemeta'`, want: Ask, static: Ask},
		{cmd: `jq -f prog.jq a.txt`, want: Ask, static: Ask, why: "-f is outside the profile"},
		{cmd: `jq -nf prog.jq`, want: Ask, static: Ask},
		{cmd: `jq --from-file prog.jq a.txt`, want: Ask, static: Ask, why: "--from-file is outside the profile"},
		{cmd: `jq . a.txt; jq -r '.include, .imports, ."import"' a.txt; jq -n '"import"'; jq -L sub . a.txt`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"jq", "-n", `import "link/k" as $s; $s`}, block: "jq import, include and modulemeta"},
		{argv: []string{"jq", "-f", "prog.jq"}, block: "-f is outside the profile"},
		{argv: []string{"jq", ".", "a.txt"}},
	})
}

// jqUsesModule reads module words only where jq reads code.
func TestJqUsesModule(t *testing.T) {
	for src, want := range map[string]bool{
		`import "a" as $a; .`:              true,
		`include "a"; .`:                   true,
		`import#c` + "\n" + `"a" as $a; .`: true,
		`"x" | modulemeta`:                 true,
		`"\("a" | modulemeta)"`:            true,
		`"\\" | modulemeta`:                true,
		`"\("\(1)") \(include)"`:           true,
		`"import"`:                         false,
		`.include, .imports, ."import"`:    false,
		`$import, $__loc__`:                false,
		`# import "a"` + "\n" + `.`:        false,
		`"\"import\""`:                     false,
		`{"include": 1} | .modulemeta`:     false,
		`importx, xinclude, modulemeta2`:   false,
	} {
		assert.Equal(t, want, jqUsesModule(src), "jqUsesModule(%q)", src)
	}
}

// F4: md5sum, sha1sum, sha256sum and shasum -c (--check) read the names to
// check from a sums file, which path confinement never sees: the output
// tells whether a file outside the session exists and matches a hash.
// BSD md5 -c compares against a string, not a file, so md5 keeps it.
func TestAnalyze_ChecksumCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	runBypassCases(t, []bypassCase{
		{cmd: `md5sum -c sums`, want: Ask, static: Ask, why: "md5sum (1:1): -c is outside the profile"},
		{cmd: `sha1sum --check sums`, want: Ask, static: Ask, why: "--check is outside the profile"},
		{cmd: `sha256sum --check=sums`, want: Ask, static: Ask},
		{cmd: `sha256sum -wc sums`, want: Ask, static: Ask},
		{cmd: `shasum -a 256 -c sums`, want: Ask, static: Ask},
		{cmd: `shasum --che sums`, want: Ask, static: Ask},
		{cmd: `md5sum a.txt; sha256sum -b a.txt; shasum -a 256 a.txt; md5 -c abc a.txt; sha1sum -- -c`, want: Allow, static: Allow},
	})
	runLaunchCases(t, []launchCase{
		{argv: []string{"sha256sum", "-c", "sums"}, block: "-c is outside the profile"},
		{argv: []string{"shasum", "--check", "sums"}, block: "--check"},
	})
}

// The tests below hold the findings from the fourth PR #430 review
// (Josh-Schoen, F5-F7, at 27af16a2).

// F5: a clone can track a bare repository (HEAD, objects/, refs/, an index
// and a config with core.worktree=.. and a filter). git -C evil diff, or git
// run inside it, uses that repository and runs its filter: git 2.50.1 did,
// with gitHarden's prefix. gitHarden sets safe.bareRepository=explicit
// (git 2.38 and later), and since older git ignores that key, the policy
// also asks when git would discover a bare repository from the directory it
// runs in. A normal repository and its .git directory stay allowed.
func TestAnalyze_GitBareRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt := gitSession(t)
	w := opt.WorkingDir
	pol := Readonly()
	for _, tc := range []struct {
		cmd  string
		want VerdictKind // runtime; static denies git
		why  string
	}{
		{cmd: `git -C meta diff`, want: Ask, why: "git (1:1): git would use the bare repository"},
		{cmd: `cd meta && git status`, want: Ask, why: "git would use the bare repository"},
		{cmd: `cd meta/refs && git diff`, want: Ask},
		{cmd: `git -C sub -C ../meta log`, want: Ask},
		{cmd: `git status; git -C sub diff; cd .git && git log; git -C .git/refs log`, want: Allow},
	} {
		v := pol.Analyze(tc.cmd, opt)
		if assert.Equal(t, tc.want, v.Kind, "%q (runtime): %s", tc.cmd, v.Reason()) && tc.why != "" {
			assert.Contains(t, v.Reason(), tc.why, "%q (runtime)", tc.cmd)
		}
		o := opt
		o.Static = true
		assert.Equal(t, Deny, pol.Analyze(tc.cmd, o).Kind, "%q (static): git needs the runner's hardening", tc.cmd)
	}
	for _, tc := range []struct {
		argv  []string
		dir   string
		grant *Grant
		block string
	}{
		{argv: []string{"git", "-C", "meta", "diff"}, dir: w, block: "git would use the bare repository " + filepath.Join(w, "meta")},
		{argv: []string{"git", "status"}, dir: filepath.Join(w, "meta"), block: "bare repository"},
		{argv: []string{"git", "diff"}, dir: filepath.Join(w, "meta", "refs"), block: "bare repository"},
		{argv: []string{"git", "-C", "meta", "diff"}, dir: w, grant: &Grant{Programs: []string{"git"}}},
		{argv: []string{"git", "status"}, dir: w},
		{argv: []string{"git", "log"}, dir: filepath.Join(w, ".git")},
		{argv: []string{"git", "diff"}, dir: filepath.Join(w, "sub")},
	} {
		err := pol.CheckLaunch(tc.argv, tc.dir, opt.Roots, tc.grant)
		if tc.block == "" {
			assert.NoError(t, err, "%v in %s", tc.argv, tc.dir)
			continue
		}
		assert.ErrorContains(t, err, tc.block, "%v in %s", tc.argv, tc.dir)
	}
}

// F6: > and >> follow a symlink to a target that does not exist yet and
// create it, so a write is judged at the target, not at the symlink. A
// symlink to a new file outside the session is a Deny, and one into a git
// directory is an Ask (dc -> .git/commondir, then git diff, ran a filter).
func TestAnalyze_WriteThroughDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics; the policy refuses every call on Windows")
	}
	opt := gitSession(t)
	w := opt.WorkingDir
	for link, target := range map[string]string{
		"ol": "../o/new.txt",            // outside, not there yet
		"dl": ".git/info/attributes",    // into the git directory, not there yet
		"dc": ".git/commondir",          // the review's route
		"c1": "c2",                      // a chain of two
		"c2": ".git/x",                  //
		"rl": "link/../new.txt",         // link is o/ssh, so this is o/new.txt
		"al": filepath.Join(w, "n.txt"), // absolute, inside, not there yet
		"nl": "new.txt",                 // inside, not there yet
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(w, link)))
	}
	pol := Readonly()
	for _, tc := range []struct {
		cmd  string
		want VerdictKind
		why  string
	}{
		{cmd: `echo x > ol`, want: Deny, why: "ol (1:8): writes outside the write roots"},
		{cmd: `echo x >> rl`, want: Deny},
		{cmd: `printf '../cm\n' > dc`, want: Ask, why: "dc (1:18): writes into a git directory"},
		{cmd: `echo x > dl`, want: Ask},
		{cmd: `echo x > c1`, want: Ask},
		{cmd: `echo x > nl; echo x >> al; cat ol`, want: Allow},
	} {
		for _, static := range []bool{false, true} {
			o := opt
			o.Static = static
			v := pol.Analyze(tc.cmd, o)
			if assert.Equal(t, tc.want, v.Kind, "%q (static=%v): %s", tc.cmd, static, v.Reason()) && tc.why != "" {
				assert.Contains(t, v.Reason(), tc.why, "%q (static=%v)", tc.cmd, static)
			}
		}
	}
	v := pol.Analyze(`echo x > dc`, opt)
	require.NotNil(t, v.Grant)
	assert.Equal(t, []string{filepath.Join(w, ".git", "commondir")}, v.Grant.WritePaths, "the grant is the symlink's target")
	for _, tc := range []struct {
		path  string
		block string
	}{
		{path: "ol", block: "writes outside the write roots"},
		{path: "rl", block: "writes outside the write roots"},
		{path: "dc", block: "writes into a git directory"},
		{path: "c1", block: "writes into a git directory"},
		{path: "nl"},
		{path: "al"},
	} {
		err := pol.CheckOpen(tc.path, w, true, opt.Roots, nil)
		if tc.block == "" {
			assert.NoError(t, err, "write %s", tc.path)
			continue
		}
		assert.ErrorContains(t, err, tc.block, "write %s", tc.path)
	}
}

// F7: in a partial clone missing a blob, git show, log -p, diff and blame
// fetch it from the promisor remote, and the repository's config picks the
// transport: core.sshCommand, or an ext:: URL with protocol.ext.allow. git
// 2.50.1 ran core.sshCommand under gitHarden's prefix. GIT_NO_LAZY_FETCH=1
// (git 2.44) stops the fetch; for older git, every built-in transport is
// set to never on the command line, which overrides the repository's
// protocol.<name>.allow.
func TestGitHarden_NoLazyFetch(t *testing.T) {
	git := Readonly().Programs["git"]
	assert.Contains(t, git.Env, "GIT_NO_LAZY_FETCH=1", "the git profile turns lazy fetch off")
	harden := strings.Join(git.Harden(fields("show HEAD:f")), " ")
	for _, proto := range []string{"ext", "fd", "file", "git", "ssh", "http", "https", "ftp", "ftps"} {
		assert.Contains(t, harden, "-c protocol."+proto+".allow=never", "transport %s is off", proto)
	}
	assert.Contains(t, harden, "-c protocol.allow=never", "every other transport is off")
}
