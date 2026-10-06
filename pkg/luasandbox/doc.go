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

// Package luasandbox runs short Lua 5.4 scripts inside the server process with
// hard limits on wall time, CPU, memory and tool calls.
//
// A script reaches the outside world only through a Host: the host decides
// which tools exist, executes them, and reports progress. The package itself
// knows nothing about agents, sessions or storage, so the same engine serves
// every host (the loom agent bridge, applications embedding loom, tests).
//
// # Guarantees
//
//   - Run never panics and never runs without limits. Zero limit fields take the
//     package defaults and every field is capped at MaxLimits.
//   - Run executes on the caller's goroutine. The package starts no goroutine
//     that touches the interpreter, so nothing outlives Run.
//   - A script cannot catch the end of its own run. Budget kills, cancellation
//     and host failures pass through every pcall/xpcall level.
//   - Every value a host function creates is charged to the run's memory
//     budget before the script can see it.
//   - Source code is screened before parsing so that pathological nesting
//     cannot exhaust the Go stack (a fatal, unrecoverable error in Go).
//
// # Known limits
//
//   - Cancellation is observed at the next host call or pcall return. A script
//     that only computes is stopped by its CPU or wall budget instead, so the
//     worst-case cancellation latency for pure computation is the remaining
//     CPU budget (10 to 40 seconds at the defaults, depending on the code).
//   - The memory budget counts what golua charges, which is total allocation
//     (garbage collection does not credit it back) and undercounts small
//     tables: measured peak process memory reached 10.5 times the budget for
//     a script building tables of tables. DeriveCapacity plans for 12 times.
//   - Metamethods the VM calls directly (__index and __newindex functions,
//     operators, comparisons, __len, __concat, __close) are not available:
//     golua recurses on the Go stack for them with no depth limit, and a Go
//     stack overflow aborts the process. __index and __newindex may be
//     tables.
//
// The interpreter is github.com/arnodel/golua. Its types never appear in this
// package's exported API, so the interpreter can be replaced here without
// touching any host.
package luasandbox
