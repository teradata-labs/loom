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

	"mvdan.cc/sh/v3/syntax"
)

// Bash evaluates an array subscript as arithmetic, and an arithmetic
// subscript runs the command substitutions in it. A subscript reaches bash
// both in a variable name (printf -v 'a[$(cmd)]', read, test -v, declare, a
// name reference) and in any value arithmetic reads: x='a[$(cmd)]';
// echo $(( x )) runs cmd, and so does [[ $x -eq 1 ]]. The runner sees each
// launch such a substitution makes, so these checks apply only under static
// enforcement:
//
//   - a variable name whose subscript is not an integer, @ or * is refused;
//   - arithmetic may read only a variable proven to hold an integer: one
//     that a top-level statement assigned an integer before the read (or the
//     enclosing for loop does), and that nothing in the command assigns
//     anything else;
//   - arithmetic over anything else (a command substitution, a positional
//     parameter, ${!x}) is refused, and so is declare -i, which makes every
//     later assignment arithmetic.

// intLiteral matches an integer constant of bash arithmetic: decimal, octal,
// hexadecimal (0x1f) or base#digits (2#101).
var intLiteral = regexp.MustCompile(`^-?(0[xX][0-9a-fA-F]+|[0-9]+(#[0-9A-Za-z@_]+)?)$`)

// safeSubscriptName matches a variable name with a constant subscript.
var safeSubscriptName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\[(-?[0-9]+|@|\*)\]$`)

// shellSetVars are variables bash itself assigns from data while the command
// runs, so an earlier integer assignment proves nothing about them.
var shellSetVars = map[string]bool{
	"REPLY": true, "MAPFILE": true, "OPTARG": true, "BASH_REMATCH": true, "PWD": true,
	"OLDPWD": true, "DIRSTACK": true, "_": true, "BASH_ARGV": true, "BASH_ARGC": true,
	"BASH_ARGV0": true, "BASH_SOURCE": true, "BASH_LINENO": true, "FUNCNAME": true,
	"BASH_COMMAND": true, "BASH_EXECUTION_STRING": true, "COPROC": true,
}

// arithRead is a variable that arithmetic reads, judged by finishArith.
type arithRead struct {
	name  string
	off   int
	n     syntax.Node
	outer *syntax.Pos
}

// intRegion is a for loop whose variable holds an integer inside it.
type intRegion struct {
	name       string
	start, end int
}

// subscriptName refuses, under static, a variable name whose subscript bash
// would evaluate as arithmetic.
func (a *analyzer) subscriptName(n syntax.Node, outer *syntax.Pos, name string) {
	if a.opt.Static && strings.ContainsRune(name, '[') && !safeSubscriptName.MatchString(name) {
		a.add(Deny, n, outer, name, "a subscript in a variable name is evaluated as arithmetic, which can run commands static enforcement cannot see")
	}
}

// varNameOperand judges the operand of test -v, [ -v and [[ -v.
func (a *analyzer) varNameOperand(n syntax.Node, outer *syntax.Pos, w *syntax.Word) {
	if !a.opt.Static {
		return
	}
	s, ok := literal(w)
	if !ok {
		a.add(Deny, n, outer, wordText(w), "a variable name computed at run time may hold a subscript, which is evaluated as arithmetic")
		return
	}
	a.subscriptName(n, outer, s)
}

// unsafeInt records that the command may assign name something other than
// an integer.
func (a *analyzer) unsafeInt(name string) {
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	a.unsafeInts[name] = true
}

// intWord reports whether w's value is an integer whatever runs: a literal
// integer, an arithmetic expansion (checked on its own), $#, $?, $$, $! or a
// length ${#x}.
func intWord(w *syntax.Word) bool {
	if s, ok := literal(w); ok {
		return intLiteral.MatchString(s)
	}
	switch p := singlePart(w).(type) {
	case *syntax.ArithmExp:
		return true
	case *syntax.ParamExp:
		return specialIntParam(p) || (p.Length && p.Param != nil && !p.Excl && p.Slice == nil && p.Repl == nil && p.Exp == nil)
	}
	return false
}

// singlePart returns the word's only part, looking inside double quotes, or
// nil.
func singlePart(w *syntax.Word) syntax.WordPart {
	if w == nil || len(w.Parts) != 1 {
		return nil
	}
	if dq, ok := w.Parts[0].(*syntax.DblQuoted); ok {
		if len(dq.Parts) != 1 {
			return nil
		}
		return dq.Parts[0]
	}
	return w.Parts[0]
}

// plainParam reports whether p is $name or ${name}, or ${name[i]}: no
// operator that changes the value.
func plainParam(p *syntax.ParamExp) bool {
	return p.Param != nil && !p.Excl && !p.Length && !p.Width && !p.IsSet && p.Flags == nil &&
		p.NestedParam == nil && len(p.Modifiers) == 0 && p.Slice == nil && p.Repl == nil &&
		p.Names == 0 && p.Exp == nil
}

// specialIntParam reports whether p is $#, $?, $$ or $!, which bash sets to
// integers.
func specialIntParam(p *syntax.ParamExp) bool {
	if !plainParam(p) || p.Index != nil {
		return false
	}
	switch p.Param.Value {
	case "#", "?", "$", "!":
		return true
	}
	return false
}

func atOrStar(w syntax.ArithmExpr) bool {
	word, ok := w.(*syntax.Word)
	if !ok {
		return false
	}
	s, ok := literal(word)
	return ok && (s == "@" || s == "*")
}

// intAssign reports whether an assignment gives its variable an integer.
func intAssign(as *syntax.Assign) bool {
	if as.Array != nil {
		for _, el := range as.Array.Elems {
			if el.Value != nil && !intWord(el.Value) {
				return false
			}
		}
		return true
	}
	return as.Value == nil || intWord(as.Value)
}

// arith judges an arithmetic expression: every word in it must be a literal
// integer, an arithmetic expansion, an integer special parameter or length,
// or a variable read that finishArith proves holds an integer.
func (a *analyzer) arith(e syntax.ArithmExpr, outer *syntax.Pos) {
	if !a.opt.Static || e == nil {
		return
	}
	switch x := e.(type) {
	case *syntax.BinaryArithm:
		if x.Op != syntax.Assgn {
			a.arith(x.X, outer) // compound assignments (+= ...) read their target
		}
		a.arith(x.Y, outer)
	case *syntax.UnaryArithm:
		a.arith(x.X, outer)
	case *syntax.ParenArithm:
		a.arith(x.X, outer)
	case *syntax.Word:
		a.arithWord(x, outer)
	default:
		a.add(Deny, e, outer, "arithmetic", "arithmetic static enforcement cannot see")
	}
}

func (a *analyzer) arithWord(w *syntax.Word, outer *syntax.Pos) {
	if s, ok := literal(w); ok {
		switch {
		case intLiteral.MatchString(s):
		case syntax.ValidName(s):
			a.arithReads = append(a.arithReads, arithRead{name: s, off: offset(w, outer), n: w, outer: outer})
		default:
			a.add(Deny, w, outer, s, "arithmetic over a value static enforcement cannot see; bash evaluates any subscript in it, which can run commands")
		}
		return
	}
	switch p := singlePart(w).(type) {
	case *syntax.ArithmExp:
		return // judged as an expression of its own
	case *syntax.ParamExp:
		if intWord(w) {
			return
		}
		// $x, ${x} and, in arithmetic, a[i]: the subscript is judged where
		// the walk meets the parameter expansion.
		if plainParam(p) && syntax.ValidName(p.Param.Value) {
			a.arithReads = append(a.arithReads, arithRead{name: p.Param.Value, off: offset(w, outer), n: w, outer: outer})
			return
		}
	}
	a.add(Deny, w, outer, wordText(w), "arithmetic over a value static enforcement cannot see; bash evaluates any subscript in it, which can run commands")
}

// arithTest judges [[ X -eq Y ]] and the other arithmetic comparisons, whose
// operands bash evaluates as arithmetic.
func (a *analyzer) arithTest(x *syntax.BinaryTest, outer *syntax.Pos) {
	switch x.Op {
	case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
	default:
		return
	}
	for _, operand := range []syntax.TestExpr{x.X, x.Y} {
		if w, ok := operand.(*syntax.Word); ok {
			a.arith(w, outer)
		} else if a.opt.Static {
			a.add(Deny, x, outer, "[[", "arithmetic static enforcement cannot see")
		}
	}
}

// paramArith judges the arithmetic inside a parameter expansion: an index
// (${a[i]}), a slice (${s:i:n}) and an indirect expansion (${!x}), whose
// value names a variable that may carry a subscript.
func (a *analyzer) paramArith(x *syntax.ParamExp, outer *syntax.Pos) {
	if !a.opt.Static {
		return
	}
	if x.Excl && x.Names == 0 && (x.Index == nil || !atOrStar(x.Index)) {
		a.add(Deny, x, outer, "${!"+paramName(x)+"}", "an indirect expansion names a variable at run time, and bash evaluates any subscript in that name")
	}
	if x.Index != nil && !atOrStar(x.Index) {
		a.arith(x.Index, outer)
	}
	if x.Slice != nil {
		a.arith(x.Slice.Offset, outer)
		a.arith(x.Slice.Length, outer)
	}
	if x.Param != nil && x.Exp != nil && (x.Exp.Op == syntax.AssignUnset || x.Exp.Op == syntax.AssignUnsetOrNull) && !intWord(x.Exp.Word) {
		a.unsafeInt(x.Param.Value)
	}
}

func paramName(x *syntax.ParamExp) string {
	if x.Param == nil {
		return ""
	}
	return x.Param.Value
}

// forLoop records what a for loop proves about its variables: a C-style
// loop's init assigns integers, and a for ... in loop over literal integers
// holds an integer inside its body. Any other loop variable is unsafe.
func (a *analyzer) forLoop(x *syntax.ForClause, depth int, outer *syntax.Pos) {
	if !a.opt.Static {
		return
	}
	start, end := int(x.Pos().Offset()), int(x.End().Offset())
	switch l := x.Loop.(type) {
	case *syntax.CStyleLoop:
		a.arith(l.Init, outer)
		a.arith(l.Cond, outer)
		a.arith(l.Post, outer)
		if depth == 0 {
			for _, name := range arithAssigned(l.Init) {
				a.intRegions = append(a.intRegions, intRegion{name: name, start: start, end: end})
			}
		}
	case *syntax.WordIter:
		if l.Name == nil {
			return
		}
		allInts := len(l.Items) > 0 && !x.Select
		for _, w := range l.Items {
			if s, ok := literal(w); !ok || !intLiteral.MatchString(s) {
				allInts = false
			}
		}
		if !allInts {
			a.unsafeInt(l.Name.Value)
			return
		}
		if depth == 0 {
			a.intRegions = append(a.intRegions, intRegion{name: l.Name.Value, start: start, end: end})
		}
	}
}

// arithAssigned returns the variables an arithmetic expression assigns.
func arithAssigned(e syntax.ArithmExpr) []string {
	switch x := e.(type) {
	case *syntax.BinaryArithm:
		if x.Op == syntax.Comma {
			return append(arithAssigned(x.X), arithAssigned(x.Y)...)
		}
		if isArithAssign(x.Op) {
			if w, ok := x.X.(*syntax.Word); ok {
				if s, ok := literal(w); ok && syntax.ValidName(s) {
					return []string{s}
				}
			}
		}
	case *syntax.ParenArithm:
		return arithAssigned(x.X)
	}
	return nil
}

// topLevelInts records the integer assignments made by statements of the
// top-level command that run whatever happens, as proof for the reads after
// them: bare assignments, declare/export/readonly/typeset, and (( )) or let
// assigning a variable.
func (a *analyzer) topLevelInts(file *syntax.File) {
	if !a.opt.Static {
		return
	}
	for _, st := range file.Stmts {
		if st.Background || st.Coprocess || st.Negated || len(st.Redirs) > 0 {
			continue
		}
		end := int(st.End().Offset())
		var assigns []*syntax.Assign
		switch c := st.Cmd.(type) {
		case *syntax.CallExpr:
			if len(c.Args) == 0 {
				assigns = c.Assigns
			}
		case *syntax.DeclClause:
			if c.Variant != nil && c.Variant.Value != "local" && c.Variant.Value != "nameref" && !declRefOrInt(c) {
				assigns = c.Args
			}
		case *syntax.ArithmCmd:
			for _, name := range arithAssigned(c.X) {
				a.intDefs[name] = append(a.intDefs[name], end)
			}
		case *syntax.LetClause:
			for _, e := range c.Exprs {
				for _, name := range arithAssigned(e) {
					a.intDefs[name] = append(a.intDefs[name], end)
				}
			}
		}
		for _, as := range assigns {
			if as.Name != nil && !as.Naked && intAssign(as) {
				a.intDefs[as.Name.Value] = append(a.intDefs[as.Name.Value], end)
			}
		}
	}
}

// declRefOrInt reports whether a declaration clause has -n or -i.
func declRefOrInt(c *syntax.DeclClause) bool {
	for _, as := range c.Args {
		if as.Name != nil || as.Value == nil {
			continue
		}
		if s, ok := literal(as.Value); ok && strings.HasPrefix(s, "-") && strings.ContainsAny(s[1:], "ni") {
			return true
		}
	}
	return false
}

// finishArith refuses every arithmetic read of a variable not proven to hold
// an integer at that point.
func (a *analyzer) finishArith() {
	for _, r := range a.arithReads {
		if a.intAt(r.name, r.off) {
			continue
		}
		a.add(Deny, r.n, r.outer, r.name, "arithmetic reads "+r.name+", whose value static enforcement cannot see; bash evaluates any subscript in that value, which can run commands")
	}
}

func (a *analyzer) intAt(name string, off int) bool {
	if a.unsafeInts[name] || shellSetVars[name] {
		return false
	}
	for _, end := range a.intDefs[name] {
		if end <= off {
			return true
		}
	}
	for _, r := range a.intRegions {
		if r.name == name && r.start <= off && off < r.end {
			return true
		}
	}
	return false
}
