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

// N1: relative paths after cd resolve against every directory the command
// can change to, and under static a cd the analyzer cannot follow is refused.
func TestAnalyze_ChangedDirectory(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `cd sub && cat ../../o/secret.txt`, want: Ask, static: Ask, why: "../../o/secret.txt (1:11): reads outside the session"},
		{cmd: `pushd sub && cat ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `cat ../../o/secret.txt; cd sub`, want: Ask, static: Ask},
		{cmd: `eval 'cd sub'; cat ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `f() { cd sub; }; f; cat < ../../o/secret.txt`, want: Ask, static: Ask},
		{cmd: `cd sub; cd ..; cd sub; cat ../../o/ssh/k`, want: Ask, static: Ask},
		{cmd: `cd /; echo x > y`, want: Deny, static: Deny, why: "y (1:14): writes outside the session"},
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
	opt, base := escapeSession(t)
	t.Setenv("CDPATH", ":sub:"+base)
	for _, static := range []bool{false, true} {
		opt.Static = static
		v := Readonly().Analyze(`cd o && cat secret.txt`, opt)
		assert.Equal(t, Ask, v.Kind, "static=%v: %s", static, v.Reason())
		assert.Contains(t, v.Reason(), "secret.txt (1:9): reads outside the session")
		assert.Contains(t, v.Reason(), "o (1:4): reads outside the session")
	}
}

// N2: a .. after a glob is refused under static, and every name a glob
// matches is path-checked, so a symlink it matches is followed. A literal ..
// after a symlink leaves the symlink's target, as the kernel opens it.
func TestAnalyze_GlobPaths(t *testing.T) {
	runBypassCases(t, []bypassCase{
		{cmd: `cat */../../o/secret.txt`, want: Allow, static: Deny, why: "a .. after a glob"},
		{cmd: `cat s?b/../../o/secret.txt`, want: Allow, static: Deny},
		{cmd: `cat */k`, want: Allow, static: Ask, why: "*/k (1:5): matches names outside the session"},
		{cmd: `cat l*`, want: Allow, static: Ask},
		{cmd: `cd sub; cat ../l*/k`, want: Ask, static: Ask},
		{cmd: `cat .*/o/secret.txt`, want: Allow, static: Ask},
		{cmd: `cat link/../secret.txt`, want: Ask, static: Ask, why: "reads outside the session"},
		{cmd: `echo x > link/../new.txt`, want: Deny, static: Deny, why: "writes outside the session"},
		{cmd: `cat *.txt sub/* [av]*.txt`, want: Allow, static: Allow},
		{cmd: `[ -f a.txt ] && [ "$x" = y ]`, want: Allow, static: Allow},
	})
}

// A glob that matches too many names, or that bash cannot parse, cannot be
// checked under static.
func TestGlobExpand(t *testing.T) {
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
		{cmd: `file -f../o/secret.txt`, want: Ask, static: Ask, why: "../o/secret.txt (1:1): reads outside the session"},
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
	opt, base := escapeSession(t)
	w := opt.WorkingDir
	secret := filepath.Join(base, "o", "secret.txt")
	pol := Readonly()
	for _, tc := range []struct {
		argv  []string
		block string // a substring of the reason; "" means allowed
	}{
		{argv: []string{"file", "-f" + secret}, block: "outside the session"},
		{argv: []string{"file", "-m../o/secret.txt", "victim.txt"}, block: "outside the session"},
		{argv: []string{"grep", "-f" + secret, "a.txt"}, block: "outside the session"},
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
		{argv: []string{"cat", "link/../secret.txt"}, block: "outside the session"},
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

// More cd targets than the analyzer follows are refused under static.
func TestAnalyze_TooManyDirectories(t *testing.T) {
	opt, _ := escapeSession(t)
	cmd := ""
	for i := 0; i < maxDirs+2; i++ {
		require.NoError(t, os.Mkdir(filepath.Join(opt.WorkingDir, "d"+itoa(i)), 0o700))
		cmd += "cd d" + itoa(i) + "; "
	}
	cmd += "ls"
	v := Readonly().Analyze(cmd, opt)
	assert.Equal(t, Allow, v.Kind, "runtime: the runner checks each launch: %s", v.Reason())
	opt.Static = true
	v = Readonly().Analyze(cmd, opt)
	assert.Equal(t, Deny, v.Kind)
	assert.Contains(t, v.Reason(), "more directories than static enforcement follows")
}
