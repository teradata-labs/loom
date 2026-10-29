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
		swhy   string      // a substring of the static reason, when it differs
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
		{cmd: `cat <(rm nope)`, want: Ask, static: Deny},
		{cmd: `/bin/rm nope`, want: Ask, static: Ask, why: "runs a program by path"},
		{cmd: `./rm`, want: Ask, static: Ask},
		{cmd: `\rm x`, want: Ask, static: Ask, why: "rm (1:1)"},
		{cmd: `r''m x`, want: Ask, static: Ask, why: "rm (1:1)"},
		{cmd: `find . -exec rm {} \;`, want: Ask, static: Ask, why: "-exec is outside the profile"},
		{cmd: `find . -delete`, want: Ask, static: Ask},
		{cmd: `find . $X`, want: Allow, static: Deny, swhy: "an argument computed at run time"},
		{cmd: `sort -o out a`, want: Ask, static: Ask},
		{cmd: `sort -ro out a`, want: Ask, static: Ask},
		{cmd: `sort --out=x a`, want: Ask, static: Ask, why: "--output"},
		{cmd: `sort -- -o`, want: Allow, static: Allow},
		{cmd: `uniq a b`, want: Ask, static: Ask, why: "more than 1 file argument"},
		{cmd: `uniq -f 1 a`, want: Allow, static: Allow},
		{cmd: `git -c core.pager='sh -c id' log`, want: Ask, static: Deny, why: "git -c"},
		{cmd: `git log -p`, want: Allow, static: Deny},
		{cmd: `git grep -c x`, want: Allow, static: Deny},
		{cmd: `git --no-pager log --oneline`, want: Allow, static: Deny},
		{cmd: `git branch`, want: Allow, static: Deny, swhy: "git needs the runner's hardening"},
		{cmd: `git status`, want: Allow, static: Deny, swhy: "git needs the runner's hardening"},
		{cmd: `git branch -D x`, want: Ask, static: Deny, why: "listing only"},
		{cmd: `git push`, want: Ask, static: Deny, why: "git push"},
		{cmd: `git log --output=x`, want: Ask, static: Deny},
		{cmd: `git -C /usr status`, want: Ask, static: Deny, why: "reads outside the session"},
		{cmd: `echo x > /etc/x`, want: Deny, static: Deny, why: "writes outside the session"},
		{cmd: `echo x >> ../../outside`, want: Deny, static: Deny},
		{cmd: `echo x > ../out.txt; echo y >> out.txt; ls 2>&1 >/dev/null; ls >&2`, want: Allow, static: Allow},
		{cmd: `cat < /etc/hosts`, want: Ask, static: Ask, why: "reads outside the session"},
		{cmd: `cat /etc/hosts`, want: Ask, static: Ask},
		{cmd: `cd /; ls`, want: Ask, static: Ask},
		{cmd: `cd ../..; ls`, want: Ask, static: Ask},
		{cmd: `d=/; cd $d; ls`, want: Allow, static: Deny, swhy: "$d (1:9): a path computed at run time"},
		{cmd: `cd ~`, want: Allow, static: Deny},
		{cmd: `ls /etc/pass*`, want: Ask, static: Ask},
		{cmd: `p=/etc; ls $p/pass*`, want: Allow, static: Deny},
		{cmd: `for f in *.txt; do wc -l "$f"; done`, want: Allow, static: Deny},
		{cmd: `wc -l *.txt`, want: Allow, static: Allow},
		{cmd: `cat ~/.ssh/config`, want: Allow, static: Deny, swhy: "~/.ssh/config (1:5): an argument computed at run time"},
		{cmd: `cat $HOME/.ssh/config`, want: Allow, static: Deny, swhy: "$HOME/.ssh/config"},
		{cmd: `cat ${HOME}/x`, want: Allow, static: Deny},
		{cmd: `cat $(echo /etc/hosts)`, want: Allow, static: Deny},
		{cmd: "cat `echo /etc/hosts`", want: Allow, static: Deny},
		{cmd: `cat < $f`, want: Allow, static: Deny, swhy: "reads a path computed at run time"},
		{cmd: `sleep $N; echo $HOME`, want: Allow, static: Allow},
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
		{cmd: `ls ~`, want: Allow, static: Deny},
		{cmd: `echo x > ~/f`, want: Allow, static: Deny},
		{cmd: `PATH=/tmp/evil ls`, want: Ask, static: Ask, why: "PATH (1:1): sets PATH"},
		{cmd: `GIT_EXTERNAL_DIFF=rm git diff`, want: Ask, static: Deny, why: "sets GIT_EXTERNAL_DIFF"},
		{cmd: `GIT_PAGER=sh git log`, want: Ask, static: Deny, why: "sets GIT_PAGER"},
		{cmd: `LD_PRELOAD=/tmp/x.so ls`, want: Ask, static: Ask, why: "sets LD_PRELOAD"},
		{cmd: `DYLD_INSERT_LIBRARIES=/tmp/x.dylib ls`, want: Ask, static: Ask, why: "sets DYLD_INSERT_LIBRARIES"},
		{cmd: `BASH_ENV=/tmp/x ls`, want: Ask, static: Ask, why: "sets BASH_ENV"},
		{cmd: `PYTHONSTARTUP=/tmp/x ls`, want: Ask, static: Ask, why: "sets PYTHONSTARTUP"},
		{cmd: `PATH=/tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `export PATH=/tmp/evil; ls`, want: Ask, static: Ask, why: "PATH (1:8): sets PATH"},
		{cmd: `declare -x PATH=/tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `typeset -x LD_PRELOAD=/tmp/x.so`, want: Ask, static: Ask, why: "sets LD_PRELOAD"},
		{cmd: `readonly -x EDITOR=sh`, want: Ask, static: Ask, why: "sets EDITOR"},
		{cmd: `f() { local PATH=/tmp/evil; ls; }; f`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `export "PATH=/tmp/evil"`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `export "$n=/tmp/evil"`, want: Ask, static: Deny, why: "name is computed at run time"},
		{cmd: `env PATH=/tmp/evil ls`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `env -u HOME GIT_SSH=/tmp/x ls`, want: Ask, static: Ask, why: "sets GIT_SSH"},
		{cmd: `declare -n r=PATH; r=/tmp/evil; ls`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `for PATH in /tmp/evil; do ls; done`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `read -r PATH <<< /tmp/evil; ls`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `read -a LD_PRELOAD <<< /tmp/x.so`, want: Ask, static: Ask, why: "sets LD_PRELOAD"},
		{cmd: `mapfile -t PATH < /dev/null`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `printf -v PATH /tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `getopts ab PAGER`, want: Ask, static: Ask, why: "sets PAGER"},
		{cmd: `: ${PATH:=/tmp/evil}`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `(( LD_BIND_NOW = 1 ))`, want: Ask, static: Ask, why: "sets LD_BIND_NOW"},
		{cmd: `eval "PATH=/tmp/evil ls"`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `IFS=x; ls`, want: Ask, static: Ask, why: "sets IFS"},
		{cmd: `printf -vPATH /tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `printf "$f" PATH /tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `printf "$f" "$x"`, want: Allow, static: Deny, swhy: "name is computed at run time"},
		{cmd: `printf '%s\\n' PATH`, want: Allow, static: Allow},
		{cmd: `read -- PATH <<< /tmp/evil`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `read -p prompt -aLD_PRELOAD <<< x`, want: Ask, static: Ask, why: "sets LD_PRELOAD"},
		{cmd: `getopts ab "$n"`, want: Ask, static: Deny, why: "name is computed at run time"},
		{cmd: `(( PATH++ ))`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `env -i -u X PATH=/tmp/evil ls`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `env "$a" ls`, want: Ask, static: Deny, swhy: "name is computed at run time"},
		{cmd: `export -n PATH`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `declare -n r="$t"`, want: Ask, static: Deny, why: "name is computed at run time"},
		{cmd: `export 'FOO' "BAR=1"`, want: Allow, static: Allow},
		{cmd: `ls >& $f`, want: Allow, static: Deny, swhy: "redirects to a path computed at run time"},
		{cmd: `let PATH=1`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `PATH+=:/tmp/evil ls`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `select PATH in a; do ls; done`, want: Ask, static: Ask, why: "sets PATH"},
		{cmd: `nameref r=LD_PRELOAD`, want: Ask, static: Ask, why: "sets LD_PRELOAD"},
		{cmd: `env -S "PATH=/tmp/evil ls"`, want: Ask, static: Ask, why: "env -S changes what env runs"},
		{cmd: `read -r "$v" <<< /tmp/evil`, want: Ask, static: Deny, why: "name is computed at run time"},
		{cmd: `FOO=1 ls`, want: Allow, static: Allow},
		{cmd: `export FOO=1`, want: Allow, static: Allow},
		{cmd: `declare -x FOO=1; x=2; echo $x`, want: Allow, static: Allow},
		{cmd: `path=/tmp/evil ls`, want: Allow, static: Allow},
		{cmd: `for f in a b; do echo $f; done`, want: Allow, static: Allow},
		{cmd: `read -r line <<< x; echo $line`, want: Allow, static: Allow},
		{cmd: `IFS=, read -r a b <<< "1,2"`, want: Allow, static: Allow},
		{cmd: `env FOO=1 ls`, want: Ask, static: Ask, why: "env (1:1): not on the shell allowlist"},
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
			if static && tc.swhy != "" {
				assert.Contains(t, v.Reason(), tc.swhy, "%q (static)", tc.cmd)
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
// An Ask for a variable that changes what runs names it in the reason and
// in the grant.
func TestAnalyze_ExecEnvGrant(t *testing.T) {
	v := Readonly().Analyze("PATH=/tmp/evil ls; GIT_EXTERNAL_DIFF=rm git diff", session(t))
	require.Equal(t, Ask, v.Kind)
	assert.Equal(t, "needs approval under shell policy readonly: "+
		"PATH (1:1): sets PATH, which changes what programs run or load; "+
		"GIT_EXTERNAL_DIFF (1:20): sets GIT_EXTERNAL_DIFF, which changes what programs run or load. "+
		"Everything else in the command is allowed.", v.Reason())
	require.NotNil(t, v.Grant)
	assert.Equal(t, []string{"PATH", "GIT_EXTERNAL_DIFF"}, v.Grant.Env)
	assert.Empty(t, v.Grant.Programs)
	assert.Equal(t, []string{"PATH", "LD_PRELOAD"}, MergeGrants([]any{&Grant{Env: []string{"PATH"}}, &Grant{Env: []string{"LD_PRELOAD", "PATH"}}}).Env)
}

func TestExecEnvName(t *testing.T) {
	for _, name := range []string{"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "BASH_ENV", "ENV",
		"GIT_EXTERNAL_DIFF", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_EDITOR", "GIT_PAGER", "GIT_ASKPASS", "GIT_EXEC_PATH",
		"PAGER", "EDITOR", "VISUAL", "PYTHONPATH", "PYTHONSTARTUP", "NODE_OPTIONS", "PERL5OPT", "RUBYOPT", "IFS",
		"PROMPT_COMMAND", "LUA_INIT", "BASH_FUNC_ls%%", "GCONV_PATH"} {
		assert.True(t, ExecEnvName(name), name)
	}
	for _, name := range []string{"FOO", "HOME", "LANG", "path", "editor", "TERM", "PWD", "MY_PATH", "XPATH"} {
		assert.False(t, ExecEnvName(name), name)
	}
}

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
