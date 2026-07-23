# Security Scan Triage — Snyk Findings (2026-07)

Triage record for the Snyk SAST/SCA findings reported against `teradata-labs/loom:main`
via ArmorCode as of 2026-07-22. Each class of finding was verified against the code
before being classified. This document exists so future scans don't re-litigate the
same findings from scratch.

## Fixed

| Finding | Location | Fix |
|---|---|---|
| SQL Injection ×3 (High) | `pkg/storage/sql_result_store.go` | PR #267 (`306728f`): every table-name interpolation site (`CREATE`/`INSERT`/`DROP`/`SELECT`) goes through `sanitizeIdentifier`, a strict `[A-Za-z0-9_]` allowlist. |
| Go stdlib SCA findings on `go.mod` — "Symlink Attack" (High), "Memory Allocation with Excessive Size" ×2, "Cleartext Transmission" (Medium) | `go.mod` | These map to Go standard-library CVEs (os.Root symlink escape GO-2026-4602, archive/tar GO-2026-4869, net/url GO-2026-4341, crypto/tls GO-2026-4337/5856) keyed off the `go` directive. Fixed by bumping `go.mod` to `go 1.26.0` / `toolchain go1.26.5` and aligning all CI workflows and `deploy/Dockerfile` to Go 1.26. |
| Path Traversal (Medium) | `pkg/artifacts/store.go` | `ValidateSessionID` (strict allowlist: `[A-Za-z0-9._-]`, no `..`) now gates `GetArtifactDir` and `GetScratchpadDir`; `session_metadata.go`'s weaker blocklist validator delegates to it. Session IDs arrive from API callers via context, so this was a real (if low-exploitability) gap. |
| Insufficient postMessage Validation (Low) | `pkg/mcp/apps/html/data-chart.html` | Adopted the trust-on-first-use origin-pinning guard already used by the other three MCP app viewers, plus a payload shape check (`labels`/`values` arrays). |

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
