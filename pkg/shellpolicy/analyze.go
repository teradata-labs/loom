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
	"os"
	"path/filepath"
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
	// literal eval or trap argument are reported at that eval or trap.
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
		if f.Verdict == v.Kind {
			parts = append(parts, fmt.Sprintf("%s (%d:%d): %s", f.What, f.Line, f.Col, f.Why))
		}
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
	// time, a non-literal eval or trap, source, aliases) is refused rather than
	// left to the runner.
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
	a := &analyzer{p: p, opt: opt, funcs: map[string]bool{}, grant: &Grant{}}
	a.source(command, 0, nil)
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
	funcs    map[string]bool
	findings []Finding
	grant    *Grant
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
	syntax.Walk(file, func(n syntax.Node) bool {
		if fd, ok := n.(*syntax.FuncDecl); ok && fd.Name != nil {
			a.funcs[fd.Name.Value] = true
		}
		return true
	})
	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CoprocClause:
			a.add(Deny, x, outer, "coproc", "not supported by the jailed shell")
		case *syntax.TestDecl:
			a.add(Deny, x, outer, "@test", "not supported by the jailed shell")
		case *syntax.Stmt:
			a.redirects(x, outer)
		case *syntax.CallExpr:
			a.call(x, depth, outer)
		}
		return true
	})
}

func (a *analyzer) call(x *syntax.CallExpr, depth int, outer *syntax.Pos) {
	args := x.Args
	if len(args) == 0 {
		return // assignments only; their words are walked for substitutions
	}
	name, lit := literal(args[0])
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
	}
	if !lit {
		if a.opt.Static {
			a.add(Deny, args[0], outer, wordText(args[0]), "a command name computed at run time cannot be checked")
		}
		return
	}
	rest := args[1:]
	switch {
	case a.funcs[name]:
		return
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
	lits := make([]string, 0, len(rest))
	complete := true
	for _, w := range rest {
		s, ok := literal(w)
		if !ok {
			complete = false
			if dir, ok := globDir(w); ok && !prof.NoPathArgs {
				a.checkRead(dir, w, outer)
			}
			continue
		}
		lits = append(lits, s)
	}
	if v := prof.violation(lits, complete); v != "" {
		a.ask(nameWord, outer, name, v, "program", name)
		return
	}
	if prof.NoPathArgs {
		return
	}
	for _, c := range pathCandidates(lits) {
		a.checkRead(c, x, outer)
	}
}

func (a *analyzer) builtin(x *syntax.CallExpr, name string, rest []*syntax.Word, depth int, outer *syntax.Pos) {
	if !implementedBuiltins[name] {
		a.add(Deny, x, outer, name, "not supported by the jailed shell")
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
		a.nested(x, rest, depth, outer)
	case "trap":
		if len(rest) > 0 {
			a.nested(x, rest[:1], depth, outer)
		}
	case "cd", "pushd":
		for _, w := range rest {
			s, ok := literal(w)
			if !ok || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
				continue
			}
			a.checkRead(s, w, outer)
		}
	}
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
				a.checkWrite(target, r, outer)
			} else {
				a.checkRead(target, r, outer)
			}
		case syntax.RdrIn:
			if target, ok := literal(r.Word); ok {
				a.checkRead(target, r, outer)
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
			if target, ok := literal(r.Word); ok {
				a.checkWrite(target, r, outer)
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

func (a *analyzer) checkRead(path string, n syntax.Node, outer *syntax.Pos) {
	if path == "" || devicePaths[path] || strings.HasPrefix(path, "~") {
		return
	}
	full := ResolvePath(a.opt.WorkingDir, path)
	if _, err := os.Stat(full); err != nil {
		return // only an existing path can be read
	}
	if inRoots(full, a.opt.Roots.Read) {
		return
	}
	a.ask(n, outer, path, "reads outside the session", "read", full)
}

func (a *analyzer) checkWrite(path string, n syntax.Node, outer *syntax.Pos) {
	if devicePaths[path] {
		return
	}
	full := ResolvePath(a.opt.WorkingDir, path)
	if inRoots(full, a.opt.Roots.Write) {
		return
	}
	a.add(Deny, n, outer, path, "writes outside the session")
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
			if strings.ContainsAny(v, "*?[") || hasBraceExpansion(v) || (i == 0 && strings.HasPrefix(v, "~")) {
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
