# Arize Phoenix Observability Guide

**Version**: v1.4.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Confirm the Span Arrived](#confirm-the-span-arrived)
- [Security Notes](#security-notes)
- [Verification Record](#verification-record)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

Loom's OTLP/HTTP exporter (`observability.mode: otel`) can send traces to [Arize Phoenix](https://github.com/Arize-ai/phoenix), which accepts OTLP/HTTP on `/v1/traces`. No Phoenix SDK and no Phoenix-specific config keys are involved: the example `examples/config/otlp-phoenix.yaml` sets the endpoint, the project header and a restrictive tool policy.

**Status**

✅ Implemented:
- Export to a local Phoenix with `otlp_endpoint: http://127.0.0.1:6006/v1/traces`
- Project routing through `otlp_headers.x-project-name`
- A receiver test (`TestOTelTracerExportsToOTLPReceiver` in `pkg/observability/otel_test.go`) that checks the request path, the project header and the decoded span payload, and that `mode: none` sends nothing

📋 Planned (Phoenix views that are not yet correct):
- Content redaction and bounded previews for exported attributes ([#643](https://github.com/teradata-labs/loom/issues/643))
- OpenInference span kinds and token counts, so Phoenix labels LLM, tool and agent spans ([#644](https://github.com/teradata-labs/loom/issues/644))
- LLM and tool inputs and outputs in Phoenix's input/output panes ([#645](https://github.com/teradata-labs/loom/issues/645))
- Exported OTel IDs on Loom spans and session grouping ([#646](https://github.com/teradata-labs/loom/issues/646))
- One root span per turn, and the trace ID in `Weave` responses ([#647](https://github.com/teradata-labs/loom/issues/647))
- A nested span tree for each turn ([#648](https://github.com/teradata-labs/loom/issues/648))

Until those land, do not rely on Phoenix's span kinds, input/output panes, token counts or trace hierarchy for Loom spans. `WeaveResponse.trace_id` is not set, so this guide finds a turn by its session ID.

## Prerequisites

- `looms` built from this repository (`go build -tags fts5 ./cmd/looms`)
- An LLM provider. The example uses Bedrock; credentials come from `AWS_*` variables or a named profile, never from the YAML
- Phoenix, through Docker or pip. The [verification record](#verification-record) used Phoenix 20.22.0
- An agent with no tools for the smoke test (below)

## Quick Start

### 1. Start Phoenix on the loopback address

Docker, publishing only the UI and OTLP/HTTP port on `127.0.0.1`:

```bash
docker run -d --name phoenix -p 127.0.0.1:6006:6006 arizephoenix/phoenix:version-20.22.0
curl -fsS http://127.0.0.1:6006/healthz
```

pip (PowerShell):

```powershell
python -m pip install arize-phoenix==20.22.0
$env:PHOENIX_HOST = "127.0.0.1"
phoenix serve
```

Read [Security Notes](#security-notes) about port 4317 before using the pip install.

### 2. Create a no-tool agent

Save as `$LOOM_DATA_DIR/agents/phoenix-smoke.yaml` (default `~/.loom/agents/`):

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: phoenix-smoke
  description: No-tool agent for the Phoenix connection check

spec:
  system_prompt: |
    Answer in one short sentence.
```

The tool policy in `otlp-phoenix.yaml` (`yolo: false`, `require_approval: true`, `allowed_tools: []`, `default_action: deny`) hides every tool from the model, including the tools the server adds to every agent, because no approval mechanism is wired up yet.

### 3. Start Loom

Pick a fresh project name for each run so its spans are isolated, and clear the overrides described in [Security Notes](#security-notes) (this list covers the common ones; also unset any other `LOOM_TOOLS_PERMISSIONS_*` or `OTEL_EXPORTER_OTLP_*` variable you have set):

```powershell
Remove-Item Env:LOOM_YOLO, Env:OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, Env:OTEL_EXPORTER_OTLP_ENDPOINT, Env:LOOM_OTLP_ENDPOINT -ErrorAction SilentlyContinue
looms serve --config examples\config\otlp-phoenix.yaml
```

To use another project name without editing the file, copy it and change `otlp_headers.x-project-name`.

In the startup log, confirm the effective tool policy and the export endpoint. The `Tool permissions configuration` line must show `yolo` false, `require_approval` true and `allowed_tools` 0, and the `Observability enabled with OTLP export` line must show `http://127.0.0.1:6006/v1/traces`.

### 4. Send one turn with synthetic input

Use a session ID you have not used before; it identifies the turn's span.

```powershell
$session = "phoenix-smoke-" + [guid]::NewGuid().ToString("N").Substring(0, 8)
$body = @{ query = "Say hello."; sessionId = $session; agentId = "phoenix-smoke" } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:5006/v1/weave -ContentType application/json -Body $body
$session
```

Spans are batched and sent within about 5 seconds, and on shutdown.

## Confirm the Span Arrived

**Phoenix UI.** Open http://127.0.0.1:6006, select the project named in `x-project-name` (`loom-dev` in the example), and find the `server.Weave` span whose `session.id` attribute is the session ID from step 4.

**Phoenix REST API.** `GET /v1/projects/{project}/spans` returns at most 100 spans per request (up to 1000 with `limit`); follow `next_cursor` for the rest:

```python
import json
import sys
import urllib.parse
import urllib.request

def phoenix_spans(project, base="http://127.0.0.1:6006"):
    spans, cursor = [], None
    while True:
        params = {"limit": "1000"}
        if cursor:
            params["cursor"] = cursor
        url = f"{base}/v1/projects/{urllib.parse.quote(project)}/spans?{urllib.parse.urlencode(params)}"
        with urllib.request.urlopen(url) as r:
            page = json.load(r)
        spans += page["data"]
        cursor = page.get("next_cursor")
        if not cursor:
            return spans

project, session = sys.argv[1], sys.argv[2]
turn = [s for s in phoenix_spans(project)
        if s["name"] == "server.Weave" and s.get("attributes", {}).get("session.id") == session]
print(len(turn), "server.Weave span(s) for", session)
```

Expected output: `1 server.Weave span(s) for phoenix-smoke-<id>`.

## Security Notes

**Verify the effective tool policy before running.** `yolo: false` in the file is not sufficient: `--yolo`, `LOOM_YOLO=true` (or `1`) and `LOOM_TOOLS_PERMISSIONS_*` environment variables override it. Unset them, do not pass `--yolo`, and check the `Tool permissions configuration` log line shows `"yolo": false` and `"require_approval": true`.

**Plaintext HTTP on loopback only.** `otlp_insecure: true` with an `http://` endpoint sends traces unencrypted. Keep it on `127.0.0.1`. For a remote Phoenix use an `https://` endpoint and `otlp_insecure: false`. The example also binds Loom's own gRPC and HTTP servers to `127.0.0.1`.

**Traces can contain sensitive data.** Exported spans can hold prompts, responses, tool arguments and results, and error messages. With `mode: otel`, `looms serve` drops credential-named attributes and masks email, phone, SSN and card-number patterns in string attributes, but that is not a privacy guarantee: other values, including span status messages, are exported as they are. Full redaction and content bounds are planned in [#643](https://github.com/teradata-labs/loom/issues/643). Use synthetic input and inspect what Phoenix stored before sending real data.

**Phoenix's OTLP/gRPC port 4317.** Phoenix also runs an OTLP/gRPC receiver on port 4317. With Docker, it is not reachable from outside the container unless you publish it (`-p`); the command above publishes only 6006, on `127.0.0.1`. With pip, `PHOENIX_HOST` moves only the HTTP listener: in the [verification record](#verification-record) run, 6006 was on `127.0.0.1` and 4317 on `::` (every interface). Check the bind after starting Phoenix:

```powershell
Get-NetTCPConnection -State Listen -LocalPort 4317, 6006 | Select-Object LocalAddress, LocalPort
```

```bash
ss -ltnp | grep -E ':(4317|6006)\b'
```

If 4317 is bound to `0.0.0.0` or `::`, block inbound TCP 4317 with the host firewall (Windows, from an elevated prompt: `New-NetFirewallRule -DisplayName "Block Phoenix OTLP gRPC" -Direction Inbound -Protocol TCP -LocalPort 4317 -Action Block`), or use Docker instead. Stop Phoenix when you are done.

## Verification Record

Run on 2026-11-19, Windows 11, Loom commit `aef3f2e1` built with `go build -tags fts5 ./cmd/looms`, Phoenix 20.22.0 from pip (`PHOENIX_HOST=127.0.0.1`), the example config with a fresh project `loom-642-20261119-135834`, the `phoenix-smoke` agent and Bedrock Claude Sonnet 4.5. Each turn used the synthetic input `Say hello.` and its own session ID.

| Check | Result |
|---|---|
| Startup log | `yolo` false, `require_approval` true, `allowed_tools` 0, `default_action` deny; Loom on `127.0.0.1:60051` and `127.0.0.1:5006` |
| Turn 1, Phoenix up | ✅ One `server.Weave` span with that turn's `session.id` in the project, found with the REST script above (44 spans in the project) |
| Turn 2, Phoenix stopped | ✅ Completed in 2.9 s (10-second deadline); `looms` logged `traces export: ... connection refused`; no span for that session after Phoenix returned (not replayed) |
| Turn 3, after Phoenix restart | ✅ One `server.Weave` span with the new turn's `session.id` |
| Phoenix listeners | 6006 on `127.0.0.1`; 4317 on `::` (every interface) despite `PHOENIX_HOST` |

`WeaveResponse.traceId` was empty, as expected on this commit ([#647](https://github.com/teradata-labs/loom/issues/647)). This is one local run, not a latency guarantee.

## Troubleshooting

**No spans in Phoenix.**
- `otlp_endpoint` must end in `/v1/traces`.
- Check you are viewing the project named in `x-project-name`.
- `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_ENDPOINT` and `LOOM_OTLP_ENDPOINT` replace `otlp_endpoint`, and their `*_HEADERS` counterparts replace `otlp_headers`. Unset them.
- `observability.enabled` must be `true` and `mode` must be `otel`. Loom logs `Observability enabled with OTLP export` at startup, or `Failed to create OTLP tracer, using no-op tracer` when the exporter cannot be built.
- Spans are sent in batches; wait about 5 seconds after the turn, or stop `looms` to flush.

**Spans land in the `default` project.** The `x-project-name` header did not reach Phoenix, usually because an `OTEL_EXPORTER_OTLP_*_HEADERS` variable replaced `otlp_headers`.

**The turn fails with a tool permission error.** The agent called a tool. Use the no-tool agent from step 2; this example's policy denies every tool.

**Phoenix is down.** Export runs in a background batch processor, off the request path, so turns do not wait on Phoenix (see the [verification record](#verification-record)). `looms` logs `traces export: ...` errors. Spans the exporter cannot deliver are dropped and are not replayed when Phoenix returns.

## Next Steps

- [OTLP integration architecture](../../architecture/otlp-integration.md): exporter configuration and attribute mapping
- [Embedded observability](embedded-observability-guide.md): local trace storage
- [Observability with Hawk](observability.md)
