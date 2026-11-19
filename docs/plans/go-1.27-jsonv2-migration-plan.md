# Go 1.27 and `encoding/json/v2` migration plan

**Status**: 🚧 In Development. Phase 1a (linter upgrade) is ⚠️ Partial: the code is on branch `chore/golangci-lint-v2.15.0`, and its exit criterion is not yet met: `just check` stops at `just vet` on `main` until PR #681 merges. Phases 1b–5 are 📋 Planned. Trial measurements in §2 and §3 were taken on 2026-11-18 with Go 1.27.4 against the working tree, using a scratch copy of `go.mod`.
**Owner**: Ilsun Park
**Scope**: Move loom from Go 1.26.7 to Go 1.27.4, then move loom's JSON code from the `encoding/json` (v1) API to `encoding/json/v2` and `encoding/json/jsontext`.
**Proto impact**: None. No `.proto` file changes. `protojson` (11 files) is a separate encoder and is not part of this work. `buf breaking` must stay clean throughout.

## 0. Decisions

D1–D7 were answered by Ilsun Park on 2026-11-19. D1–D5 and D7 follow the recommendation. D6 was an open call and is decided as shown. D8 was found on 2026-11-19 while tracing readers for D6 and was answered the same day, following the recommendation.

| # | Decision | Decided | Why |
|---|---|---|---|
| D1 | `go` directive: `go 1.27.4`, or keep `go 1.26.x` and add `toolchain go1.27.4` | `go 1.27.4` | Importing `encoding/json/v2` needs a 1.27 standard library, so the directive has to move before Phase 2 anyway. Cost: any module that imports loom must build with Go ≥ 1.27.4. `loom-cloud` is on `go 1.26.1`. |
| D2 | Route to v2 | Two steps: switch call sites to the v2 API with v1 semantics (no behaviour change), then drop v1 semantics one option at a time | This is the "option-by-option" path in the Go migration guide. Each PR is either mechanical or changes one behaviour. |
| D3 | Exported Go API: keep `json.RawMessage` and `json.Number` in exported `pkg/` types, or change to `jsontext.Value` | Keep | 32 exported struct fields and 7 exported function signatures use them. Changing them breaks `loom-cloud` and `avmo-tera-cloud` at compile time. Both types work unchanged through v2 (measured, §3). |
| D4 | Where the shared option sets live | `internal/jsonopts` | Keeps new public API surface at zero while the API is stabilizing. Promote to `pkg/` only if a downstream repo needs it. |
| D5 | Strictness for model-produced JSON (tool-call arguments, structured output) | Keep case-insensitive field matching permanently. Decide duplicate-name and invalid-UTF-8 rejection from an eval run, not by default. | v2 case-sensitive matching drops mismatched fields **without an error** (§3, row 7). Models do emit `Query` for `query`. |
| D6 | `cmd/loom-longmemeval` report fields named `*_ms` hold `time.Duration` and serialise as **nanoseconds** | Emit real milliseconds under the same field names | Existing mislabel, not caused by this migration, but v2 forces a choice because it refuses to marshal `time.Duration` (§3, row 10). Result files written before the change hold nanoseconds and are not rewritten; see Phase 4, item 2 for what that means for `score`. |
| D7 | Migrate the 154 test files too | Yes, last | Low direct value. It is what allows a repo-wide lint rule against new v1 calls (Phase 5). |
| D8 | HITL webhook body: `HumanRequest.Timeout` (`json:"timeout"`) is sent by `JSONNotifier.Notify` (`pkg/shuttle/human_tool.go:744`) as **nanoseconds** | Keep nanoseconds | The receivers are outside the repo and cannot be audited, the field name carries no unit, and the same body already carries `expires_at`. Without action a v2 marshal of this struct returns an error and the webhook stops (row 10). The exported Go field stays `time.Duration` (D3); the webhook gets its own payload struct with an integer `timeout`. |

## 1. Where the repo is today (measured)

| Item | Value |
|---|---|
| `go.mod` directive | `go 1.26.7`, no `toolchain` line, no `godebug` lines, no `replace` |
| Newest Go releases | go1.27.4 and go1.26.11 (`go.dev/dl`, 2026-11-18). Go 1.27.0 shipped 2026-08-19. |
| CI pins | 15 `go-version: '1.26'` values and 2 `if: matrix.go-version == '1.26'` conditions across 5 workflows (`ci.yml`, `nightly.yml`, `release.yml`, `version-check.yml`, `windows-quickstart.yml`) |
| Docker | `deploy/Dockerfile`, `deploy/longmemeval/Dockerfile`, `docker/Dockerfile.runtime`, all `golang:1.26-*` |
| Linter | golangci-lint v2.9.0 in CI and locally (built with go1.26.1). It cannot load a `go 1.27` module. v2.15.0 is built with go1.27.0 (verified by running the binary). |
| Docs | State Go 1.24+ (`CONTRIBUTING.md:27`), Go 1.25+ (`docs/guides/quickstart.md:34`, `docs/installation/WINDOWS.md:71`, `docs/reference/tui.md:110`, `docs/guides/docker-backend.md:58`, `web/index.html:413`). All are already wrong: `go.mod` requires 1.26.7. |
| Downstream | `loom-cloud`: `go 1.26.1`, pinned to a loom pseudo-version from 2026-05-13 |

JSON surface, excluding `gen/`:

| Measure | Production | Tests |
|---|---|---|
| Files importing `encoding/json` | 186 | 154 |
| `json.Marshal` / `json.MarshalIndent` calls | 264 / 22 | 264 / 2 |
| `json.Unmarshal` calls | 341 | 268 |
| `json.NewDecoder` / `json.NewEncoder` | 20 / 11 | 47 / 65 |
| `json.RawMessage` references | 104 | 173 |
| `json:"…"` tag lines / with `omitempty` | 1,447 / 491 | 373 / 20 |
| Custom `MarshalJSON` / `UnmarshalJSON` methods | 4 / 5 | 1 / 0 |
| Other JSON libraries imported directly | none | none |

Largest production groups by file count: `pkg/mcp` 28, `pkg/agent` 25, `pkg/llm` 18, `pkg/shuttle` 16, `pkg/storage` 10.

## 2. What the Go 1.27 toolchain changes without any code edits

### 2.1 The JSON engine swap is not optional

From the Go 1.27 release notes: "The `encoding/json` package is now backed by the v2 implementation. Marshaling and unmarshaling behavior is preserved, but the exact text of error messages may differ." The only opt-out is the build-time setting `GOEXPERIMENT=nojsonv2`, which the notes say will be removed in a later release.

So Phase 1b (toolchain only) already puts all 340 files on the new engine. This plan does not use `nojsonv2`. It is recorded here as the rollback lever.

### 2.2 Trial results: Go 1.27.4, `go` directive set to 1.27.4 in a scratch modfile

| Gate | Result |
|---|---|
| `go build -tags fts5 ./...` | ✅ Passes. No dependency changes needed; `go.sum` unchanged. |
| `go vet -tags fts5 ./...` | ⚠️ 1 new finding: `third_party/golua/parsing/parser.go:187: unreachable code`. Clean on 1.26.7. The `just vet` recipe and nightly CI exclude `third_party/`, so no gate fails, but the finding is real. |
| `gofmt -l` (1.27 formatter) | ✅ 0 files. The 1.27 alignment change does not touch this repo. |
| `go test -tags fts5 -race -short ./...` | ⚠️ 119 of 120 test packages pass (100 more packages have no tests). 1 fails: `pkg/luasandbox`, `TestMeasureJSONMatchesEncodingJSON` (§2.3). No data races reported. `pkg/skills/index` was verified in a separate run with gateway credentials unset (see the note below). |
| `golangci-lint` v2.15.0 | ⚠️ 143 findings where v2.9.0 reports 0. 127 staticcheck `QF1012` (`WriteString(fmt.Sprintf(…))` → `fmt.Fprintf`), 9 staticcheck `SA1019` (tests that exercise loom's own deprecated `SkillsConfig.EnabledSkills` / `DisabledSkills`), 6 errcheck (2 in production: `pkg/mcp/transport/stdio.go:259`, `pkg/shelljail/host.go:540`), 1 govet `inline` (`reflect.Ptr`). 128 are in production files, 15 in tests. These come from the linter upgrade, not from Go 1.27: the same 143 appear with the directive left at `go 1.26.7`. |

How the trial was run: `GOTOOLCHAIN=go1.27.4` with `-modfile` pointing at a scratch copy of `go.mod` whose directive was set to `go 1.27.4`. Build, vet, and tests ran against the working tree (which had unrelated uncommitted changes). Lint ran against an export of `HEAD`, because golangci-lint does not accept `-modfile`.

Note on `pkg/skills/index`: `TestRouteCorpusAgainstJev` (`router_corpus_test.go:606`) calls the live Jev decider through the Vercel gateway whenever gateway credentials are in the environment. `-short` does not skip it. With credentials exported in the shell, the package ran for more than 8 minutes on go1.27.4 and more than 4 minutes on go1.26.7 before each run was stopped. With credentials unset it passes in 2 seconds on go1.27.4. This is not a Go 1.27 issue. It does mean `just test` on a machine with gateway credentials makes paid calls and can stall; the trial runs here made such calls.

### 2.3 Measured v1 behaviour differences on 1.27.4

Side-by-side run of one program on go1.26.7 and go1.27.4. Results were the same with the `go` directive at 1.26.7 and at 1.27.4, so the directive does not gate them.

| Case | go1.26.7 | go1.27.4 | Effect on loom |
|---|---|---|---|
| `json.Marshal` of a string with an invalid UTF-8 byte (`"\xff"`) | `"�"` (escaped, 8 bytes) | `"�"` (literal U+FFFD, 5 bytes) | **Breaks** `measureJSON` in `pkg/luasandbox/jsonbound.go:216`, which mirrors the encoder byte for byte to enforce result-size limits. `TestMeasureJSONMatchesEncodingJSON` fails (expected 58, got 61). The release notes do not mention this change. |
| `json.Unmarshal` type-mismatch error text | `…into Go value of type string` | `…into Go struct field .a of type string` | Wording only. Any error text that reaches a model in a retry prompt changes wording. |
| `*json.SyntaxError` type and `Offset` | present | same type, same offsets | `cmd/looms/config.go:1323` (the one `errors.As` site) keeps working. |
| Duplicate keys, invalid UTF-8 on input, trailing data | accepted / replaced / rejected | same | No change under the v1 API. |

No test in the repo asserts on JSON error text (0 matches), and no production code string-matches it.

### 2.4 Other Go 1.27 changes and loom's exposure

| Change in 1.27 | Exposure | Action |
|---|---|---|
| HTTP/1 `Response.Body.Close` now drains unread content up to a limit | 62 production `Body.Close()` sites; 10 production SSE readers | Verify that cancelling a streaming LLM call still returns promptly. Existing stream-timeout tests cover part of this. |
| `compress/flate` output bytes differ from 1.26 | 5 production files import `compress/*` or `archive/zip` | Check that no stored hash or golden file depends on compressed bytes. |
| `SystemCertPool` honours `SSL_CERT_FILE` / `SSL_CERT_DIR` on macOS and Windows | 2 production references | Check `pkg/tls` behaviour on macOS and Windows. |
| `go test -json` adds an `OutputType` field | 1 production reference | Check that consumer. |
| `go vet` (and the vet subset in `go test`) gains checks: `stdversion` by default, `%w` with pointer-to-error | Trial vet run shows only the golua finding | None beyond the golua fix. |
| `asynctimerchan` and five `tls*` GODEBUG settings removed | No `godebug` lines, `//go:debug` comments, or `GODEBUG` use in the repo | None. |
| Closure symbol names simplified | 0 uses of `runtime.FuncForPC` / `CallersFrames` | None. |
| `net.UnixConn` reads return bare `io.EOF` | 0 `net.OpError` handling sites | None. |
| `go mod tidy` merges duplicate `require` blocks at `go 1.27` | `go.mod` | Expect a `go.mod` layout diff; rerun `just notices`. |
| macOS 13 becomes the minimum | Install docs, Homebrew formula | State it in the install docs. |
| Generic methods, struct-literal field selectors | Language additions | No action. Not adopted as part of this work. |

## 3. What the v2 API changes (measured on go1.27.4)

Each row was run through both packages. "Loom exposure" counts production code only.

| # | Behaviour | v1 | v2 default | Loom exposure |
|---|---|---|---|---|
| 1 | `omitempty` on numbers and bools | omits `0` / `false` | **emits** `"temperature":0`, `"stream":false` | 47 numeric and 17 bool fields; 21 of the 64 are in `pkg/llm` provider request types |
| 2 | `omitempty` on `any` | omits only nil | also omits `""`, `[]`, `{}` | 6 fields |
| 3 | `omitempty` on an empty struct value | emits `{}` | omits | 7 named-type fields need a type check |
| 4 | nil slice / nil map without `omitempty` | `null` | `[]` / `{}` | Not yet counted. Needs the type-aware audit in Phase 2. |
| 5 | Map key order | sorted | **random** (20 distinct outputs in 50 runs) unless `Deterministic(true)` | `map[string]any` appears 1,687 times. 8 production files hash or compare marshalled JSON (`pkg/agent/agent.go`, `pkg/agent/context_compilation.go`, `pkg/artifacts/store.go`, `pkg/mcp/adapter/shuttle.go`, `internal/mcpstate/sealer.go`, and 3 more). Tool schemas built from maps also feed provider prompt caches. |
| 6 | HTML escaping of `<`, `>`, `&` | escaped | not escaped | 6 production files put marshalled JSON into HTML or scripts (`pkg/visualization/report_generator.go`, `internal/workflow-viz/visualizer.go`, `pkg/mcp/apps/compiler.go`, `pkg/server/http.go`, and 2 more). This is an XSS defence that must become explicit. |
| 7 | Field-name matching on unmarshal | case-insensitive | case-sensitive; `{"Query":"x"}` leaves `query` empty **with no error** | Every site that parses model-produced JSON into a struct |
| 8 | Duplicate object names | last wins | error | Inputs from models, MCP servers, HTTP clients |
| 9 | Invalid UTF-8 in input strings | replaced with U+FFFD | error | Same as row 8, plus tool results |
| 10 | `time.Duration` | nanoseconds as a number | **error**: "no default representation". A `format:` tag does not help: go1.27.4 rejects every `format` tag value (`nano`, `micro`, `milli`, `sec`, `units`, `iso8601` all probed) with "unsupported `format` tag option", under v2 **and** under v1. The only built-in route is the legacy option `jsonv1.FormatDurationAsNano`. | 6 fields (`cmd/loom-longmemeval` 4, `pkg/metaagent/progress.go:67`, `pkg/shuttle/human_tool.go:57`). No `format:` tag exists in the repo today (0 matches). |
| 11 | Pointer-receiver `MarshalJSON` on a non-addressable value | not called | called | `*RequestID` (`pkg/mcp/protocol/jsonrpc.go:41`) and `*JSONSchema` (`pkg/shuttle/tool.go:240`). Output changes wherever either is held by value. |
| 12 | Stream of values from one reader | `Decoder.Decode` reads one value per call | `UnmarshalRead` errors on a second value | 20 production `NewDecoder` sites. Real stream readers must use `jsontext.Decoder` with `UnmarshalDecode`. |
| 13 | Trailing newline when writing | `Encoder.Encode` appends `\n` | `MarshalWrite` does not | 11 production `NewEncoder` sites. Any newline-delimited framing must add it explicitly. |
| 14 | `Decoder.UseNumber`, `Decoder.Token` | available | no direct equivalent; `jsontext.Decoder` reads tokens and keeps number text | `pkg/luasandbox` (3 `UseNumber` sites; `jsonbound.go` is a hand-written token walker) |
| 15 | `DisallowUnknownFields` | decoder method | `RejectUnknownMembers(true)` option, works on `Unmarshal` | 5 sites (`pkg/shuttle/hook_config.go` 4, `pkg/decision/llm/adapter.go` 1) |
| 16 | Error detail | Go field name | JSON Pointer to the failing member (`/limit`), JSON kind, Go type, byte offset | Benefit: retry prompts can name the exact argument that failed. |
| 17 | `,string` on `*int` | quoted | quoted, same | 2 tags in `pkg/llm/gemini/types.go`. No change. |
| 18 | Numbers into `any` | `float64` | `float64`, same | No change. |
| 19 | `json.RawMessage`, `json.Number` fields under v2 | n/a | round-trip correctly | Supports D3. |
| 20 | `jsonv2.Marshal(v, jsonv1.DefaultOptionsV1())` | n/a | byte-identical to v1 output in the probe | Supports D2 step one. |

Not measured: merge semantics when unmarshalling into a non-zero value (v2 differs from v1). There is no cheap static check for which call sites reuse a target. The differential tests in Phase 2 are the detection method.

## 4. Design

1. **Wire bytes do not change by accident.** Every change to bytes sent to a provider, an MCP peer, storage, or a hash is its own reviewed change with a test that shows the before and after.
2. **Options are named once.** `internal/jsonopts` (D4) exports a small number of `jsonv2.Options` values, one per boundary. Call sites pass a named set, never an inline list. Draft sets:
   - `Legacy`: `jsonv1.DefaultOptionsV1()`. Exists only during the migration. Deleted in Phase 4.
   - `Wire`: output to providers, MCP peers, HTTP clients, storage, and hashes. Always `Deterministic(true)`.
   - `ModelInput`: parsing model-produced JSON. `MatchCaseInsensitiveNames(true)` (D5).
   - `Strict`: config files, hook config, HTTP request bodies. `RejectUnknownMembers(true)` where the v1 code used `DisallowUnknownFields`; duplicate names and invalid UTF-8 rejected.
   - `HTML`: `Wire` plus `jsontext.EscapeForHTML(true)` and `EscapeForJS(true)`, for the 6 files in row 6.
3. **Exported types keep `json.RawMessage` and `json.Number`** (D3).
4. **Tags over options for per-field intent.** Numeric and bool fields that must be dropped when zero move from `omitempty` to `omitzero`. `omitzero` has the same meaning under v1 and v2, so this change can land before any call site moves.
5. **No hand-written mirrors of encoder output** where avoidable. `measureJSON` is the one known case; Phase 1b fixes it against the 1.27 encoder and adds a fuzz test that compares it with the real encoder.

## 5. Phases

Each phase is one or more PRs. Every PR passes `just check` (lint, `go test -tags fts5 -race`, build) before merge. A failed exit criterion stops the phase.

### Phase 0: Decisions

- [x] D1–D8 answered and recorded in this file (2026-11-19).

### Phase 1a: Linter upgrade (one PR, still on Go 1.26.7)

Split out so the toolchain PR stays small. golangci-lint v2.15.0 lints the current `go 1.26.7` module (measured).

- [x] P0: golangci-lint v2.9.0 → v2.15.0. The version is written once, in the Justfile (`golangci_lint_version`); `ci.yml` reads it from there, `just lint` refuses a different installed version (v1.x included), and `just verify` reports the installed version.
- [x] P0: The 6 errcheck findings (2 production, 4 in tests). Each is a channel receive that dropped an `error`. `pkg/mcp/transport/stdio.go` logs the exit status of a force-killed server at Info; `pkg/shelljail/host.go` puts it in the self-test timeout error; both paths have a test. Of the 4 test sites, 3 assert on the value and the door-gate test reports it in its failure message.
- [x] P1: The 127 `QF1012` findings (41 in `pkg/orchestration`, 17 in `pkg/patterns`, 16 in `pkg/evals`), applied with `golangci-lint run --fix` and reviewed: every changed line is `x.WriteString(fmt.Sprintf(...))` → `fmt.Fprintf(&x, ...)`.
- [x] P1: The 9 `SA1019` findings are tests of loom's own deprecated fields. Each line carries a `//nolint:staticcheck` comment that states the reason. No blanket exclusion.
- [x] P2: The govet `inline` finding (`cmd/loom/decision_ask_test.go:172`, `reflect.Ptr` → `reflect.Pointer`).

Measured on the branch (2026-11-19, macOS arm64, Go 1.26.7): golangci-lint v2.15.0 reports 0 issues, and v2.9.0 also reports 0. Developers need the v2.15.0 release binary; a branch that has not merged `main` shows the 143 findings when linted with it.

**Exit**: `just check` prints "All checks passed" with v2.15.0 on Go 1.26.7. ⚠️ Not met as of 2026-11-19: `just vet` (untagged) fails on `main` at `pkg/agent/compact_test.go:183`, where a test helper sits behind the `fts5` build tag. PR #681 fixes it; this branch does not touch that file. Every other step of `just check` passes on the branch.

### Phase 1b: Toolchain to Go 1.27.4 (one PR, no JSON API changes)

On hold by the owner's call (2026-11-19). Not started.

Priority order:

- [ ] P0: `go.mod` to `go 1.27.4`; `go mod tidy`; `just notices`. Open PRs that touch the same ground, to land or rebase on first: #676 (fuzz job on Go 1.27, vendored golua fixes), #675 (notices without module versions), #681 (JEV environment isolated in tests).
- [ ] P0: Fix `measureJSON` invalid-UTF-8 accounting (`pkg/luasandbox/jsonbound.go:216`). Add a fuzz test: `measureJSON` length and bytes equal `json.Marshal` for arbitrary strings.
- [ ] P0: 15 CI pins and 2 matrix conditions to `'1.27'`; 3 Dockerfiles to `golang:1.27-*`.
- [ ] P0: Rerun golangci-lint v2.15.0 against the `go 1.27.4` directive and fix anything new.
- [ ] P1: Remove the unreachable `return` at `third_party/golua/parsing/parser.go:187`, following the vendored-golua patch process.
- [ ] P1: Gate `TestRouteCorpusAgainstJev` so a plain `just test` does not make live gateway calls (explicit opt-in variable, or skip under `-short`). Found during the trial; independent of Go 1.27.
- [ ] P1: Work through the §2.4 "Action" column: stream-cancel latency, compressed-byte dependence, `SSL_CERT_FILE`, `go test -json` consumer.
- [ ] P1: Fix the stale Go version statements listed in §1 and add the macOS 13 minimum.
- [ ] P2: `just race-check`, `just build-dist` (darwin, linux, and windows cross-compiles), all three Docker builds, Windows quickstart workflow.
- [ ] P2: Tell `loom-cloud` and `avmo-tera-cloud` owners that the next loom version needs Go ≥ 1.27.4.

**Exit**: `just check` prints "All checks passed" on 1.27.4; CI green on Linux, macOS, Windows; no use of `GOEXPERIMENT=nojsonv2`.

**Rollback**: revert the PR.

### Phase 2: Foundation for v2 (one PR, no call-site changes)

- [ ] P0: `internal/jsonopts` with the option sets from §4 and table-driven tests that pin each set's behaviour for every row of §3.
- [ ] P0: Wire snapshot tests. For each provider request builder in `pkg/llm` and each MCP JSON-RPC message type, a golden file of today's bytes. These are the reference for every later phase.
- [ ] P0: Differential test helper: marshal and unmarshal the same value through v1 and through a named option set, and fail on any difference. Used by table tests and by fuzz tests for tool-call arguments, JSON-RPC messages, and hook config.
- [ ] P1: Type-aware audit tool (a `go/analysis` pass run from a test or `cmd/`): list every `omitempty` field by Go type, every non-`omitempty` slice or map field, every type with a pointer-receiver `MarshalJSON` held by value. Output committed under `docs/research/`.
- [ ] P1: `omitempty` → `omitzero` on the 64 numeric and bool fields, with the snapshot tests showing zero byte changes.
- [ ] P2: `docs/architecture/json-codec.md`: which option set applies at which boundary and why.

**Exit**: snapshot and differential tests pass on unchanged production code.

### Phase 3: Switch call sites to the v2 API with `Legacy` semantics (several PRs, no behaviour change)

Order, lowest blast radius first:

- [ ] 1. `cmd/*` tools, `internal/*`, and single-file packages (about 40 files).
- [ ] 2. `pkg/storage`, `pkg/artifacts`, `pkg/observability`, `pkg/evals`, `pkg/skills`, `pkg/server` (about 30 files).
- [ ] 3. `pkg/shuttle` and `pkg/decision` (about 21 files). `DisallowUnknownFields` becomes `RejectUnknownMembers`.
- [ ] 4. `pkg/mcp` (28 files). Stream framing checked against rows 12 and 13. `pkg/mcp/conformance` must pass.
- [ ] 5. `pkg/llm` (18 files). One provider per commit. Snapshot bytes unchanged.
- [ ] 6. `pkg/agent`, `pkg/orchestration`, `pkg/metaagent` (about 33 files).
- [ ] 7. `pkg/luasandbox` (5 files). `jsonbound.go` and the `UseNumber` sites move to `jsontext.Decoder`. Highest effort per file.

Per PR: snapshot tests unchanged, differential tests pass, `go test -tags fts5 -race` passes.

**Exit**: no production call to `json.Marshal`, `json.Unmarshal`, `json.NewEncoder`, or `json.NewDecoder` from v1 remains. `encoding/json` is imported only for `RawMessage`, `Number`, and option constructors.

### Phase 4: Replace `Legacy` with the boundary option sets (one behaviour per PR)

- [ ] 1. `Deterministic(true)` and HTML escaping made explicit (`Wire`, `HTML`). No byte change expected.
- [ ] 2. `time.Duration` fields given an explicit unit. A `format:` tag is not available (row 10), so each field changes its Go type or gets an explicit marshal method.
  - [ ] `cmd/loom-longmemeval` (D6): `duration_ms`, `total_duration_ms`, and both `avg_duration_ms` fields become integer milliseconds (`int64`, matching the proto `*_ms` convention). `Summarize` and the text report convert at the edge.
  - [ ] Stored results are not rewritten or deleted. `loom-longmemeval score` reads a stored `[]EntryResult` file and writes the entry back inside `ScoredResult`, so scoring a file written before this change passes the nanosecond number through unchanged. The file has no version marker, so old and new files cannot be told apart by content. Record the cut-over commit and date in the longmemeval docs, and state the unit in any comparison that mixes old and new files.
  - [ ] `pkg/shuttle/human_tool.go:57` (`HumanRequest.Timeout`): traced 2026-11-19. Both stores write a `timeout_ms` integer column and never marshal the field. The one JSON writer is the webhook body in `JSONNotifier.Notify` (`human_tool.go:744`), shown as a usable notifier in `docs/guides/human-in-the-loop.md:549`. Give `Notify` its own payload struct with an integer `timeout` in nanoseconds (D8), and a test that pins the body bytes.
  - [ ] `pkg/metaagent/progress.go:67` (`Question.Timeout`): traced 2026-11-19. Nothing in the repo marshals `Question` or `ProgressEvent`; it is an in-process TUI event that also carries a channel. No wire exposure. Remove the unused `json` tags from both structs so a later marshal is a deliberate change.
- [ ] 3. Pointer-receiver `MarshalJSON` semantics (row 11). Review each by-value use of `RequestID` and `JSONSchema`.
- [ ] 4. nil slice and nil map output (row 4), one provider or protocol at a time. Each byte change reviewed against that API's contract.
- [ ] 5. `omitempty` v2 semantics for `any` and struct fields (rows 2 and 3).
- [ ] 6. Input strictness (rows 7–9) per D5. Dogfood: run `looms eval` on the agent suite before and after, and compare tool-call parse failures.
- [ ] 7. Errors: switch the one `SyntaxError` site to v2 error types, and feed the JSON Pointer from v2 errors into tool-argument retry prompts.
- [ ] 8. Delete `jsonopts.Legacy`.

**Exit**: `Legacy` is gone. Every remaining v1-compat option in the tree has a comment stating why it is permanent.

### Phase 5: Tests and enforcement

- [ ] Migrate the 154 test files (D7).
- [ ] Lint rule that forbids new calls to v1 `json.Marshal*`, `json.Unmarshal`, `json.NewEncoder`, `json.NewDecoder` (the `forbidigo` linter; a plain import ban would also block `json.RawMessage`).
- [ ] Update `CLAUDE.md` ("Common Mistakes to Avoid") and `docs/architecture/json-codec.md`.

## 6. Risks

| Risk | Where | Mitigation |
|---|---|---|
| A provider rejects or reinterprets a request after a byte change (`"temperature":0`, `[]` for `null`) | `pkg/llm`, 11 provider packages | Phase 2 snapshots; Phase 4 changes one behaviour per PR; live smoke test per provider where keys exist |
| Random map order lowers provider prompt-cache hit rates or changes content hashes | Row 5 | `Deterministic(true)` in every output option set; a test that marshals each tool schema 50 times and expects one output |
| Model-produced arguments silently lose fields | Row 7 | `ModelInput` keeps case-insensitive matching; differential fuzz test |
| XSS through unescaped JSON in HTML | Row 6 | `HTML` option set; a test with `</script>` payloads for each of the 6 files |
| Stored rows written before the migration fail to parse after it | `pkg/storage` | Read paths keep `Legacy` input semantics until a test over fixture rows from the current release passes under the new set |
| The 1.27 engine has further undocumented v1 differences like the U+FFFD one | Anywhere | The Phase 1b gate is the full race suite plus the conformance and integration suites, not the release notes |

## 7. Not verified

- `just check` itself was not run under 1.27.4. Its parts were run separately as described in §2.2.
- Integration tests (`just test-integration`, `just test-e2e-storage`), `just race-check`, cross-compiles, Docker builds, Linux, and Windows were not run under 1.27.4. The trial ran on macOS arm64 only.
- No live provider call was made under 1.27.4.
- `avmo-tera-cloud`'s Go version was not checked (no `go.mod` found at `~/Projects/avmo-tera-cloud`).
- Row 4 exposure and merge-semantics exposure are uncounted.

## 8. Sources

- Go 1.27 release notes: https://go.dev/doc/go1.27
- `encoding/json/v2` migration guide: https://go.dev/doc/jsonv2-migration
- `encoding/json` package docs, "Migrating to v2": https://pkg.go.dev/encoding/json
- `encoding/json/v2`: https://pkg.go.dev/encoding/json/v2
- `encoding/json/jsontext`: https://pkg.go.dev/encoding/json/jsontext
- Go releases: https://go.dev/dl/?mode=json
- golangci-lint releases: https://github.com/golangci/golangci-lint/releases
