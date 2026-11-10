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
	"regexp"
	"strings"
)

// EmptyTreeHash is git's well-known empty tree object. Pointing git's
// attribute source at it means no .gitattributes applies, so no filter,
// textconv or diff driver from the repository's own config runs.
const EmptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Profile is the rule set a program's arguments must satisfy to run without
// approval.
type Profile struct {
	// RefuseShort lists single-letter options refused anywhere in a short
	// option group ("-ro" contains "o").
	RefuseShort string
	// RefuseLong lists long options refused, with or without "=value", and
	// any abbreviation of them (GNU getopt accepts unambiguous prefixes).
	RefuseLong []string
	// RefuseArgs lists argument tokens refused exactly (find's "-exec").
	RefuseArgs []string
	// ShortWithValue lists short options that take a value: the rest of the
	// option group ("-k2"), or else the next argument, which is then not
	// counted as a positional argument. RefuseShort does not look at letters
	// inside such a value.
	ShortWithValue string
	// MaxPositional limits positional arguments; negative means unlimited.
	MaxPositional int
	// NoPathArgs marks a program whose arguments are not file names, so path
	// confinement does not look at them.
	NoPathArgs bool
	// Check is an extra rule over the arguments; it returns the violation, or
	// "" when the arguments are fine.
	Check func(args []string) string
	// Harden rewrites the arguments before the program starts (run time
	// only), for example to add git's safety flags.
	Harden func(args []string) []string
	// Env lists extra NAME=value pairs the program runs with (run time only).
	Env []string
	// RepoCheck is an extra rule over the directory the program runs in
	// and its arguments (git: the repository it would use); it returns the
	// violation, or "". The analyzer calls it with each directory the
	// command can be in, when every argument is literal; CheckLaunch calls
	// it with the shell's directory.
	RepoCheck func(dir string, args []string) string
	// NoDirOperands marks a program that reads every file in a directory
	// operand and follows the symlinks it meets there (diff): an operand
	// that is an existing directory is outside the profile.
	NoDirOperands bool
	// Alternates names programs that do the same job under another
	// platform's name (BSD md5 for GNU md5sum). The readonly policy lists
	// both so a command works on either platform. A runner that cannot find
	// this program does not report it missing when it found one of these.
	Alternates []string

	// pathShort lists the ShortWithValue options whose value names a file:
	// a value attached to one ("-f../x") is path-checked like a positional
	// argument. A value in the next argument is checked as a positional
	// argument anyway.
	pathShort string
}

// violation returns why args break the profile, or "". complete reports that
// args are every argument of the call; when some arguments could not be read
// statically, positional counting is skipped.
func (pr *Profile) violation(args []string, complete bool) string {
	if pr == nil {
		return ""
	}
	positional := 0
	afterDashes := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if afterDashes || a == "-" || !strings.HasPrefix(a, "-") {
			positional++
			continue
		}
		if a == "--" {
			afterDashes = true
			continue
		}
		for _, r := range pr.RefuseArgs {
			if a == r {
				return a + " is outside the profile"
			}
		}
		if strings.HasPrefix(a, "--") {
			name, _, _ := strings.Cut(a, "=")
			for _, l := range pr.RefuseLong {
				if len(name) > 2 && strings.HasPrefix(l, name) {
					return l + " is outside the profile"
				}
			}
			continue
		}
		group := a[1:]
		for j, c := range group {
			if strings.ContainsRune(pr.RefuseShort, c) {
				return "-" + string(c) + " is outside the profile"
			}
			if strings.ContainsRune(pr.ShortWithValue, c) {
				if j == len(group)-1 {
					i++ // the value is the next argument
				}
				break
			}
		}
	}
	if complete && pr.MaxPositional >= 0 && positional > pr.MaxPositional {
		return "more than " + itoa(pr.MaxPositional) + " file argument(s) (the next would be written)"
	}
	if pr.Check != nil {
		if v := pr.Check(args); v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// pathCandidates returns the arguments that may name files: every positional
// argument, the value of every "--option=value", and a value attached to a
// short option whose value is a file (pathShort).
func (pr *Profile) pathCandidates(args []string) []string {
	var out []string
	afterDashes := false
	for _, a := range args {
		switch {
		case afterDashes:
			out = append(out, a)
		case a == "--":
			afterDashes = true
		case a == "-":
		case strings.HasPrefix(a, "--"):
			if _, v, ok := strings.Cut(a, "="); ok && v != "" {
				out = append(out, v)
			}
		case strings.HasPrefix(a, "-"):
			if v := pr.attachedPath(a[1:]); v != "" {
				out = append(out, v)
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// attachedPath returns the file name attached to a path-valued short option
// in the option group, or "".
func (pr *Profile) attachedPath(group string) string {
	if pr == nil {
		return ""
	}
	for j, c := range group {
		if !strings.ContainsRune(pr.ShortWithValue, c) {
			continue
		}
		if strings.ContainsRune(pr.pathShort, c) {
			return group[j+1:]
		}
		return "" // the rest of the group is a value that is not a file
	}
	return ""
}

// Readonly returns the built-in "readonly" policy: programs that cannot start
// another program, cannot write a file except through an argument their
// profile refuses, and are run often (design 06 §5.3, decision D8).
func Readonly() *Policy {
	p := &Policy{
		Name:     "readonly",
		flat:     Spec{Name: "readonly"},
		Builtins: map[string]bool{},
		Programs: map[string]*Profile{},
		Never:    map[string]bool{"sudo": true, "su": true, "doas": true, "pkexec": true},
	}
	for _, b := range []string{
		"echo", "printf", "test", "[", "cd", "pwd", "read", "true", "false", ":",
		"set", "shift", "exit", "return", "break", "continue", "wait", "type",
		"getopts", "pushd", "popd", "dirs", "mapfile", "readarray", "eval", "trap",
		"unset",
	} {
		p.Builtins[b] = true
	}
	plain := func(names ...string) {
		for _, n := range names {
			p.Programs[n] = &Profile{MaxPositional: -1}
		}
	}
	noPaths := func(names ...string) {
		for _, n := range names {
			p.Programs[n] = &Profile{MaxPositional: -1, NoPathArgs: true}
		}
	}
	plain("cat", "head", "tail", "cut", "comm", "cmp", "od",
		"stat", "realpath", "md5")
	// -c (--check) reads the names of the files to hash from a sums file,
	// which path confinement never sees, and tells whether each exists and
	// matches. BSD md5 -c compares against a string, so md5 keeps it.
	for _, n := range []string{"md5sum", "sha1sum", "sha256sum", "shasum"} {
		p.Programs[n] = &Profile{MaxPositional: -1, RefuseShort: "c", RefuseLong: []string{"--check"}}
	}
	// GNU systems ship md5sum, sha1sum and sha256sum; macOS ships md5 and
	// shasum (and, on recent releases, the GNU names too). Each name stands
	// in for the other platform's, so only a host with neither is told one
	// is missing.
	for name, alts := range map[string][]string{
		"md5": {"md5sum"}, "md5sum": {"md5"},
		"sha1sum": {"shasum"}, "sha256sum": {"shasum"},
	} {
		p.Programs[name].Alternates = alts
	}
	noPaths("tr", "basename", "dirname", "seq", "sleep", "uname", "whoami", "id", "which")
	// --files0-from=F reads the names of the files to read from F, which
	// path confinement never sees (GNU sort printed a file named in F).
	// wc and du leak the named files' counts and sizes the same way.
	p.Programs["wc"] = &Profile{MaxPositional: -1, RefuseLong: []string{"--files0-from"}}
	p.Programs["ls"] = &Profile{MaxPositional: -1, Check: lsCheck}
	// Options that take a file name attached to them ("-f../x") are listed
	// in pathShort, so the attached name is path-checked too. -R
	// (--dereference-recursive) follows every symlink it meets out of the
	// roots; -r follows only those on the command line, which are checked.
	// BSD grep -S follows every symlink too (GNU grep has no -S).
	for _, n := range []string{"grep", "egrep", "fgrep"} {
		p.Programs[n] = &Profile{MaxPositional: -1, RefuseShort: "RS", RefuseLong: []string{"--dereference-recursive"},
			ShortWithValue: "efmABCdD", pathShort: "f"}
	}
	// diff compares the files in a directory operand (and, with -r, in its
	// subdirectories) and follows the symlinks it meets there, so a
	// directory operand is outside the profile, with or without -r.
	p.Programs["diff"] = &Profile{MaxPositional: -1, ShortWithValue: "CUDFIxXSWL", pathShort: "X", NoDirOperands: true}
	// du -L (--dereference) follows every symlink it meets out of the
	// roots; -H, -D and --dereference-args follow only the operands, which
	// are path-checked.
	p.Programs["du"] = &Profile{MaxPositional: -1, RefuseShort: "L", RefuseLong: []string{"--files0-from", "--dereference"},
		ShortWithValue: "XBdtI", pathShort: "X"}
	// jq's import, include and modulemeta load a module by path, which path
	// confinement never sees; a program file (-f, --from-file) can hold them.
	p.Programs["jq"] = &Profile{MaxPositional: -1, ShortWithValue: "L", pathShort: "L",
		RefuseShort: "f", RefuseLong: []string{"--from-file"}, Check: jqCheck}
	p.Programs["sort"] = &Profile{MaxPositional: -1, RefuseShort: "oT", ShortWithValue: "ktS",
		RefuseLong: []string{"--output", "--compress-program", "--temporary-directory", "--files0-from"}}
	p.Programs["uniq"] = &Profile{MaxPositional: 1, ShortWithValue: "fsw"}
	// find -L and -follow follow every symlink out of the roots; -H follows
	// only the starting points, which are path-checked. findCheck refuses
	// -L inside a BSD option group (-LE, -xL). GNU find -files0-from F reads
	// the starting points from F, which path confinement never sees.
	p.Programs["find"] = &Profile{MaxPositional: -1, Check: findCheck, RefuseArgs: []string{
		"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls",
		"-follow", "-files0-from"}}
	// file -f F (--files-from F) reads the names of the files to check from
	// F, which path confinement never sees, as sort --files0-from does.
	p.Programs["file"] = &Profile{MaxPositional: -1, RefuseShort: "Cf", RefuseLong: []string{"--compile", "--files-from"},
		ShortWithValue: "eFfmP", pathShort: "m"}
	p.Programs["base64"] = &Profile{MaxPositional: -1, RefuseShort: "o", RefuseLong: []string{"--output"},
		ShortWithValue: "wbi", pathShort: "i"}
	// date's arguments are not path-checked (they are formats and times), so
	// the options that read a file are refused: -f/--file prints the file's
	// lines in its errors, -r/--reference reads its time.
	p.Programs["date"] = &Profile{MaxPositional: -1, NoPathArgs: true, RefuseShort: "sfr", ShortWithValue: "dv",
		RefuseLong: []string{"--set", "--file", "--reference"}, Check: dateCheck}
	p.Programs["git"] = gitProfile()
	return p
}

// lsCheck refuses ls -L (--dereference) together with -R (--recursive):
// ls then lists the directories symlinks point to, recursively, so a symlink
// in the session leads it out of the read roots. Each alone stays allowed:
// -R does not follow symlinks, and -L alone lists one level of the operands.
// Every letter of a short option group counts, so a GNU option value such as
// "-IRL" (ignore pattern "RL") is refused too; that costs an approval, never
// a listing outside the roots. Long options are matched by abbreviation.
func lsCheck(args []string) string {
	recursive, deref := false, false
	for _, a := range args {
		if a == "--" {
			break
		}
		switch {
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(a, "=")
			if len(name) > 2 && strings.HasPrefix("--recursive", name) {
				recursive = true
			}
			if len(name) > 2 && strings.HasPrefix("--dereference", name) {
				deref = true
			}
		case strings.HasPrefix(a, "-") && a != "-":
			recursive = recursive || strings.ContainsRune(a[1:], 'R')
			deref = deref || strings.ContainsRune(a[1:], 'L')
		}
	}
	if recursive && deref {
		return "ls -L with -R lists the directories symlinks point to, out of the read roots"
	}
	return ""
}

// findOptionLetters are the letters of BSD find's options that take no
// value (find [-H | -L | -P] [-EXdsx]). BSD find reads them as one group
// ("-LE"); GNU find takes -H, -L and -P only as separate arguments. No
// GNU or BSD find primary is spelled with these letters and an L, so such a
// group is an option group, not an expression.
const findOptionLetters = "EHLPXdsx"

// findCheck refuses an option group that contains -L ("-L", "-LE", "-xL"):
// find then follows every symlink it meets out of the read roots.
func findCheck(args []string) string {
	for _, a := range args {
		if len(a) < 2 || a[0] != '-' || a[1] == '-' {
			continue
		}
		group := a[1:]
		if strings.ContainsRune(group, 'L') && strings.Trim(group, findOptionLetters) == "" {
			return "find " + a + " is outside the profile"
		}
	}
	return ""
}

// jqModuleWords are the jq keywords and builtins that load a module file by
// path: import and include in the program, and modulemeta at run time.
var jqModuleWords = map[string]bool{"import": true, "include": true, "modulemeta": true}

// jqCheck refuses a jq argument that uses a module: jq looks the path up
// itself, so a symlink in the session leads it out of the read roots (jq
// refuses ".." in the path, but not a symlink). Every argument is read as
// jq code, because which one is the program depends on jq's options; a file
// name such as include.json costs an approval, never a module load.
func jqCheck(args []string) string {
	for _, a := range args {
		if jqUsesModule(a) {
			return "jq import, include and modulemeta load modules by path, which path confinement does not see"
		}
	}
	return ""
}

// jqUsesModule reports whether src, read as jq code, holds one of
// jqModuleWords as a name: outside string literals and comments, and not a
// field (.import) or a variable ($import). It follows string interpolation
// (\(...)), whose code is checked too. It ends a comment at the newline, so
// it reads at least as much code as jq does.
func jqUsesModule(src string) bool {
	var interp []int // paren depth at which each open interpolation ends
	inStr := false
	depth := 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			switch c {
			case '\\':
				if i+1 < len(src) && src[i+1] == '(' {
					interp = append(interp, depth)
					depth++
					inStr = false
				}
				i++
			case '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if n := len(interp); n > 0 && depth == interp[n-1] {
				interp = interp[:n-1]
				inStr = true
			}
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			if jqModuleWords[src[i:j]] && (i == 0 || (src[i-1] != '.' && src[i-1] != '$')) {
				return true
			}
			i = j - 1
		}
	}
	return false
}

// dateCheck refuses BSD date's positional time setting ("date 202601010000").
var dateSetArg = regexp.MustCompile(`^[0-9]{4,}(\.[0-9]{2})?$`)

func dateCheck(args []string) string {
	for _, a := range args {
		if dateSetArg.MatchString(a) {
			return a + " would set the clock"
		}
	}
	return ""
}

// gitSubcommands are the git subcommands the git profile allows.
var gitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true,
	"ls-files": true, "blame": true, "grep": true, "describe": true, "shortlog": true,
	"branch": true,
}

// gitBranchFlags are the only arguments "git branch" may take (listing).
var gitBranchFlags = map[string]bool{
	"--list": true, "-a": true, "--all": true, "-r": true, "--remotes": true,
	"-v": true, "-vv": true, "--verbose": true, "--show-current": true,
}

// gitRefused are options refused anywhere in a git command line: they change
// where git reads its repository or programs, undo the hardening, write files,
// or start a pager. Global options (before the subcommand) are also limited to
// gitGlobalAllowed, which is what refuses "-c": as a subcommand option "-c"
// means something harmless ("git grep -c" counts).
var gitRefused = []string{
	"--config-env", "--exec-path", "--attr-source", "--git-dir", "--work-tree",
	"--namespace", "--super-prefix", "--output", "--ext-diff", "--textconv",
	"--open-files-in-pager", "--paginate",
}

// gitExactOptions are real git options that are also a prefix of a refused
// one, so they are not refused as an abbreviation of it.
var gitExactOptions = map[string]bool{"--text": true}

// gitGlobalAllowed are global options (before the subcommand) the profile lets
// through. "-C" takes a directory, which path confinement checks.
var gitGlobalAllowed = map[string]bool{"--no-pager": true, "-P": true, "--no-optional-locks": true, "-C": true}

func gitProfile() *Profile {
	return &Profile{
		MaxPositional: -1,
		Check:         gitCheck,
		RepoCheck:     gitRepoCheck,
		Harden:        gitHarden,
		Env: []string{
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1",
		},
	}
}

// gitValueGlobals are git global options whose value is the next argument.
var gitValueGlobals = map[string]bool{
	"-c": true, "-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--super-prefix": true, "--attr-source": true,
}

// gitSplit returns git's global options (with their values) and the index of
// the subcommand, or -1 when there is none.
func gitSplit(args []string) (globals []string, sub int) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return args[:i], i
		}
		if gitValueGlobals[a] && i+1 < len(args) {
			i++
		}
	}
	return args, -1
}

func gitCheck(args []string) string {
	for _, a := range args {
		// -O runs a program (git grep -O<cmd> opens matches in it) or reads
		// a file (diff -O<orderfile>), alone or in a group such as -iO<cmd>.
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'O') {
			return "git -O is outside the profile"
		}
		name, _, _ := strings.Cut(a, "=")
		if !strings.HasPrefix(name, "--") || len(name) < 3 || gitExactOptions[name] {
			continue
		}
		// git's option parser accepts any unambiguous abbreviation
		// (git grep --op=<cmd>), so a prefix of a refused option is refused.
		for _, r := range gitRefused {
			if strings.HasPrefix(r, name) {
				return "git " + r + " is outside the profile"
			}
		}
	}
	globals, sub := gitSplit(args)
	for i := 0; i < len(globals); i++ {
		g := globals[i]
		if !gitGlobalAllowed[g] {
			return "git " + g + " is outside the profile"
		}
		if gitValueGlobals[g] {
			i++
		}
	}
	if sub < 0 {
		return "git with no subcommand is outside the profile"
	}
	name := args[sub]
	if !gitSubcommands[name] {
		return "git " + name + " is outside the profile"
	}
	if name == "branch" {
		for _, a := range args[sub+1:] {
			if !gitBranchFlags[a] {
				return "git branch " + a + " is outside the profile (listing only)"
			}
		}
	}
	return ""
}

// gitTransports are git's built-in transports and remote helpers.
var gitTransports = []string{"ext", "fd", "file", "git", "ssh", "http", "https", "ftp", "ftps"}

// gitNoTransport turns every transport off: a partial clone fetches a
// missing blob from its promisor remote on git show, log -p, diff and
// blame, and the repository's config picks the transport (core.sshCommand,
// or an ext:: URL that runs a command). GIT_NO_LAZY_FETCH=1 in the profile's
// Env stops the fetch on git 2.44 and later; for older git these options
// refuse the transport. Each protocol.<name>.allow is set as well as
// protocol.allow, because the repository's own protocol.<name>.allow
// overrides protocol.allow, and the command line overrides the repository.
var gitNoTransport = func() []string {
	out := []string{"-c", "protocol.allow=never"}
	for _, t := range gitTransports {
		out = append(out, "-c", "protocol."+t+".allow=never")
	}
	return out
}()

// gitRepoCheck refuses a git command that would use a bare repository:
// one found from dir, after each -C, before any directory holding a .git.
// A clone can track such a repository, with a config that sets
// core.worktree and a filter, so git diff run in it runs the filter.
func gitRepoCheck(dir string, args []string) string {
	globals, _ := gitSplit(args)
	d := dir
	for i := 0; i < len(globals); i++ {
		if globals[i] == "-C" && i+1 < len(globals) {
			d = ResolvePath(d, globals[i+1])
		}
		if gitValueGlobals[globals[i]] {
			i++
		}
	}
	if bare := bareRepoAt(d); bare != "" {
		return "git would use the bare repository " + bare + ", whose own config can make git run programs"
	}
	return ""
}

// bareRepoAt returns the bare repository git discovers from dir, or "":
// walking up from dir, the first directory that holds a .git entry is a
// normal repository (""), and the first that is itself a git directory is
// bare unless it is named .git. dir is resolved, as git's getcwd is.
func bareRepoAt(dir string) string {
	for p := filepath.Clean(dir); ; {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return ""
		}
		if isGitDir(p) {
			if filepath.Base(p) == ".git" {
				return ""
			}
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

// gitHarden prepends the options that stop git running programs from the
// repository's own config (design 06 Probe 9), and adds --no-ext-diff and
// --no-textconv to the subcommands that diff. core.attributesFile=/dev/null
// drops an attributes file the config names; log.showSignature=false and
// the gpg program defaults stop git log and git show running a signature
// program the config names. safe.bareRepository=explicit (git 2.38) makes git
// refuse a bare repository it would discover, such as one a clone tracks;
// gitRepoCheck refuses the same for older git, which ignores the key.
// Nothing here can drop $GIT_DIR/info/attributes: a write into a git
// directory is an Ask instead (inGitDir).
func gitHarden(args []string) []string {
	globals, sub := gitSplit(args)
	out := []string{
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "-c", "core.attributesFile=/dev/null",
		"-c", "log.showSignature=false", "-c", "gpg.program=gpg", "-c", "gpg.ssh.program=ssh-keygen",
		"-c", "gpg.x509.program=gpgsm", "-c", "safe.bareRepository=explicit",
	}
	out = append(out, gitNoTransport...)
	out = append(out, "--no-pager", "--attr-source="+EmptyTreeHash)
	out = append(out, globals...)
	if sub < 0 {
		return out
	}
	out = append(out, args[sub])
	switch args[sub] {
	case "diff", "log", "show":
		out = append(out, "--no-ext-diff", "--no-textconv")
	}
	return append(out, args[sub+1:]...)
}
