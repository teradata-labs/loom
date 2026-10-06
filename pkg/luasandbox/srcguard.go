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

	"github.com/arnodel/golua/scanner"
	"github.com/arnodel/golua/token"
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
	// maxExpressionTokens bounds tokens in one expression run (operator
	// chains such as a..b..c or - - - x, and index/call chains a.b.c or
	// f()()), which nest in the parse tree even without brackets.
	maxExpressionTokens = 1000
)

// checkSourceShape rejects sources whose nesting would make the parser or
// compiler recurse beyond maxSyntaxDepth levels. It tokenizes iteratively and
// never recurses. Lexical errors are left for the parser to report.
func checkSourceShape(chunk, src string) error {
	sc := scanner.New(chunk, []byte(src))
	// runs[i] counts tokens since the last expression boundary at depth i.
	runs := make([]int, 1, 16)
	prevEndsOperand := false
	for {
		tok := sc.Scan()
		if tok == nil || tok.Type == token.EOF || tok.Type == token.INVALID || tok.Type == token.UNFINISHED {
			return nil
		}
		top := len(runs) - 1
		switch {
		case opensFrame(tok.Type):
			runs[top]++
			if len(runs) > maxSyntaxDepth {
				return fmt.Errorf("%s:%d: too many syntax levels (more than %d nested brackets or blocks); flatten the code", chunk, tok.Line, maxSyntaxDepth)
			}
			runs = append(runs, 0)
			prevEndsOperand = false
		case closesFrame(tok.Type):
			if len(runs) > 1 {
				runs = runs[:top]
			}
			runs[len(runs)-1]++
			prevEndsOperand = tok.Type != token.KwUntil
		case resetsRun(tok.Type):
			runs[top] = 0
			prevEndsOperand = false
		default:
			// An identifier directly after a complete operand starts a new
			// statement ("f(x) g(y)", "a = 1 b = 2"); Lua has no other way
			// for two operands to be adjacent.
			if tok.Type == token.IDENT && prevEndsOperand {
				runs[top] = 0
			}
			runs[top]++
			if runs[top] > maxExpressionTokens {
				return fmt.Errorf("%s:%d: expression too long (more than %d tokens without a break); split it into several statements or use table.concat", chunk, tok.Line, maxExpressionTokens)
			}
			prevEndsOperand = endsOperand(tok.Type)
		}
	}
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
