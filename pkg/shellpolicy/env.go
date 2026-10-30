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
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// execEnvNames are environment variables whose value changes which program
// runs, or what code a shell, interpreter, loader or allowlisted program
// loads and executes. Names are matched exactly (environment names are
// case-sensitive).
var execEnvNames = map[string]bool{
	// Program lookup and the shell itself.
	"PATH": true, "BASH_ENV": true, "ENV": true, "ZDOTDIR": true, "SHELL": true,
	"SHELLOPTS": true, "BASHOPTS": true, "PROMPT_COMMAND": true, "PS4": true, "IFS": true,
	// Programs other programs start.
	"PAGER": true, "MANPAGER": true, "EDITOR": true, "VISUAL": true, "BROWSER": true,
	"LESSOPEN": true, "LESSCLOSE": true, "SSH_ASKPASS": true, "SUDO_ASKPASS": true,
	// Interpreter start-up code and module paths.
	"NODE_OPTIONS": true, "NODE_PATH": true, "PERL5OPT": true, "PERL5LIB": true,
	"PERLLIB": true, "RUBYOPT": true, "RUBYLIB": true, "JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "CLASSPATH": true, "TCLLIBPATH": true,
	// C library modules loaded at run time.
	"GCONV_PATH": true,
}

// execEnvPrefixes are name prefixes with the same effect: the dynamic loader
// (LD_PRELOAD, LD_LIBRARY_PATH, DYLD_INSERT_LIBRARIES), git's program and
// config hooks (GIT_EXTERNAL_DIFF, GIT_SSH, GIT_SSH_COMMAND, GIT_EDITOR,
// GIT_PAGER, GIT_ASKPASS, GIT_EXEC_PATH, GIT_CONFIG_*), Python start-up
// (PYTHONPATH, PYTHONSTARTUP), Lua start-up (LUA_INIT, LUA_PATH) and bash
// exported functions (BASH_FUNC_*).
var execEnvPrefixes = []string{"LD_", "DYLD_", "GIT_", "PYTHON", "LUA_", "BASH_FUNC_"}

// ExecEnvName reports whether setting the environment variable name changes
// what a command runs or loads, so a command that sets it needs approval.
// The list is deliberately broad: a false positive costs an approval, a false
// negative runs a program the policy never saw.
func ExecEnvName(name string) bool {
	if execEnvNames[name] {
		return true
	}
	for _, p := range execEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// setsVar judges one variable the command sets, named as written: a name
// ExecEnvName reports is an Ask granting that name.
func (a *analyzer) setsVar(n syntax.Node, outer *syntax.Pos, name string) {
	if ExecEnvName(name) {
		a.ask(n, outer, name, "sets "+name+", which changes what programs run or load", "env", name)
	}
}

// setsComputedVar judges a variable whose name is computed at run time, so
// it may be any of ExecEnvName's names: an Ask under runtime (nothing checks
// the environment at launch), a Deny under static.
func (a *analyzer) setsComputedVar(n syntax.Node, outer *syntax.Pos, w *syntax.Word) {
	text := wordText(w)
	if a.opt.Static {
		a.add(Deny, n, outer, text, "sets a variable whose name is computed at run time, which cannot be checked")
		return
	}
	a.ask(n, outer, text, "sets a variable whose name is computed at run time, which may change what programs run", "env", text)
}

// assigns judges the assignments before a command, or a bare assignment.
// An IFS prefix on read, mapfile or readarray only sets that builtin's own
// field separator, so it is not judged.
func (a *analyzer) assigns(x *syntax.CallExpr, outer *syntax.Pos) {
	cmd := ""
	if len(x.Args) > 0 {
		cmd, _ = literal(x.Args[0])
	}
	for _, as := range x.Assigns {
		if as.Name == nil {
			continue
		}
		if as.Name.Value == "IFS" && (cmd == "read" || cmd == "mapfile" || cmd == "readarray") {
			continue
		}
		a.setsVar(as, outer, as.Name.Value)
	}
}

// decl judges declare, local, export, readonly, typeset and nameref. Every
// name they set is judged, and so is the target of a name reference
// (declare -n r=PATH makes r=... set PATH).
func (a *analyzer) decl(x *syntax.DeclClause, outer *syntax.Pos) {
	nameref := x.Variant != nil && x.Variant.Value == "nameref"
	for _, as := range x.Args {
		if as.Name == nil {
			// An option ("-x"), or a quoted or computed "NAME=value".
			if as.Value == nil {
				continue
			}
			s, ok := literal(as.Value)
			if !ok {
				a.setsComputedVar(as, outer, as.Value)
				continue
			}
			if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
				if strings.HasPrefix(s, "-") && strings.ContainsRune(s[1:], 'n') {
					nameref = true
				}
				continue
			}
			name, _, _ := strings.Cut(s, "=")
			a.setsVar(as, outer, name)
			continue
		}
		a.setsVar(as, outer, as.Name.Value)
		if nameref && as.Value != nil {
			if target, ok := literal(as.Value); ok {
				a.setsVar(as, outer, target)
			} else {
				a.setsComputedVar(as, outer, as.Value)
			}
		}
	}
}

// arithAssign judges an arithmetic assignment ((( X = 1 )), let X=1, X++).
func (a *analyzer) arithAssign(target syntax.ArithmExpr, outer *syntax.Pos) {
	w, ok := target.(*syntax.Word)
	if !ok {
		return
	}
	if name, ok := literal(w); ok {
		a.setsVar(w, outer, name)
	}
}

// isArithAssign reports whether op assigns to its left operand.
func isArithAssign(op syntax.BinAritOperator) bool {
	switch op {
	case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
		syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn:
		return true
	}
	return false
}

// varBuiltin judges the variables a builtin sets by name: read (bash's
// options; -a takes the array name), mapfile and readarray, printf -v, and
// getopts. A computed word where a name may stand is judged as a computed
// name.
func (a *analyzer) varBuiltin(x *syntax.CallExpr, name string, rest []*syntax.Word, outer *syntax.Pos) {
	switch name {
	case "read":
		a.optionNames(x, rest, "dinNptu", "a", outer)
	case "mapfile", "readarray":
		a.optionNames(x, rest, "dnOsuCc", "", outer)
	case "getopts":
		if len(rest) > 1 {
			a.nameWord(x, rest[1], outer)
		}
	case "printf":
		if len(rest) == 0 {
			return
		}
		first, ok := literal(rest[0])
		switch {
		case ok && first == "-v":
			if len(rest) > 1 {
				a.nameWord(x, rest[1], outer)
			}
		case ok && strings.HasPrefix(first, "-v"):
			a.setsVar(x, outer, first[2:])
		case !ok && len(rest) > 1:
			// A computed first word may be -v; the next word is then a name.
			if s, lit := literal(rest[1]); lit {
				a.setsVar(x, outer, s)
			} else if a.opt.Static {
				a.setsComputedVar(x, outer, rest[0])
			}
		}
	}
}

// optionNames judges a builtin whose positional words are variable names.
// valueOpts are short options that take a value; nameOpts are options whose
// value is itself a variable name.
func (a *analyzer) optionNames(x *syntax.CallExpr, words []*syntax.Word, valueOpts, nameOpts string, outer *syntax.Pos) {
	for i := 0; i < len(words); i++ {
		s, ok := literal(words[i])
		if !ok {
			a.setsComputedVar(x, outer, words[i])
			continue
		}
		if s == "--" {
			for _, w := range words[i+1:] {
				a.nameWord(x, w, outer)
			}
			return
		}
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.setsVar(x, outer, s)
			continue
		}
		group := s[1:]
		for j, c := range group {
			if !strings.ContainsRune(valueOpts+nameOpts, c) {
				continue
			}
			value, valueWord := group[j+1:], (*syntax.Word)(nil)
			if value == "" && i+1 < len(words) {
				i++
				valueWord = words[i]
			}
			if strings.ContainsRune(nameOpts, c) {
				if valueWord != nil {
					a.nameWord(x, valueWord, outer)
				} else {
					a.setsVar(x, outer, value)
				}
			}
			break
		}
	}
}

// nameWord judges one word that names a variable.
func (a *analyzer) nameWord(x *syntax.CallExpr, w *syntax.Word, outer *syntax.Pos) {
	if s, ok := literal(w); ok {
		a.setsVar(x, outer, s)
		return
	}
	a.setsComputedVar(x, outer, w)
}

// envArgs judges the arguments of env before the program it starts: each
// NAME=value it sets, and the options that change what it runs (-S splits a
// string into more assignments and a command; BSD -P sets the search path).
func (a *analyzer) envArgs(nameWord *syntax.Word, rest []*syntax.Word, outer *syntax.Pos) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		s, ok := literal(w)
		if !ok {
			a.setsComputedVar(w, outer, w)
			return
		}
		switch {
		case s == "-u" || s == "-C" || s == "--unset" || s == "--chdir":
			i++ // the value is the next argument
		case s == "-S" || s == "-P" || strings.HasPrefix(s, "--split-string") ||
			(strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "--") && strings.ContainsAny(s[1:], "SP")):
			a.ask(nameWord, outer, "env "+s, "env "+s+" changes what env runs", "program", "env")
			return
		case strings.HasPrefix(s, "-"):
			// -i, -0, -v, -, --ignore-environment, --unset=NAME, --chdir=DIR
		default:
			name, _, isAssign := strings.Cut(s, "=")
			if !isAssign || !syntax.ValidName(name) {
				return // the program env starts
			}
			a.setsVar(w, outer, name)
		}
	}
}
