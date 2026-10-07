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
	// ShortWithValue lists short options whose value is the next argument,
	// so that value is not counted as a positional argument.
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
				if len(name) > 3 && strings.HasPrefix(l, name) {
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
// argument, and the value of every "--option=value".
func pathCandidates(args []string) []string {
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
		default:
			out = append(out, a)
		}
	}
	return out
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
	plain("cat", "head", "tail", "wc", "cut", "comm", "cmp", "diff", "od",
		"ls", "stat", "du", "realpath",
		"md5sum", "sha1sum", "sha256sum", "shasum", "md5",
		"grep", "egrep", "fgrep", "jq")
	noPaths("tr", "basename", "dirname", "seq", "sleep", "uname", "whoami", "id", "which")
	p.Programs["sort"] = &Profile{MaxPositional: -1, RefuseShort: "oT",
		RefuseLong: []string{"--output", "--compress-program", "--temporary-directory"}}
	p.Programs["uniq"] = &Profile{MaxPositional: 1, ShortWithValue: "fsw"}
	p.Programs["find"] = &Profile{MaxPositional: -1, RefuseArgs: []string{
		"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"}}
	p.Programs["file"] = &Profile{MaxPositional: -1, RefuseShort: "C", RefuseLong: []string{"--compile"}}
	p.Programs["base64"] = &Profile{MaxPositional: -1, RefuseShort: "o", RefuseLong: []string{"--output"}}
	p.Programs["date"] = &Profile{MaxPositional: -1, NoPathArgs: true, RefuseShort: "s",
		RefuseLong: []string{"--set"}, Check: dateCheck}
	p.Programs["git"] = gitProfile()
	return p
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
	"-O", "--open-files-in-pager", "--paginate",
}

// gitGlobalAllowed are global options (before the subcommand) the profile lets
// through. "-C" takes a directory, which path confinement checks.
var gitGlobalAllowed = map[string]bool{"--no-pager": true, "-P": true, "--no-optional-locks": true, "-C": true}

func gitProfile() *Profile {
	return &Profile{
		MaxPositional: -1,
		Check:         gitCheck,
		Harden:        gitHarden,
		Env: []string{
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
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
		name, _, _ := strings.Cut(a, "=")
		for _, r := range gitRefused {
			if name == r {
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

// gitHarden prepends the options that stop git running programs from the
// repository's own config (design 06 Probe 9), and adds --no-ext-diff and
// --no-textconv to the subcommands that diff.
func gitHarden(args []string) []string {
	globals, sub := gitSplit(args)
	out := []string{
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "--no-pager", "--attr-source=" + EmptyTreeHash,
	}
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
