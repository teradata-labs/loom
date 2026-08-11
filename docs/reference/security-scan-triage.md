# Security Scan Triage

Triage record for security-scanner findings against `teradata-labs/loom:main` —
Snyk SAST/SCA via ArmorCode (2026-07-22) and GitHub CodeQL (2026-07/08). Each class
of finding was verified against the code before being classified. This document
exists so future scans don't re-litigate the same findings from scratch.

## Fixed

| Finding | Location | Fix |
|---|---|---|
| SQL Injection ×3 (High) | `pkg/storage/sql_result_store.go` | PR #267 (`306728f`): every table-name interpolation site (`CREATE`/`INSERT`/`DROP`/`SELECT`) goes through `sanitizeIdentifier`, a strict `[A-Za-z0-9_]` allowlist. |
| Go stdlib SCA findings on `go.mod` — "Symlink Attack" (High), "Memory Allocation with Excessive Size" ×2, "Cleartext Transmission" (Medium) | `go.mod` | These map to Go standard-library CVEs (os.Root symlink escape GO-2026-4602, archive/tar GO-2026-4869, net/url GO-2026-4341, crypto/tls GO-2026-4337/5856) keyed off the `go` directive. Fixed by bumping `go.mod` to `go 1.26.0` / `toolchain go1.26.5` and aligning all CI workflows and `deploy/Dockerfile` to Go 1.26. |
| Path Traversal (Medium) | `pkg/artifacts/store.go` | `ValidateSessionID` (strict allowlist: `[A-Za-z0-9._-]`, no `..`) now gates `GetArtifactDir` and `GetScratchpadDir`; `session_metadata.go`'s weaker blocklist validator delegates to it. Session IDs arrive from API callers via context, so this was a real (if low-exploitability) gap. |
| Insufficient postMessage Validation (Low) | `pkg/mcp/apps/html/data-chart.html` | Adopted the trust-on-first-use origin-pinning guard already used by the other three MCP app viewers, plus a payload shape check (`labels`/`values` arrays). |

## CodeQL follow-up (2026-07-23)

The PR #271 hardening itself surfaced three `go/path-injection` CodeQL alerts
(#666–668) in `pkg/artifacts/session_metadata.go` (`WriteSessionArtifactMetadata`:
`os.MkdirAll`, `os.CreateTemp`, `os.Rename`). Not exploitable — `ValidateSessionID`
already allowlists `[A-Za-z0-9._-]` and rejects `..` — but CodeQL doesn't model that
function as a sanitizer. A containment pattern (`filepath.Clean` + `filepath.Rel` +
`filepath.IsLocal`) was added inside `SessionArtifactsRoot`, the single choke point
every session metadata path derives from; `ReadSessionArtifactMetadata`'s duplicate
inline check was folded into it. **Correction (2026-08-11): that pattern did not
close the alerts** — the guard was applied to a derived value CodeQL doesn't track
back to the sink. See the next section for the root cause and the fix that worked.

## CodeQL go/path-injection + go/zipslip (2026-08-11)

Triage of the five open CodeQL alerts on `main`: #666–669 (`go/path-injection`,
`pkg/artifacts/session_metadata.go` — `os.MkdirAll`/`os.CreateTemp`/`os.Rename` in
`WriteSessionArtifactMetadata`, `os.ReadFile` in `ReadSessionArtifactMetadata`) and
#670 (`go/zipslip`, `pkg/server/skills_import.go` `extractZipToTempDir`).

**Assessment: all five not exploitable.**

- #666–669: every flagged path derives from `SessionArtifactsRoot`, gated by
  `ValidateSessionID` (strict `[A-Za-z0-9._-]` allowlist — no separators, no `..`).
  Hostile IDs (`../x`, `a/b`, `/etc`, …) are covered by
  `TestSessionArtifactsRoot_rejectsBadPath`. The metadata feature is also
  default-off (`SessionMetadataEnabled`).
- #670: `extractZipToTempDir` cleans each entry name, rejects escapes, extracts
  into a fresh `MkdirTemp` dir, writes only regular files (Go `archive/zip` never
  materializes symlinks through this path), and caps decompression at 64MB.
  `TestAddSkill_RejectsHostileZip` covers escaping entries.

**Why the 2026-07 guards didn't register** (verified against the query sources,
[`TaintedPathCustomizations.qll`](https://github.com/github/codeql/blob/main/go/ql/lib/semmle/go/security/TaintedPathCustomizations.qll)
and
[`ZipSlipCustomizations.qll`](https://github.com/github/codeql/blob/main/go/ql/lib/semmle/go/security/ZipSlipCustomizations.qll)):

- CodeQL models `filepath.IsLocal` as a barrier guard (`IsLocalCheck`), but a guard
  sanitizes only the exact expression it checks. Both sites guarded a derived
  `rel` value while the tainted `root`/`dest` flowed on to the sinks untouched.
- The zip check `strings.HasPrefix(rel, ".."+string(filepath.Separator))` also
  fails CodeQL's `DotDotCheck`, which requires a string literal (`".."`, `"../"`,
  `"..\\"`); its generic `PrefixCheck` sanitizes on the *true* branch (the
  `HasPrefix(path, safeDir)` idiom) — the opposite polarity.

**Fix.** Guard the tainted value itself with `filepath.IsLocal` before it is joined:
`SessionArtifactsRoot` now checks `filepath.IsLocal(sessionID)` (one shared choke
point closes #666–669), and `extractZipToTempDir` checks `filepath.IsLocal(name)`
right after `filepath.Clean`, replacing the `IsAbs` + `Rel`/`HasPrefix` block it
strictly subsumes (closes #670). Semantics are equal-or-stricter: the only newly
rejected inputs are Windows drive-relative paths (`C:x`) and reserved device names
(`NUL`). Lesson for future scans: **place the analyzer-modeled check on the exact
variable that reaches the sink**, not on a value derived from it.

## No fix available upstream

| Finding | Location | Status |
|---|---|---|
| `github.com/docker/docker` vulns (incl. the "Symlink Attack"-class docker-cp races GO-2026-5617/5668, archive endpoint GO-2026-5746, AuthZ bypass GO-2026-4887/4883) | `go.mod` | Docker migrated the engine to `github.com/moby/moby/v2`; fixes exist only there (≥ 2.0.0-beta.14, still beta). Every version of the `docker/docker` module path is permanently marked vulnerable. All five CVEs are **daemon-side**; Loom uses only the client SDK (`pkg/docker/executor.go`). Revisit when moby/v2 reaches GA. |

## Verified false positives / intentional behavior

- **Hardcoded passwords/credentials ×34 in `_test.go` files** (`pkg/backends/teradata`, `pkg/backends/supabase`, `internal/pgxdriver`, `cmd/looms`, `pkg/artifacts`): all obviously fake fixtures (`testpass`, `secret`, URL-escaping cases like `p@ss/w:rd?#`, a bare JWT header with no payload/signature).
- **Hardcoded Credentials ×2 in `pkg/agent/session_store.go`**: the string `"default-user"` — a single-tenant identity label for the SQLite backend, not a credential.
- **Hardcoded Secret in `scripts/longmemeval-eval/evaluate_qa.py`**: `openai_api_key = "EMPTY"` — the conventional placeholder required by the OpenAI client when pointing at a local vLLM server. Real keys come from `OPENAI_API_KEY`.
- **XSS in `pkg/server/http.go`**: the only reflected value is escaped with both `url.PathEscape` and `html.EscapeString`; app names are allowlist-validated (`[A-Za-z0-9_-]`); responses carry nosniff, X-Frame-Options, and a strict CSP.
- **Permissive TrustManager in `cmd/loom/skills_classify.go` and `pkg/tui/client/client.go`**: `InsecureSkipVerify` only activates behind the default-off `--tls-insecure` CLI flag (self-signed cert support); otherwise system cert pool + optional `--tls-ca`. Both sites now carry `//nolint:gosec` annotations.
- **Permissive TrustManager in `internal/supabaseauth/management_test.go`**: test-only client talking to a local `httptest` loopback server.
- **Path Traversal ×14 in `cmd/looms/cmd_serve.go`, `cmd/loom-mcp`, `cmd/workflow-viz`, `cmd/loom-bench-harness`, and the longmemeval eval scripts**: every flagged path is a CLI flag, positional argument, or operator-authored config — standard "operator picks the file" cases with no network attack surface.
- **postMessage validation in `conversation-viewer.html`, `explain-plan-visualizer.html`, `data-quality-dashboard.html`**: all three already implement TOFU origin pinning plus JSON-RPC 2.0 shape validation. The host origin is unknowable at build time under the `ui://` MCP Apps scheme, so `'*'` is used only for the initial handshake before locking.
