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
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// VerdictKind is the outcome of judging a command: run it, ask a person, or
// refuse it.
type VerdictKind int

const (
	Allow VerdictKind = iota
	Ask
	Deny
)

func (k VerdictKind) String() string {
	switch k {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}

// Finding is one part of a command that needs approval or is refused.
type Finding struct {
	Verdict VerdictKind
	// What is the part of the command, as written ("rm", "find -delete",
	// "/etc/hosts").
	What string
	// Why says what rule it meets ("not on the shell allowlist").
	Why string
	// Line and Col locate it in the command (1-based). Parts found inside a
	// literal eval or trap argument are reported at that eval or trap. Both
	// are 0 for a variable the call's env parameter sets.
	Line, Col uint
}

// Verdict is the judgment of a whole command.
type Verdict struct {
	Kind     VerdictKind
	Policy   string
	Findings []Finding
	// Grant is what approving the command allows; set only on Ask.
	Grant *Grant
}

// Reason renders the verdict for a person or a model: every finding of the
// verdict's kind, with its position. Empty for Allow.
func (v Verdict) Reason() string {
	if v.Kind == Allow {
		return ""
	}
	var parts []string
	for _, f := range v.Findings {
		if f.Verdict != v.Kind {
			continue
		}
		if f.Line == 0 {
			parts = append(parts, fmt.Sprintf("%s (env parameter): %s", f.What, f.Why))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%d:%d): %s", f.What, f.Line, f.Col, f.Why))
	}
	if v.Kind == Deny {
		return "shell policy " + v.Policy + " refuses this command: " + strings.Join(parts, "; ")
	}
	return "needs approval under shell policy " + v.Policy + ": " + strings.Join(parts, "; ") +
		". Everything else in the command is allowed."
}

// Options are what Analyze needs to know about the call.
type Options struct {
	// WorkingDir is the absolute directory the command starts in; relative
	// paths resolve against it.
	WorkingDir string
	// Roots are the directories the command may read and write.
	Roots Roots
	// Static marks a binding with no runner behind it ("enforcement:
	// static"): anything Analyze cannot see (a program name computed at run
	// time, a non-literal eval or trap, source, aliases, a path argument or
	// redirect target built from ~, a parameter or a substitution, a cd it
	// cannot follow, a .. after a glob, a computed subscript, arithmetic over
	// a value not proven to be an integer) is refused rather than left to the
	// runner, and so is a program whose profile needs run-time hardening
	// (Profile.Harden or Profile.Env). Glob arguments are expanded and each
	// name they match is path-checked.
	Static bool
}

// maxNesting bounds how deep literal eval and trap arguments are parsed.
const maxNesting = 4

// implementedBuiltins are the builtins mvdan.cc/sh's interpreter (v3.14.1)
// implements. interp.IsBuiltin also names builtins it refuses at run time
// ("unsupported builtin"), such as kill, umask and ulimit; Analyze refuses
// those up front. TestImplementedBuiltinsMatchInterpreter keeps this in step
// with the interpreter.
var implementedBuiltins = map[string]bool{
	":": true, "true": true, "false": true, "help": true, "times": true, "exit": true,
	"set": true, "shift": true, "unset": true, "echo": true, "printf": true, "break": true,
	"continue": true, "pwd": true, "cd": true, "wait": true, "builtin": true, "type": true,
	"hash": true, "eval": true, "source": true, ".": true, "[": true, "test": true,
	"exec": true, "command": true, "dirs": true, "pushd": true, "popd": true, "return": true,
	"read": true, "getopts": true, "shopt": true, "alias": true, "unalias": true, "trap": true,
	"readarray": true, "mapfile": true,
}

// Analyze judges command under p before it runs. Allow means every part is
// within the policy as far as the command text shows; Ask means some literal
// part needs a person's approval (Grant says what approving allows); Deny
// means the command is refused outright.
func (p *Policy) Analyze(command string, opt Options) Verdict {
	return p.analyze(command, nil, opt)
}

// analyze is Analyze for a call that also puts the variables env in the
// command's environment (shell_execute's env parameter): each is judged like
// an assignment before the command.
func (p *Policy) analyze(command string, env []string, opt Options) Verdict {
	a := &analyzer{p: p, opt: opt, funcDecls: map[string][]int{}, unsetFuncs: map[string]bool{}, grant: &Grant{},
		intDefs: map[string][]int{}, unsafeInts: map[string]bool{}}
	for _, name := range env {
		switch {
		case name == "CDPATH" && opt.Static:
			a.findings = append(a.findings, Finding{Verdict: Deny, What: name,
				Why: "sets CDPATH, which changes where cd goes in a way static enforcement does not follow"})
		case ExecEnvName(name):
			a.findings = append(a.findings, Finding{Verdict: Ask, What: name,
				Why: "sets " + name + ", which changes what programs run or load"})
			a.grant.add("env", name)
		}
	}
	a.source(command, 0, nil)
	a.finish()
	sort.SliceStable(a.findings, func(i, j int) bool {
		fi, fj := a.findings[i], a.findings[j]
		return fi.Line < fj.Line || (fi.Line == fj.Line && fi.Col < fj.Col)
	})
	v := Verdict{Kind: Allow, Policy: p.Name, Findings: a.findings}
	for _, f := range a.findings {
		if f.Verdict > v.Kind {
			v.Kind = f.Verdict
		}
	}
	if v.Kind == Ask && !a.grant.empty() {
		v.Grant = a.grant
	}
	return v
}

type analyzer struct {
	p        *Policy
	opt      Options
	findings []Finding
	grant    *Grant

	// funcDecls holds, for each function name, the end offsets of its
	// declarations that run unconditionally: direct statements of the
	// top-level command, not in the background.
	funcDecls map[string][]int
	// funcCalls are calls judged as function calls; finish judges them as
	// commands when something in the command may unset the function.
	funcCalls []funcCall
	// unsetFuncs are the names an unset may remove as a function;
	// unsetAnyFunc is set when an unset names a word computed at run time.
	unsetFuncs   map[string]bool
	unsetAnyFunc bool

	// paths and cds are judged by finishPaths, once every directory the
	// command can change to is known.
	paths  []pathCheck
	cds    []cdTarget
	globs  []globCheck
	dirOps []dirOperand
	// repoChecks are calls whose profile judges the directory they run in
	// (Profile.RepoCheck), judged from every directory the command can be in.
	repoChecks []repoCheck
	// tooManyCds is set once the command has more than maxCdTargets cd or
	// pushd targets; the targets past the limit are not recorded.
	tooManyCds bool

	// What static enforcement knows about the variables arithmetic reads:
	// intDefs are the end offsets of top-level integer assignments,
	// intRegions the for loops that hold an integer in their variable, and
	// unsafeInts the names the command may assign anything else.
	intDefs    map[string][]int
	intRegions []intRegion
	unsafeInts map[string]bool
	arithReads []arithRead
}

// funcCall is a call to a declared function, kept to judge it as a command
// when the function may have been unset.
type funcCall struct {
	x      *syntax.CallExpr
	args   []*syntax.Word
	name   string
	depth  int
	outer  *syntax.Pos
	judged bool
}

// offset is where n runs, in the coordinates of the top-level command: the
// node's own offset, or that of the outer eval/trap.
func offset(n syntax.Node, outer *syntax.Pos) int {
	if outer != nil {
		return int(outer.Offset())
	}
	return int(n.Pos().Offset())
}

// isFunc reports whether a call to name at off runs a function: a
// declaration of it ran unconditionally before off.
func (a *analyzer) isFunc(name string, off int) bool {
	for _, end := range a.funcDecls[name] {
		if end <= off {
			return true
		}
	}
	return false
}

// finish judges what had to wait for the whole command: function calls whose
// function an unset may remove, then paths, which depend on every cd.
func (a *analyzer) finish() {
	for changed := true; changed; {
		changed = false
		for i := range a.funcCalls {
			c := &a.funcCalls[i]
			if c.judged || (!a.unsetAnyFunc && !a.unsetFuncs[c.name]) {
				continue
			}
			c.judged, changed = true, true
			a.command(c.x, c.args, c.name, c.depth, c.outer)
		}
	}
	a.finishPaths()
	a.finishArith()
}

// at is where findings are reported: the node's own position, or the outer
// eval/trap when parsing its literal argument.
func (a *analyzer) at(n syntax.Node, outer *syntax.Pos) (uint, uint) {
	if outer != nil {
		return outer.Line(), outer.Col()
	}
	if n == nil {
		return 1, 1
	}
	p := n.Pos()
	return p.Line(), p.Col()
}

func (a *analyzer) add(v VerdictKind, n syntax.Node, outer *syntax.Pos, what, why string) {
	line, col := a.at(n, outer)
	a.findings = append(a.findings, Finding{Verdict: v, What: what, Why: why, Line: line, Col: col})
}

func (a *analyzer) ask(n syntax.Node, outer *syntax.Pos, what, why, grantKind, grantItem string) {
	a.add(Ask, n, outer, what, why)
	a.grant.add(grantKind, grantItem)
}

func (a *analyzer) source(src string, depth int, outer *syntax.Pos) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		a.add(Deny, nil, outer, "the command", "does not parse as bash: "+err.Error())
		return
	}
	if depth == 0 {
		// Only a declaration that runs whatever happens defines a function
		// for the calls after it: not one in a branch, a loop, a subshell, an
		// and-or list, the background or an eval string.
		for _, st := range file.Stmts {
			if fd, ok := st.Cmd.(*syntax.FuncDecl); ok && fd.Name != nil && !st.Background && !st.Coprocess && !st.Negated {
				a.funcDecls[fd.Name.Value] = append(a.funcDecls[fd.Name.Value], int(st.End().Offset()))
			}
		}
		a.topLevelInts(file)
	}
	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CoprocClause:
			a.add(Deny, x, outer, "coproc", "not supported by the jailed shell")
		case *syntax.TestDecl:
			a.add(Deny, x, outer, "@test", "not supported by the jailed shell")
		case *syntax.Stmt:
			a.redirects(x, outer)
		case *syntax.CallExpr:
			a.assigns(x, outer)
			a.call(x, depth, outer)
		case *syntax.DeclClause:
			a.decl(x, outer)
		case *syntax.WordIter:
			if x.Name != nil {
				a.setsVar(x, outer, x.Name.Value) // for NAME in ...; select NAME in ...
			}
		case *syntax.ParamExp:
			if x.Param != nil && x.Exp != nil && (x.Exp.Op == syntax.AssignUnset || x.Exp.Op == syntax.AssignUnsetOrNull) {
				a.setsVar(x, outer, x.Param.Value) // ${NAME=value}, ${NAME:=value}
			}
			a.promptExpansion(x, outer)
			a.paramArith(x, outer)
		case *syntax.ArithmExp:
			a.arith(x.X, outer)
		case *syntax.ArithmCmd:
			a.arith(x.X, outer)
		case *syntax.LetClause:
			for _, e := range x.Exprs {
				a.arith(e, outer)
			}
		case *syntax.ForClause:
			a.forLoop(x, depth, outer)
		case *syntax.Assign:
			if x.Index != nil {
				a.arith(x.Index, outer) // a[i]=v
			}
		case *syntax.ArrayElem:
			if x.Index != nil {
				a.arith(x.Index, outer) // a=([i]=v)
			}
		case *syntax.BinaryTest:
			a.arithTest(x, outer)
		case *syntax.UnaryTest:
			if w, ok := x.X.(*syntax.Word); ok && x.Op == syntax.TsVarSet {
				a.varNameOperand(x, outer, w) // [[ -v NAME ]]
			}
		case *syntax.BinaryArithm:
			if isArithAssign(x.Op) {
				a.arithAssign(x.X, outer)
			}
		case *syntax.UnaryArithm:
			if x.Op == syntax.Inc || x.Op == syntax.Dec {
				a.arithAssign(x.X, outer)
			}
		}
		return true
	})
}

func (a *analyzer) call(x *syntax.CallExpr, depth int, outer *syntax.Pos) {
	args := x.Args
	if len(args) == 0 {
		return // assignments only (judged by assigns); their words are walked for substitutions
	}
	name, lit := literal(args[0])
	prefixed := false // command, exec and builtin skip functions
	for lit && (name == "command" || name == "exec" || name == "builtin") {
		if len(args) == 1 {
			return // bare exec (redirects only), command or builtin: starts nothing
		}
		if name == "command" {
			next, ok := literal(args[1])
			if ok && strings.HasPrefix(next, "-") {
				if next == "-v" || next == "-V" {
					return // only reports what a name is
				}
				a.add(Deny, x, outer, "command "+next, "not supported by the jailed shell")
				return
			}
		}
		args = args[1:]
		name, lit = literal(args[0])
		prefixed = true
	}
	if !lit {
		if a.opt.Static {
			a.add(Deny, args[0], outer, wordText(args[0]), "a command name computed at run time cannot be checked")
		}
		return
	}
	if !prefixed && a.isFunc(name, offset(x, outer)) {
		// Its body is judged where it is declared. finish judges the call as
		// a command if the function may be unset first.
		a.funcCalls = append(a.funcCalls, funcCall{x: x, args: args, name: name, depth: depth, outer: outer})
		return
	}
	a.command(x, args, name, depth, outer)
}

// command judges a call whose name, args[0], is the literal name and is not
// a function.
func (a *analyzer) command(x *syntax.CallExpr, args []*syntax.Word, name string, depth int, outer *syntax.Pos) {
	rest := args[1:]
	if filepath.Base(name) == "env" {
		a.envArgs(args[0], rest, outer)
	}
	switch {
	case interp.IsBuiltin(name):
		a.builtin(x, name, rest, depth, outer)
	case strings.ContainsRune(name, '/'):
		if a.p.Never[filepath.Base(name)] {
			a.add(Deny, args[0], outer, name, "never allowed by the shell policy")
			return
		}
		a.ask(args[0], outer, name, "runs a program by path", "program", name)
	case a.p.Never[name]:
		a.add(Deny, args[0], outer, name, "never allowed by the shell policy")
	default:
		prof, ok := a.p.Programs[name]
		if !ok {
			a.ask(args[0], outer, name, "not on the shell allowlist", "program", name)
			return
		}
		a.program(x, args[0], name, prof, rest, outer)
	}
}

func (a *analyzer) program(x *syntax.CallExpr, nameWord *syntax.Word, name string, prof *Profile, rest []*syntax.Word, outer *syntax.Pos) {
	if a.opt.Static && (prof.Harden != nil || len(prof.Env) > 0) {
		// The profile is only safe with the hardening a runner applies at
		// launch (git's -c overrides and environment); static has no runner.
		a.add(Deny, nameWord, outer, name, name+" needs the runner's hardening, which static enforcement does not apply")
		return
	}
	lits := make([]string, 0, len(rest))
	complete := true
	globbed := false
	for _, w := range rest {
		s, ok := literal(w)
		if !ok {
			complete = false
			if prof.NoPathArgs {
				continue
			}
			if dir, ok := globDir(w); ok {
				globbed = true
				a.readPath(dir, w, outer)
				if a.opt.Static {
					a.staticGlob(w, outer)
					if prof.NoDirOperands {
						a.dirOperand(globPattern(w), true, name, w, outer)
					}
				}
			} else if a.opt.Static {
				a.add(Deny, w, outer, wordText(w), "an argument computed at run time (tilde, parameter or command substitution) cannot be path-checked")
			}
			continue
		}
		lits = append(lits, s)
	}
	if v := prof.violation(lits, complete); v != "" {
		a.ask(nameWord, outer, name, v, "program", name)
		return
	}
	if prof.RepoCheck != nil && complete {
		a.repoChecks = append(a.repoChecks, repoCheck{check: prof.RepoCheck, args: lits, prog: name, n: nameWord, outer: outer})
	}
	if a.opt.Static && globbed && prof.MaxPositional >= 0 {
		// No runner counts the names the glob expands to.
		a.ask(nameWord, outer, name, "a glob may expand to more than "+itoa(prof.MaxPositional)+
			" file argument(s) (the next would be written)", "program", name)
		return
	}
	if prof.NoPathArgs {
		return
	}
	for _, c := range prof.pathCandidates(lits) {
		a.readPath(c, x, outer)
		if prof.NoDirOperands {
			a.dirOperand(c, false, name, x, outer)
		}
	}
}

func (a *analyzer) builtin(x *syntax.CallExpr, name string, rest []*syntax.Word, depth int, outer *syntax.Pos) {
	if !implementedBuiltins[name] {
		a.add(Deny, x, outer, name, "not supported by the jailed shell")
		return
	}
	a.varBuiltin(x, name, rest, outer)
	if name == "unset" {
		a.unsetNames(x, rest, outer)
	}
	if !a.supportedOptions(x, name, rest, outer) {
		return
	}
	if !a.p.Builtins[name] {
		if a.opt.Static && (name == "source" || name == "." || name == "alias" || name == "shopt") {
			a.add(Deny, x, outer, name, "runs commands the static check cannot see")
			return
		}
		a.ask(x, outer, name, "a shell builtin outside the policy", "builtin", name)
		return
	}
	switch name {
	case "eval":
		a.nested(x, dropDashes(rest), depth, outer)
	case "trap":
		action := dropDashes(rest)
		if len(action) == 0 {
			break
		}
		if s, ok := literal(action[0]); ok && (s == "-" || s == "-p" || s == "-l" || s == "-P") {
			break // a reset, or a listing: no code
		}
		a.nested(x, action[:1], depth, outer)
	case "cd", "pushd":
		a.chdir(x, name, rest, outer)
	case "popd":
		if a.opt.Static {
			a.add(Deny, x, outer, name, "returns to a directory from the stack, which static enforcement does not follow")
		}
	}
}

// unsetNames records the functions an unset may remove: with -f, or with no
// -v, every name (unset NAME removes a function when no variable has that
// name). A word computed at run time may name any function.
func (a *analyzer) unsetNames(x *syntax.CallExpr, words []*syntax.Word, outer *syntax.Pos) {
	varsOnly := false
	afterDashes := false
	for _, w := range words {
		s, ok := literal(w)
		if !ok {
			a.unsetAnyFunc = true
			continue
		}
		if !afterDashes && strings.HasPrefix(s, "-") && len(s) > 1 {
			if s == "--" {
				afterDashes = true
				continue
			}
			if strings.ContainsRune(s, 'f') {
				varsOnly = false
			} else if strings.ContainsRune(s, 'v') {
				varsOnly = true
			}
			continue
		}
		a.subscriptName(x, outer, s)
		if !varsOnly {
			a.unsetFuncs[s] = true
		}
	}
}

// promptExpansion judges the @P operator (${x@P}): bash expands the value as
// a prompt string, which runs the command substitutions in it, so a value the
// analyzer never parsed as code runs. Static enforcement refuses it. The
// jailed shell leaves the value as it is and starts nothing
// (TestInterpreter_PromptExpansionRunsNothing), so runtime gives no verdict.
func (a *analyzer) promptExpansion(x *syntax.ParamExp, outer *syntax.Pos) {
	if !a.opt.Static || x.Exp == nil || x.Exp.Op != syntax.OtherParamOps {
		return
	}
	if op, ok := literal(x.Exp.Word); ok && op != "P" {
		return // @Q, @E, @A, @a, @U, @u, @L, @K, @k run nothing
	}
	a.add(Deny, x, outer, "${"+paramName(x)+"@P}", "prompt expansion runs the command substitutions in the value, which static enforcement cannot see")
}

// dropDashes drops the "--" that may end eval's and trap's options.
func dropDashes(words []*syntax.Word) []*syntax.Word {
	if len(words) > 0 {
		if s, ok := literal(words[0]); ok && s == "--" {
			return words[1:]
		}
	}
	return words
}

// supportedOptions refuses the builtin options that run code or change what
// runs, which the jailed shell does not implement: mapfile/readarray -C (a
// callback evaluated as code) and set -k / set -o keyword (every NAME=value
// argument becomes an environment assignment of the command). Under static
// a computed set option may be either. It reports whether the call may be
// judged further.
func (a *analyzer) supportedOptions(x *syntax.CallExpr, name string, rest []*syntax.Word, outer *syntax.Pos) bool {
	switch name {
	case "mapfile", "readarray":
		for _, w := range rest {
			s, ok := literal(w)
			if !ok || s == "--" {
				break
			}
			if !strings.HasPrefix(s, "-") {
				continue
			}
			for _, c := range s[1:] {
				if c == 'C' {
					a.add(Deny, x, outer, name+" -C", "runs a callback as code; not supported by the jailed shell")
					return false
				}
				if strings.ContainsRune("dnOsuc", c) {
					break // the rest is the option's value
				}
			}
		}
	case "set":
		for i := 0; i < len(rest); i++ {
			s, ok := literal(rest[i])
			if !ok {
				if a.opt.Static {
					a.add(Deny, x, outer, "set "+wordText(rest[i]), "a set option computed at run time may be -k, which static enforcement cannot follow")
					return false
				}
				return true
			}
			if s == "--" || s == "-" || !strings.HasPrefix(s, "-") {
				return true // the rest are positional parameters ("+o" turns options off)
			}
			for _, c := range s[1:] {
				switch c {
				case 'k':
					a.add(Deny, x, outer, "set -k", "turns NAME=value arguments into environment assignments; not supported by the jailed shell")
					return false
				case 'o':
					if i+1 < len(rest) {
						i++
						v, ok := literal(rest[i])
						if !ok && a.opt.Static {
							a.add(Deny, x, outer, "set -o "+wordText(rest[i]), "a set option computed at run time may be keyword, which static enforcement cannot follow")
							return false
						}
						if ok && v == "keyword" {
							a.add(Deny, x, outer, "set -o keyword", "turns NAME=value arguments into environment assignments; not supported by the jailed shell")
							return false
						}
					}
				}
			}
		}
	}
	return true
}

// nested parses the literal argument of eval or trap as a command of its own.
func (a *analyzer) nested(x *syntax.CallExpr, words []*syntax.Word, depth int, outer *syntax.Pos) {
	parts := make([]string, 0, len(words))
	for _, w := range words {
		s, ok := literal(w)
		if !ok {
			if a.opt.Static {
				a.add(Deny, x, outer, wordText(w), "code computed at run time cannot be checked")
			}
			return
		}
		parts = append(parts, s)
	}
	if depth+1 > maxNesting {
		a.add(Deny, x, outer, "eval", "nested too deeply to check")
		return
	}
	pos := x.Pos()
	if outer != nil {
		pos = *outer
	}
	a.source(strings.Join(parts, " "), depth+1, &pos)
}

func (a *analyzer) redirects(st *syntax.Stmt, outer *syntax.Pos) {
	for _, r := range st.Redirs {
		if r.N != nil {
			if fd, err := strconv.Atoi(r.N.Value); err != nil || fd > 2 {
				a.add(Deny, r, outer, r.N.Value+r.Op.String(), "redirects on file descriptors above 2 are not supported by the jailed shell")
				continue
			}
		}
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
			continue
		case syntax.DplIn, syntax.DplOut:
			target, ok := literal(r.Word)
			if !ok {
				if a.opt.Static {
					a.add(Deny, r, outer, wordText(r.Word), "redirects to a path computed at run time, which cannot be checked")
				}
				continue
			}
			if target == "-" {
				continue
			}
			if fd, err := strconv.Atoi(target); err == nil {
				if fd > 2 {
					a.add(Deny, r, outer, r.Op.String()+target, "redirects on file descriptors above 2 are not supported by the jailed shell")
				}
				continue
			}
			if r.Op == syntax.DplOut {
				a.writePath(target, r, outer)
			} else {
				a.readPath(target, r, outer)
			}
		case syntax.RdrIn:
			if target, ok := literal(r.Word); ok {
				a.readPath(target, r, outer)
			} else if a.opt.Static {
				a.add(Deny, r, outer, wordText(r.Word), "reads a path computed at run time, which cannot be checked")
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
			if target, ok := literal(r.Word); ok {
				a.writePath(target, r, outer)
			} else if a.opt.Static {
				a.add(Deny, r, outer, wordText(r.Word), "writes to a path computed at run time, which cannot be checked")
			}
		default:
			a.add(Deny, r, outer, r.Op.String(), "not supported by the jailed shell")
		}
	}
}

// devicePaths are always allowed as redirect targets and arguments.
var devicePaths = map[string]bool{"/dev/null": true, "/dev/stdin": true, "/dev/stdout": true, "/dev/stderr": true}

// netDevice reports whether bash opens a network connection for path: a
// redirect to /dev/tcp/HOST/PORT or /dev/udp/HOST/PORT, which need not exist
// on disk.
func netDevice(path string) bool {
	for _, p := range []string{path, filepath.Clean(path)} {
		if strings.HasPrefix(p, "/dev/tcp/") || strings.HasPrefix(p, "/dev/udp/") {
			return true
		}
	}
	return false
}

// literal returns the word's value when it is fully literal: no parameter,
// command, arithmetic or process expansion, no glob, brace or tilde expansion,
// and no $'...' or $"..." quoting.
func literal(w *syntax.Word) (string, bool) {
	if w == nil || len(w.Parts) == 0 {
		return "", false
	}
	var b strings.Builder
	for i, part := range w.Parts {
		switch x := part.(type) {
		case *syntax.Lit:
			v := x.Value
			if strings.ContainsAny(v, "*?") || opensBracket(w.Parts, i, v) || hasBraceExpansion(v) || (i == 0 && strings.HasPrefix(v, "~")) {
				return "", false
			}
			b.WriteString(unescapeUnquoted(v))
		case *syntax.SglQuoted:
			if x.Dollar {
				return "", false
			}
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			if x.Dollar {
				return "", false
			}
			for _, p := range x.Parts {
				lit, ok := p.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unescapeDouble(lit.Value))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// opensBracket reports whether a "[" in v, the unquoted part i of a word,
// starts a glob bracket expression: bash needs a "]" after it ("[" alone, as
// in the test command, is literal). A part that is not literal text may hold
// the "]".
func opensBracket(parts []syntax.WordPart, i int, v string) bool {
	k := strings.IndexByte(v, '[')
	if k < 0 {
		return false
	}
	if strings.IndexByte(v[k+1:], ']') >= 0 {
		return true
	}
	for _, p := range parts[i+1:] {
		switch x := p.(type) {
		case *syntax.Lit:
			if strings.Contains(x.Value, "]") {
				return true
			}
		case *syntax.SglQuoted:
			if strings.Contains(x.Value, "]") {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func hasBraceExpansion(v string) bool {
	open := strings.IndexByte(v, '{')
	if open < 0 {
		return false
	}
	inner := v[open:]
	return strings.Contains(inner, "}") && (strings.Contains(inner, ",") || strings.Contains(inner, ".."))
}

// globDir returns the literal directory a glob word lists ("/etc" for
// /etc/pass*), when the word's only expansion is a glob.
func globDir(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var prefix strings.Builder
	sawGlob := false
	for i, part := range w.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		v := lit.Value
		if i == 0 && strings.HasPrefix(v, "~") {
			return "", false
		}
		if idx := strings.IndexAny(v, "*?["); idx >= 0 {
			prefix.WriteString(unescapeUnquoted(v[:idx]))
			sawGlob = true
			break
		}
		prefix.WriteString(unescapeUnquoted(v))
	}
	if !sawGlob {
		return "", false
	}
	p := prefix.String()
	if !strings.Contains(p, "/") {
		return ".", true
	}
	return filepath.Dir(p + "x"), true
}

func unescapeUnquoted(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
			if v[i] == '\n' {
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func unescapeDouble(v string) string {
	if !strings.ContainsRune(v, '\\') {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) && strings.ContainsRune("$`\"\\\n", rune(v[i+1])) {
			i++
			if v[i] == '\n' {
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// wordText renders a word back to source for a reason string.
func wordText(w *syntax.Word) string {
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, w); err != nil {
		return "the command name"
	}
	return b.String()
}
