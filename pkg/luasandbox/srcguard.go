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

package luasandbox

import (
	"fmt"

	"github.com/teradata-labs/loom/third_party/golua/scanner"
	"github.com/teradata-labs/loom/third_party/golua/token"
)

// The interpreter's parser and compiler recurse once per nesting level with no
// limit of their own. Measured on golua v0.3.0: 1 MiB of "((((" or "- - - -"
// needs more than 128 MiB of Go stack to compile, and Go aborts the whole
// process (not just the goroutine) when a stack passes 1 GiB. Reference Lua
// stops at 200 syntax levels; this guard applies the same limit before the
// parser runs.
const (
	// maxSyntaxDepth bounds brackets plus blocks open at once.
	maxSyntaxDepth = 200
	// maxExpressionTokens bounds tokens in unfinished expressions across all
	// open levels (operator chains such as a..b..c or - - - x, and
	// index/call chains a.b.c or f()()), which nest in the parse tree even
	// without brackets.
	maxExpressionTokens = 2000
)

// checkSourceShape rejects sources whose nesting would make the parser or
// compiler recurse too deeply. It tokenizes iteratively and never recurses.
// Lexical errors are left for the parser to report.
//
// Recursion depth grows with open brackets and blocks and with the length of
// each unfinished expression in every open level, so the guard bounds the
// number of open levels and the total of unfinished-expression tokens across
// all of them (bounding each level separately would let the limits multiply).
func checkSourceShape(chunk, src string) error {
	sc := scanner.New(chunk, []byte(src))
	// runs[i] counts tokens since the last expression boundary at depth i;
	// open is their sum, the work the parser holds on its stack.
	runs := make([]int, 1, 16)
	open := 0
	prevEndsOperand := false
	bump := func() {
		runs[len(runs)-1]++
		open++
	}
	reset := func() {
		open -= runs[len(runs)-1]
		runs[len(runs)-1] = 0
	}
	for {
		tok := sc.Scan()
		if tok == nil || tok.Type == token.EOF || tok.Type == token.INVALID || tok.Type == token.UNFINISHED {
			return nil
		}
		switch {
		case opensFrame(tok.Type):
			// if/do/repeat always start a statement, and so does function
			// right after a complete one; they end the run before them.
			if startsStatement(tok.Type) || (tok.Type == token.KwFunction && prevEndsOperand) {
				reset()
			}
			bump()
			if len(runs) > maxSyntaxDepth {
				return fmt.Errorf("%s:%d: too many syntax levels (more than %d nested brackets or blocks); flatten the code", chunk, tok.Line, maxSyntaxDepth)
			}
			runs = append(runs, 0)
			prevEndsOperand = false
		case closesFrame(tok.Type):
			if len(runs) > 1 {
				reset()
				runs = runs[:len(runs)-1]
			}
			bump()
			prevEndsOperand = tok.Type != token.KwUntil
		case resetsRun(tok.Type):
			reset()
			prevEndsOperand = false
			continue
		default:
			// An identifier directly after a complete operand starts a new
			// statement ("f(x) g(y)", "a = 1 b = 2"); Lua has no other way
			// for two operands to be adjacent.
			if tok.Type == token.IDENT && prevEndsOperand {
				reset()
			}
			bump()
			prevEndsOperand = endsOperand(tok.Type)
		}
		// Brackets count too: f()()() and a[1][2][3] nest without opening
		// a level that stays open.
		if open > maxExpressionTokens {
			return fmt.Errorf("%s:%d: expression nesting too deep (more than %d unfinished tokens); split it into several statements or use table.concat", chunk, tok.Line, maxExpressionTokens)
		}
	}
}

// startsStatement reports keywords that can only begin a statement.
func startsStatement(tp token.Type) bool {
	switch tp {
	case token.KwIf, token.KwDo, token.KwRepeat:
		return true
	}
	return false
}

func opensFrame(tp token.Type) bool {
	switch tp {
	case token.SgOpenBkt, token.SgOpenSquareBkt, token.SgOpenBrace,
		token.KwFunction, token.KwDo, token.KwIf, token.KwRepeat:
		return true
	}
	return false
}

func closesFrame(tp token.Type) bool {
	switch tp {
	case token.SgCloseBkt, token.SgCloseSquareBkt, token.SgCloseBrace,
		token.KwEnd, token.KwUntil:
		return true
	}
	return false
}

// resetsRun reports tokens that end one expression and start another.
func resetsRun(tp token.Type) bool {
	switch tp {
	case token.SgComma, token.SgSemicolon, token.SgAssign, token.SgDoubleColon,
		token.KwLocal, token.KwGlobal, token.KwReturn, token.KwThen, token.KwElse,
		token.KwElseIf, token.KwBreak, token.KwGoto, token.KwIn, token.KwWhile,
		token.KwFor:
		return true
	}
	return false
}

func endsOperand(tp token.Type) bool {
	switch tp {
	case token.IDENT, token.STRING, token.LONGSTRING, token.NUMDEC, token.NUMHEX,
		token.KwNil, token.KwTrue, token.KwFalse, token.SgEtc:
		return true
	}
	return false
}
