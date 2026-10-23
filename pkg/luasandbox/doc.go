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
//   - Run executes on the caller's goroutine. The only other goroutine is a
//     context.AfterFunc that sets the interpreter's interrupt flag when the
//     run's context ends; it never touches the interpreter, and Run stops it
//     before returning.
//   - Cancellation stops the script at its next interpreter step, even when
//     it only computes.
//   - A script cannot catch the end of its own run. Budget kills, cancellation
//     and host failures pass through every pcall/xpcall level.
//   - Every value a host function creates is charged to the run's memory
//     budget before the script can see it.
//   - Source code is screened before parsing so that pathological nesting
//     cannot exhaust the Go stack (a fatal, unrecoverable error in Go).
//
// # Known limits
//
//   - The memory budget counts total allocation (garbage collection does not
//     credit it back). Measured peak process memory reaches up to 2.7 times
//     the budget for scripts holding many small strings or closures, whose
//     headers golua does not charge; DeriveCapacity plans for 3 times.
//
// The interpreter is github.com/arnodel/golua v0.3.0 with seven fixes, vendored
// in third_party/golua (see its README; each fix is proposed upstream). Its
// types never appear in this package's exported API, so the interpreter can
// be replaced here without touching any host.
package luasandbox
