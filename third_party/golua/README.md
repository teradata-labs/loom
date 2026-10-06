# golua (vendored, patched)

This directory holds a copy of the Lua 5.4 interpreter
[`github.com/arnodel/golua`](https://github.com/arnodel/golua), Apache-2.0 (see
`LICENSE`), used only by `pkg/luasandbox`. It is not a loom API: import
`pkg/luasandbox`, not these packages.

| | |
|---|---|
| Upstream | `arnodel/golua` branch `lua5.5`, commit `a129ef2` (tag `v0.3.0`) |
| Patched source | [`ilsiepotamus/golua`](https://github.com/ilsiepotamus/golua) branch `combined/sandbox-hardening`, commit `56531d9` |
| Packages copied | the 20 packages `pkg/luasandbox` needs, non-test files only, imports rewritten to this path |

## Why it is vendored

`pkg/luasandbox` runs untrusted scripts inside a shared server process. Building it
turned up defects in golua that an embedder cannot work around safely from outside.
Each fix is proposed upstream; once upstream releases them, this copy can be replaced
by the module again.

| Change | Upstream PR | Files |
|---|---|---|
| `string.format("%p")` without an argument panicked; it now returns an error | [#130](https://github.com/arnodel/golua/pull/130) | `lib/stringlib/format.go` |
| `base.Load` wrote shared function flags on every runtime: a data race between concurrent runtimes | [#131](https://github.com/arnodel/golua/pull/131) | `lib/base/base.go`, `lib/base/ipairs.go` |
| Metamethods the VM calls directly recursed on the Go stack without limit and aborted the process; nested loops are now limited to 1000 (a catchable "stack overflow") | [#132](https://github.com/arnodel/golua/pull/132) | `runtime/thread.go` |
| `Interrupt` stops a running context from another goroutine; `PopContext` restores the parent before charging it | [#133](https://github.com/arnodel/golua/pull/133) | `runtime/interrupt.go` (new), `runtime/runtimecontext.go`, `runtime/runtimecontextmanager.go`, `runtime/runtimecontextmanager_noquotas.go` |
| Deeply nested source exhausted the Go stack in the parser and compiler; nesting is now limited to 400 levels ("too many syntax levels") | [#134](https://github.com/arnodel/golua/pull/134) | `parsing/parser.go` |
| Tables were charged 8% to 29% of what they allocate; they are now charged what they allocate (within about 10%) | [#135](https://github.com/arnodel/golua/pull/135) | `runtime/table.go`, `runtime/hashtable.go`, `runtime/luacont.go` |
| 64-bit Lua integers were narrowed to `int` with a plain conversion, which wraps where `int` is 32 bits: wrong positions, miscompiled literals and two panics on linux/386. They now saturate (`runtime.ClampToInt`) or are compared in `int64`; `math.ldexp` no longer overflows near `math.MaxInt` on any platform; already-correct narrowings put the bound check next to the conversion | [#136](https://github.com/arnodel/golua/pull/136) | `runtime/numconv.go`, `runtime/hashtable.go`, `code/instructions.go`, `ircomp/compinstr.go`, `ast/string.go`, `lib/base/error.go`, `lib/base/select.go`, `lib/mathlib/mathlib.go`, `lib/stringlib/{format,matching,packer,packing,stringlib}.go`, `lib/tablelib/tablelib.go`, `lib/utf8lib/utf8lib.go` |

Every modified file starts with a "Modified for loom" notice. No other file differs
from upstream except for the rewritten import paths.

## Verification

The patched branch passes golua's own Go tests in both build modes (default and
`noquotas`) and all three pool modes, and the official Lua 5.4 test suite
([`arnodel/golua-tests`](https://github.com/arnodel/golua-tests) branch
`golua-5.5`). Both also pass on linux/386 (emulated), apart from `lib/golib`, which
needs a Go toolchain with cgo at test time. Under `-race`, golua's coroutine tests report the same data races on
the patched branch as on unpatched upstream (context push and pop from coroutine
goroutines); `pkg/luasandbox` does not load the coroutine library.

The tests are not copied here. Run them in the fork:

```bash
git clone git@github.com:ilsiepotamus/golua.git && cd golua
git checkout combined/sandbox-hardening
go test ./... && go test -tags noquotas ./...
go build -o /tmp/golua . && git clone --branch golua-5.5 https://github.com/arnodel/golua-tests.git
cd golua-tests && /tmp/golua -u -e "_U=true" -e "_port=true" all.lua   # prints "final OK !!!"
```

## Updating

1. Rebase the fork's patch branches on the new upstream release, rerun the commands
   above, and merge them into `combined/sandbox-hardening`.
2. Copy the same packages again, non-test files only, rewrite
   `github.com/arnodel/golua/` to `github.com/teradata-labs/loom/third_party/golua/`,
   and restore the notices on modified files.
3. Run `go test -tags fts5 -race ./pkg/luasandbox/`.

When upstream has merged every change above, delete this directory and require
`github.com/arnodel/golua` again.

This directory is excluded from gofmt, vet, lint and gosec, like `vendor/`: it is
kept byte-for-byte close to upstream so updates stay mechanical.
