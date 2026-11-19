# Vercel AI Gateway: completion plan

**Status**: ✅ Done (2026-11-19); in review as PR #691, branch `feat/vercel-gateway-complete` off `main` (b3a2d6ab + later).
**Goal**: move the `vercel-ai-gateway` provider from ⚠️ Partial to ✅ Implemented, and fix every loom defect the 2026-11-18/19 evaluation found (harness and results on branch `feat/vercel-gateway-eval`, a separate PR).

## Decisions (Ilsun, 2026-11-19)

1. Work goes on a new branch off `main`; PR #668 was squash-merged.
2. Plan decision #4 ("per-request BYOK out of scope") is reversed: build per-request BYOK, `providerTimeouts` and `sessionAffinity`.
3. `tools.shell_execute.mode` stays `bash` by default in the product. The eval harness keeps `jailed`.
4. OIDC is run live: the Vercel CLI is installed (`npm i -g vercel`, 65.15.0); the user logs in.

## Design notes

- **Secrets never sit in agent YAML.** A BYOK credential names environment variables for its secret fields (`env: {apiKey: LOOM_BYOK_ANTHROPIC_API_KEY}`); every name must start with `LOOM_BYOK_` (owner's decision in the pre-PR review). Non-secret fields (`region`, `resourceName`, `project`, `location`) may be literal under `values`. A missing variable is a build error that names neither the variable nor a value.
- **API surface is selectable.** New `LLMConfig` field `gateway_api`: `""`/`auto` (today's dual routing), `messages`, `chat`, `responses`. The Responses API takes any model id (verified live 2026-11-19 on `openai/gpt-5.5` and `anthropic/claude-sonnet-5.5`).
- **Responses client is its own package** (`pkg/llm/openairesponses`), not a mode of `pkg/llm/openai`: the request and stream shapes share almost nothing with Chat Completions. `store` is always `false`; reasoning is replayed by sending back `reasoning` items with `encrypted_content` (requested with `include: ["reasoning.encrypted_content"]`), because the gateway forbids `previous_response_id` with `gen_` ids.

## Checklist (priority order)

### P0 — defects found by the evaluation

- [x] **Stalled gateway request holds an agent for 300 s.** Shared stream reader in `pkg/llm`; anthropic client stream timeouts; the gateway streams every call (60 s first byte, 90 s idle, measured basis in the reference doc) and retries a stall once when nothing reached the caller. Truncated streams are errors (50cd502e).
- [x] **Refused command inside a shell chain is not a tool failure**: any block fails the call with `POLICY_BLOCKED` (597001b9).
- [ ] **Catalog `context_window` unused under serve** (review F4): being fixed by the pr-shepherd session (loom-b3) on `fix/vercel-gateway-followups`; not on this branch, to avoid two conflicting PRs.
- [ ] **Review F5** (`NormalizeProviderName` to a neutral helper; `readBounded` short reads): done on `fix/vercel-gateway-followups` (f19492ed, 30bb030d) by the pr-shepherd session.
- [x] **Fallback flag wrong for dated or dash-for-dot model echoes** (`gpt-5.5-2026-04-23`, `claude-sonnet-5-5`).
- [x] Empty-reply fallback masked Sonnet refusals (`pkg/agent/empty_reply.go`).
- [x] Jev pinned-id comment was wrong; `TestInitDecisionRouterFromConfig` depended on the shell's environment.
- [x] Eval harness: E4 criterion + two calibration items (recalibrated: 0.962 / 0.077), C5 case folding, cache fields in `latency.jsonl`, `judge -file` (fc1a3f84).

### P1 — finish the provider

- [x] **Proto first**: `LLMGatewayRouting` gains `byok`, `provider_timeouts_ms`, `session_affinity`, `disallow_prompt_training`, `inference_region`, `headquarters`, `user`, `tags`; `LLMConfig` gains `gateway_api`. `buf lint`, `buf breaking`, `buf generate`.
- [x] Typed `Routing` + validation + `ExtraBody` for every new key; YAML (agent and `looms.yaml`) mapping; env-var BYOK resolution.
- [x] `pkg/llm/openairesponses` client (Chat, ChatStream, tools, reasoning replay, usage, cost, errors, limiter, timeouts).
- [x] Wire `gateway_api: responses` through `vercelgateway.Client`; inspector reads `response.provider_metadata`.
- [x] Tiered prices from the catalog (proto `ModelInfo.price_tiers`; a83e4f96).
- [x] Gateway models in `ListAvailableModels` / the TUI picker (c380f5b5).

### P2 — live verification (each result recorded in the spike/eval research docs)

- [x] Responses API: text, tools over several turns, reasoning replay, streaming, cost.
- [x] Per-request BYOK: the key is sent and tried upstream; `providerTimeouts` accepted; fallback to system credentials observed. The test Anthropic key had no credit, so no call was served by it; Ilsun accepted this evidence (2026-11-19).
- [x] `sessionAffinity` / session id: `clientSessionIdSource` = `explicit`.
- [x] `anthropic-beta` accepted (unknown values too).
- [x] Thinking replay anthropic → bedrock and anthropic → vertex.
- [x] Cache usage fields from anthropic, bedrock and vertex.
- [x] `zeroDataRetention` accepted; its effect on routing is not visible in the response (recorded as unverified).
- [x] Error bodies: 402, 429 and mid-stream failure cannot be provoked on demand on a paid team; recorded as such.
- [x] Gateway timeouts: a 6 min 38 s stream completed; stall defaults from measured gaps.
- [x] OIDC: project token from `vercel env pull` (empty project `loom-gateway-oidc-test` on team loom13, created 2026-11-19 with the owner's approval); `TestLiveComplete_OIDC` passes on all three APIs.

### Found along the way

- [x] `TestRouteCorpusAgainstJev` ran (and failed the package at the 10-minute timeout) whenever a gateway key was exported; now opt-in with `LOOM_JEV_CORPUS=1` (8c1e72de).

### P3 — docs and status

- [x] Reference doc, providers doc, README, CLAUDE.md provider line, CHANGELOG (8f111ab8).
- [x] Status → ✅ only for what is implemented and verified; anything not provokable stays listed with the reason.
