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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nestedSource builds n levels of one construct.
func nestedSource(kind string, n int) string {
	r := strings.Repeat
	switch kind {
	case "parens":
		return "return " + r("(", n) + "1" + r(")", n)
	case "tables":
		return "return " + r("{", n) + r("}", n)
	case "unary":
		return "return " + r("- ", n) + "1"
	case "not":
		return "return " + r("not ", n) + "true"
	case "concat":
		return "return " + r("'a'..", n) + "'a'"
	case "power":
		return "return " + r("2^", n) + "1"
	case "plus":
		return "return " + r("1+", n) + "1"
	case "blocks":
		return r("do ", n) + r("end ", n)
	case "ifs":
		return r("if true then ", n) + r("end ", n)
	case "funcs":
		return "return " + r("function() return ", n) + "1" + r(" end", n)
	case "index":
		return "local a = {} return a" + r(".b", n)
	case "calls":
		return "local f = function() return f end return f" + r("()", n)
	case "repeat":
		return r("repeat ", n) + r("until true ", n)
	}
	panic(kind)
}

var nestedKinds = []string{"parens", "tables", "unary", "not", "concat", "power", "plus", "blocks", "ifs", "funcs", "index", "calls", "repeat"}

func TestSourceGuardRejectsDeepNesting(t *testing.T) {
	// Each of these needed more than 128 MiB of Go stack to compile at 1 MiB
	// of source before the guard existed.
	for _, kind := range nestedKinds {
		t.Run(kind, func(t *testing.T) {
			src := nestedSource(kind, 2500)
			err := checkSourceShape("inline", src)
			require.Error(t, err)
			assert.Regexp(t, `too many syntax levels|expression too long`, err.Error())

			lim := MaxLimits()
			big := nestedSource(kind, 300000)
			if len(big) > lim.MaxSourceBytes {
				big = big[:lim.MaxSourceBytes]
			}
			start := time.Now()
			res := Run(context.Background(), Program{Source: big}, lim, newFakeHost())
			assert.Equal(t, OutcomeScriptError, res.Outcome)
			assert.Less(t, time.Since(start), slack(time.Second), "the guard scans 1 MiB quickly")
		})
	}
}

func TestSourceGuardAcceptsOrdinaryCode(t *testing.T) {
	for _, kind := range nestedKinds {
		t.Run("nested 50 "+kind, func(t *testing.T) {
			assert.NoError(t, checkSourceShape("inline", nestedSource(kind, 50)))
		})
		t.Run("nested 190 "+kind, func(t *testing.T) {
			assert.NoError(t, checkSourceShape("inline", nestedSource(kind, 190)), "just under the syntax-level limit")
		})
	}
	t.Run("thousands of statements", func(t *testing.T) {
		var b strings.Builder
		for i := range 5000 {
			fmt.Fprintf(&b, "print(%d) x%d = %d y = x%d .. 'z'\n", i, i%7, i, i%7)
		}
		assert.NoError(t, checkSourceShape("inline", b.String()))
	})
	t.Run("long but legitimate expression", func(t *testing.T) {
		parts := make([]string, 300)
		for i := range parts {
			parts[i] = fmt.Sprintf("v%d", i)
		}
		src := "local s = " + strings.Join(parts, " .. ', ' .. ")
		assert.NoError(t, checkSourceShape("inline", src))
	})
	t.Run("big table literal", func(t *testing.T) {
		items := make([]string, 20000)
		for i := range items {
			items[i] = fmt.Sprintf("{id = %d, name = 'n%d'}", i, i)
		}
		assert.NoError(t, checkSourceShape("inline", "return {"+strings.Join(items, ", ")+"}"))
	})
	t.Run("brackets inside strings and comments do not count", func(t *testing.T) {
		src := "local s = '" + strings.Repeat("(", 1000) + "' -- " + strings.Repeat("{", 1000) + "\n" +
			"local l = [[" + strings.Repeat("[", 1000) + "]] return #s"
		assert.NoError(t, checkSourceShape("inline", src))
	})
	t.Run("lexical errors are left to the parser", func(t *testing.T) {
		assert.NoError(t, checkSourceShape("inline", `local s = "unterminated`))
		res := runSrc(t, `local s = "unterminated`, nil)
		assert.Equal(t, OutcomeScriptError, res.Outcome)
	})
}
