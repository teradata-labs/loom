// Modified for loom, 2026: CompileLuaChunkWithPoll lets the embedder stop a
// long compilation (loom-only; see third_party/golua/README.md for the list of
// changes from upstream).

package astcomp

import (
	"fmt"

	"github.com/teradata-labs/loom/third_party/golua/ast"
	"github.com/teradata-labs/loom/third_party/golua/ir"
)

// CompileLuaChunk compiles the given block statement to IR code and returns a
// slice or ir.Constant values and the index to the main code constant.
func CompileLuaChunk(source string, s ast.BlockStat) (kidx uint, consts []ir.Constant, err error) {
	return CompileLuaChunkWithPoll(source, s, nil)
}

// CompileLuaChunkWithPoll is CompileLuaChunk calling poll, when not nil, once
// every pollInterval statements and expressions compiled. The embedder uses it
// to stop compiling a large chunk when its deadline passes or its caller
// cancels: poll does so by panicking, and the panic propagates to the caller
// of CompileLuaChunkWithPoll.
func CompileLuaChunkWithPoll(source string, s ast.BlockStat, poll func()) (kidx uint, consts []ir.Constant, err error) {
	defer func() {
		if r := recover(); r != nil {
			compErr, ok := r.(Error)
			if !ok {
				panic(r)
			}
			err = compErr
		}
	}()
	kp := ir.NewConstantPool()
	rootIrC := ir.NewCodeBuilder("<global chunk>", kp)
	rootIrC.DeclareLocal("_ENV", rootIrC.GetFreeRegister())
	irC := rootIrC.NewChild("<main chunk>")
	c := &compiler{CodeBuilder: irC}
	if poll != nil {
		c.poll = &poller{fn: poll}
	}
	c.compileFunctionBody(ast.Function{
		ParList: ast.ParList{HasDots: true},
		Body:    s,
	})
	kidx, _ = irC.Close()
	return kidx, kp.Constants(), nil
}

type compiler struct {
	*ir.CodeBuilder
	poll *poller // shared by the compilers of nested functions; nil when not polling
}

func (c *compiler) NewChild(name string) *compiler {
	return &compiler{
		CodeBuilder: c.CodeBuilder.NewChild(name),
		poll:        c.poll,
	}
}

// pollInterval is how many statements and expressions are compiled between
// two calls of the poll function. Compiling 256 nodes takes microseconds, so a
// stop request is noticed promptly, and the poll costs well under 1% of the
// compilation.
const pollInterval = 256

// poller counts compiled nodes and calls fn every pollInterval of them.
type poller struct {
	fn func()
	n  uint
}

// tick counts one compiled node. It does nothing on a nil poller.
func (p *poller) tick() {
	if p == nil {
		return
	}
	p.n++
	if p.n%pollInterval == 0 {
		p.fn()
	}
}

// Names of various labels and registers used during compilation.
const (
	breakLblName    = ir.Name("<break>")
	ellipsisRegName = ir.Name("...")
	callerRegName   = ir.Name("<caller>")
	loopFRegName    = ir.Name("<f>")
	loopSRegName    = ir.Name("<s>")
	loopVarRegName  = ir.Name("<var>")
)

// Error that results from a valid AST which does not form a valid program.
type Error struct {
	Where   ast.Locator
	Message string
}

func (e Error) Error() string {
	loc := e.Where.Locate().StartPos()
	return fmt.Sprintf("%d:%d: %s", loc.Line, loc.Column, e.Message)
}

// The compiler uses other packages that may return non nil errors only if there
// is a bug in the compiler.  Such errors are wrapped in must() so that the bugs
// are not silently ignored.
type compilerBug struct {
	err error
}

func (b compilerBug) Error() string {
	return b.err.Error()
}

func must(err error) {
	if err != nil {
		panic(compilerBug{err: err})
	}
}
