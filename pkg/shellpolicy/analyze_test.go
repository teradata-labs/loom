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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

// session makes a resolved session directory with a file and a subdirectory,
// and returns analysis options rooted there.
func session(t *testing.T) Options {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o700))
	return Options{WorkingDir: filepath.Join(root, "sub"), Roots: Roots{Read: []string{root}, Write: []string{root}}}
}

// The corpus from design 06 §11 (the hook column), plus the shapes the
// analyzer has to walk into.
func TestAnalyze_Corpus(t *testing.T) {
	opt := session(t)
	pol := Readonly()
	for _, tc := range []struct {
		cmd    string
		want   VerdictKind
		static VerdictKind // the verdict under enforcement: static
		why    string      // a substring of the reason, when not Allow
	}{
		{cmd: `ls -la`, want: Allow, static: Allow},
		{cmd: `ls -la | grep x | wc -l && echo ok || echo no`, want: Allow, static: Allow},
		{cmd: `cat ../notes.txt "../notes.txt" '../notes.txt'`, want: Allow, static: Allow},
		{cmd: `x=rm; $x -rf nope`, want: Allow, static: Deny, why: "computed at run time"},
		{cmd: `$(echo rm) -rf nope`, want: Allow, static: Deny},
		{cmd: "`echo rm` nope", want: Allow, static: Deny},
		{cmd: `eval "rm nope"`, want: Ask, static: Ask, why: "rm (1:1): not on the shell allowlist"},
		{cmd: `eval "$CMD"`, want: Allow, static: Deny},
		{cmd: `exec rm nope`, want: Ask, static: Ask, why: "rm"},
		{cmd: `command rm nope`, want: Ask, static: Ask, why: "rm"},
		{cmd: `command -v rm`, want: Allow, static: Allow},
		{cmd: `rm() { echo fake; }; rm nope`, want: Allow, static: Allow},
		{cmd: `shopt -s expand_aliases; alias ll='rm'; ll x`, want: Ask, static: Deny, why: "shopt"},
		{cmd: `source ./s.sh`, want: Ask, static: Deny, why: "source"},
		{cmd: `trap 'rm nope' EXIT`, want: Ask, static: Ask, why: "rm"},
		{cmd: `(rm nope) &`, want: Ask, static: Ask},
		{cmd: `cat <(rm nope)`, want: Ask, static: Ask},
		{cmd: `/bin/rm nope`, want: Ask, static: Ask, why: "runs a program by path"},
		{cmd: `./rm`, want: Ask, static: Ask},
		{cmd: `\rm x`, want: Ask, static: Ask, why: "rm (1:1)"},
		{cmd: `r''m x`, want: Ask, static: Ask, why: "rm (1:1)"},
		{cmd: `find . -exec rm {} \;`, want: Ask, static: Ask, why: "-exec is outside the profile"},
		{cmd: `find . -delete`, want: Ask, static: Ask},
		{cmd: `find . $X`, want: Allow, static: Allow},
		{cmd: `sort -o out a`, want: Ask, static: Ask},
		{cmd: `sort -ro out a`, want: Ask, static: Ask},
		{cmd: `sort --out=x a`, want: Ask, static: Ask, why: "--output"},
		{cmd: `sort -- -o`, want: Allow, static: Allow},
		{cmd: `uniq a b`, want: Ask, static: Ask, why: "more than 1 file argument"},
		{cmd: `uniq -f 1 a`, want: Allow, static: Allow},
		{cmd: `git -c core.pager='sh -c id' log`, want: Ask, static: Ask, why: "git -c"},
		{cmd: `git log -p`, want: Allow, static: Allow},
		{cmd: `git grep -c x`, want: Allow, static: Allow},
		{cmd: `git --no-pager log --oneline`, want: Allow, static: Allow},
		{cmd: `git branch`, want: Allow, static: Allow},
		{cmd: `git branch -D x`, want: Ask, static: Ask, why: "listing only"},
		{cmd: `git push`, want: Ask, static: Ask, why: "git push"},
		{cmd: `git log --output=x`, want: Ask, static: Ask},
		{cmd: `git -C /usr status`, want: Ask, static: Ask, why: "reads outside the session"},
		{cmd: `echo x > /etc/x`, want: Deny, static: Deny, why: "writes outside the session"},
		{cmd: `echo x >> ../../outside`, want: Deny, static: Deny},
		{cmd: `echo x > ../out.txt; echo y >> out.txt; ls 2>&1 >/dev/null; ls >&2`, want: Allow, static: Allow},
		{cmd: `cat < /etc/hosts`, want: Ask, static: Ask, why: "reads outside the session"},
		{cmd: `cat /etc/hosts`, want: Ask, static: Ask},
		{cmd: `cd /; ls`, want: Ask, static: Ask},
		{cmd: `cd ../..; ls`, want: Ask, static: Ask},
		{cmd: `d=/; cd $d; ls`, want: Allow, static: Allow},
		{cmd: `ls /etc/pass*`, want: Ask, static: Ask},
		{cmd: `p=/etc; ls $p/pass*`, want: Allow, static: Allow},
		{cmd: `for f in *.txt; do wc -l "$f"; done`, want: Allow, static: Allow},
		{cmd: `env`, want: Ask, static: Ask},
		{cmd: `echo $ANTHROPIC_API_KEY`, want: Allow, static: Allow},
		{cmd: `sleep 100 & sleep 100`, want: Allow, static: Allow},
		{cmd: `file -C -m x`, want: Ask, static: Ask},
		{cmd: `base64 -o out in`, want: Ask, static: Ask},
		{cmd: `date +%s; date -u`, want: Allow, static: Allow},
		{cmd: `date -s 10:00`, want: Ask, static: Ask},
		{cmd: `date 202601010000`, want: Ask, static: Ask, why: "would set the clock"},
		{cmd: `coproc cat`, want: Deny, static: Deny, why: "not supported"},
		{cmd: `exec 3>f`, want: Deny, static: Deny, why: "file descriptors above 2"},
		{cmd: `sudo ls`, want: Deny, static: Deny, why: "never allowed"},
		{cmd: `/usr/bin/sudo ls`, want: Deny, static: Deny},
		{cmd: `kill -9 $$`, want: Deny, static: Deny, why: "not supported by the jailed shell"},
		{cmd: `umask 000`, want: Deny, static: Deny},
		{cmd: `echo (`, want: Deny, static: Deny, why: "does not parse"},
		{cmd: `export X=$(rm nope)`, want: Ask, static: Ask},
		{cmd: `echo ${X:-$(rm nope)}`, want: Ask, static: Ask},
		{cmd: "cat <<EOF\n$(rm nope)\nEOF", want: Ask, static: Ask},
		{cmd: `a=(); a+=($(rm nope))`, want: Ask, static: Ask},
		{cmd: `[[ -n $(rm nope) ]]`, want: Ask, static: Ask},
		{cmd: `echo $((1 + $(rm nope)))`, want: Ask, static: Ask},
		{cmd: `select x in a; do rm $x; done`, want: Ask, static: Ask},
		{cmd: `case x in x) rm y;; esac`, want: Ask, static: Ask},
		{cmd: `time rm x`, want: Ask, static: Ask},
		{cmd: `! rm x`, want: Ask, static: Ask},
		{cmd: `if rm x; then :; fi`, want: Ask, static: Ask},
		{cmd: `ls ~`, want: Allow, static: Allow},
		{cmd: `echo x > ~/f`, want: Allow, static: Deny},
	} {
		for _, static := range []bool{false, true} {
			o := opt
			o.Static = static
			v := pol.Analyze(tc.cmd, o)
			want := tc.want
			if static {
				want = tc.static
			}
			assert.Equal(t, want, v.Kind, "%q (static=%v): %s", tc.cmd, static, v.Reason())
			if tc.why != "" && v.Kind == want && want != Allow && (!static || tc.static == tc.want) {
				assert.Contains(t, v.Reason(), tc.why, "%q (static=%v)", tc.cmd, static)
			}
			if v.Kind == Ask {
				assert.NotNil(t, v.Grant, "%q: an Ask carries what approving it allows", tc.cmd)
			} else {
				assert.Nil(t, v.Grant, "%q: only an Ask carries a grant", tc.cmd)
			}
		}
	}
}

func TestAnalyze_ReasonAndGrant(t *testing.T) {
	opt := session(t)
	v := Readonly().Analyze("rm nope; npm install\ncat /etc/hosts", opt)
	require.Equal(t, Ask, v.Kind)
	assert.Equal(t, "needs approval under shell policy readonly: "+
		"rm (1:1): not on the shell allowlist; npm (1:10): not on the shell allowlist; "+
		"/etc/hosts (2:1): reads outside the session. Everything else in the command is allowed.", v.Reason())
	require.NotNil(t, v.Grant)
	assert.Equal(t, []string{"rm", "npm"}, v.Grant.Programs)
	hosts, err := filepath.EvalSymlinks("/etc/hosts")
	require.NoError(t, err)
	assert.Equal(t, []string{hosts}, v.Grant.ReadPaths)

	d := Readonly().Analyze("sudo ls; rm x", opt)
	require.Equal(t, Deny, d.Kind)
	assert.Equal(t, "shell policy readonly refuses this command: sudo (1:1): never allowed by the shell policy", d.Reason(),
		"a Deny's reason lists only what is refused")
	assert.Equal(t, "", Readonly().Analyze("ls", opt).Reason())
}

// A finding inside a literal eval argument is reported at the eval.
func TestAnalyze_NestedPositions(t *testing.T) {
	v := Readonly().Analyze(`ls; eval "eval 'rm x'"`, session(t))
	require.Equal(t, Ask, v.Kind)
	require.Len(t, v.Findings, 1)
	assert.Equal(t, uint(1), v.Findings[0].Line)
	assert.Equal(t, uint(5), v.Findings[0].Col)

	deep := `eval "eval \"eval 'eval \\\"eval rm\\\"'\""`
	assert.Equal(t, Deny, Readonly().Analyze(deep, session(t)).Kind, "eval nested past the limit is refused")
}

func TestLiteral(t *testing.T) {
	for src, want := range map[string]string{
		`rm`: "rm", `\rm`: "rm", `r''m`: "rm", `"r"m`: "rm", `'a b'`: "a b", `"a\"b"`: `a"b`, `a\ b`: "a b",
	} {
		w := parseWord(t, src)
		got, ok := literal(w)
		assert.True(t, ok, src)
		assert.Equal(t, want, got, src)
	}
	for _, src := range []string{`$x`, `$(rm)`, "`rm`", `r?`, `{a,b}`, `~/x`, `$'rm'`, `"$x"`, `a{1..3}`} {
		_, ok := literal(parseWord(t, src))
		assert.False(t, ok, src)
	}
}

func TestGlobDir(t *testing.T) {
	for src, want := range map[string]string{`/etc/pass*`: "/etc", `*.txt`: ".", `a/b/c*`: "a/b", `/x*`: "/"} {
		got, ok := globDir(parseWord(t, src))
		assert.True(t, ok, src)
		assert.Equal(t, want, got, src)
	}
	for _, src := range []string{`plain`, `$x/*`, `~/x*`} {
		_, ok := globDir(parseWord(t, src))
		assert.False(t, ok, src)
	}
}

func FuzzAnalyze(f *testing.F) {
	for _, s := range []string{"ls", "rm -rf /", "eval 'rm x'", "a=$(b) c", "x() { y; }; x", "cat <(z)", "echo >f", "git -c a=b log"} {
		f.Add(s)
	}
	root := f.TempDir()
	opt := Options{WorkingDir: root, Roots: Roots{Read: []string{root}, Write: []string{root}}}
	pol := Readonly()
	f.Fuzz(func(t *testing.T, cmd string) {
		if len(cmd) > 4096 {
			return
		}
		v := pol.Analyze(cmd, opt)
		if v.Kind == Allow && strings.Contains(" "+cmd, " rm ") && !strings.ContainsAny(cmd, "'\"\\$`#()={}") {
			// a plain literal rm in command position must never be allowed
			if strings.HasPrefix(strings.TrimSpace(cmd), "rm ") {
				t.Fatalf("allowed a literal rm: %q", cmd)
			}
		}
		_ = v.Reason()
	})
}

func parseWord(t *testing.T, src string) *syntax.Word {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	require.NoError(t, err, src)
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	require.True(t, ok, src)
	return call.Args[0]
}
