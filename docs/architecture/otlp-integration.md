# OTLP Integration Architecture

Architecture for exporting Loom traces via OpenTelemetry Protocol (OTLP), enabling backend-agnostic observability across Opik, Jaeger, Grafana Tempo, Honeycomb, Datadog, and any OTLP-compliant system.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.3.0

---

## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [Current State](#current-state)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
  - [OTelTracer](#oteltracer)
  - [Attribute Translation Layer](#attribute-translation-layer)
  - [OTelConfig](#otelconfig)
  - [Auto-Select Extension](#auto-select-extension)
- [Key Interactions](#key-interactions)
  - [Span Export Flow](#span-export-flow)
  - [Attribute Translation Flow](#attribute-translation-flow)
- [Data Structures](#data-structures)
  - [OTelConfig](#otelconfig-struct)
  - [Span Mapping Table](#span-mapping-table)
- [Algorithms](#algorithms)
  - [Loom-to-OTel Span Bridge](#loom-to-otel-span-bridge)
  - [GenAI Attribute Translation](#genai-attribute-translation)
- [Design Trade-offs](#design-trade-offs)
- [Constraints and Limitations](#constraints-and-limitations)
- [Performance Characteristics](#performance-characteristics)
- [Concurrency Model](#concurrency-model)
- [Error Handling](#error-handling)
- [Security Considerations](#security-considerations)
- [Backend Compatibility](#backend-compatibility)
- [Related Work](#related-work)
- [References](#references)
- [Further Reading](#further-reading)


## Overview

Loom's observability system (`pkg/observability`) currently exports traces to two destinations: Hawk (proprietary HTTP API) and an embedded in-process store (memory or SQLite). Neither destination speaks the OpenTelemetry Protocol (OTLP), which is the industry standard for trace export adopted by all major observability platforms.

This document describes the design for an `OTelTracer` — a new `Tracer` implementation that bridges Loom's internal span model to the OTel SDK, enabling trace export to any OTLP-compliant backend via a single configuration change.

The primary motivation is **backend freedom**: a Loom deployment wired for Opik today should be rewirable for Jaeger or Grafana Tempo tomorrow with no code change — only a different `otlp_endpoint` in `looms.yaml`.


## Design Goals

1. **Backend-agnostic**: Any OTLP HTTP endpoint works without code changes
2. **Standard attribute mapping**: Loom attributes translate to OTel GenAI semantic conventions (`gen_ai.*`) so backends render LLM/tool spans correctly out of the box
3. **Zero instrumentation change**: Existing `StartSpan` / `EndSpan` call sites are untouched
4. **Consistent config pattern**: `mode: otel` follows the same `looms.yaml` / env var pattern as `mode: service` and `mode: embedded`
5. **Privacy preservation**: PII redaction applied before export, same policy as HawkTracer
6. **No new required dependencies**: OTel SDK packages are already in `go.mod` as indirect deps

**Non-goals**:
- gRPC OTLP transport (HTTP-only; Opik and most backends support HTTP)
- OTel metrics export (Loom's `RecordMetric` remains internal only for now)
- Distributed trace propagation across process boundaries (single-process server)
- OTel logs API integration


## Current State

Understanding what already exists clarifies exactly what must be built.

### What Is Present

```
pkg/observability/
  interface.go        ✅  Tracer + SpanExporter interfaces defined
  types.go            ✅  Span, Event, Status, ResourceAttributes
  instrumentation.go  ✅  80+ span name + attribute constants (AttrLLM*, AttrTool*, etc.)
  hawk.go             ✅  HTTP export tracer (HawkTracer) — reference implementation
  embedded.go         ✅  In-process tracer with SpanExporter hook
  noop.go             ✅  Zero-overhead tracer for testing
  auto_select.go      ✅  Mode-based tracer factory (service / embedded / none / auto)

go.mod                ✅  OTel SDK already present as indirect deps:
                          go.opentelemetry.io/otel v1.43.0
                          go.opentelemetry.io/otel/trace v1.43.0
                          go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0

cmd/looms/config.go   ✅  ObservabilityConfig.Provider = "hawk, otlp" (comment placeholder)
                      ✅  mode switch in cmd_serve.go (service / embedded / none)
```

### What Is Missing

```
pkg/observability/otel.go         ✅  OTelTracer implementation
pkg/observability/otel_attrs.go   ✅  Loom AttrLLM* → gen_ai.* translation map
pkg/observability/otel_config.go  ✅  OTelConfig struct + env var loading
                                      (incl. LOOM_OTLP_INSECURE resolution)
pkg/observability/otel_test.go    ✅  Unit tests with behavioral assertions
                                      (export count checks, LOOM_OTLP_INSECURE
                                       env resolution, errcheck-safe Shutdown)

cmd/looms/config.go               ✅  OTLPEndpoint / OTLPHeaders fields on ObservabilityConfig
cmd/looms/cmd_serve.go            ✅  case "otel": branch in tracer switch
pkg/observability/auto_select.go  ✅  TracerModeOTel constant + selection logic
                                      (auto mode reads OTEL_EXPORTER_OTLP_TRACES_ENDPOINT /
                                       LOOM_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_TRACES_HEADERS /
                                       LOOM_OTLP_HEADERS, LOOM_OTLP_INSECURE, OTEL_SERVICE_NAME)
pkg/agent/agent.go                ✅  message.preview / response.preview truncated to 200 runes
                                      (privacy + payload size guard on Chat / ChatWithProgress)
```

### Key Insight: SpanExporter Already Exists

`pkg/observability/interface.go` defines a `SpanExporter` interface already used by `EmbeddedTracer` for dual-write:

```go
type SpanExporter interface {
    ExportSpans(ctx context.Context, spans []*Span) error
    ForceFlush(ctx context.Context) error
    Shutdown(ctx context.Context) error
}
```

This interface could host an `OTLPSpanExporter` implementation as a lighter alternative to a full `OTelTracer`. The trade-off between these two approaches is analyzed in [Design Trade-offs](#design-trade-offs).


## System Context

```
┌──────────────────────────────────────────────────────────────────────┐
│                         External Environment                         │
│                                                                      │
│  ┌──────────┐     ┌──────────────────┐     ┌────────────────────┐  │
│  │  Agent   │────▶│   Loom Server    │────▶│  OTLP Backend      │  │
│  │  (user)  │     │  (looms serve)   │     │  (Opik / Jaeger /  │  │
│  └──────────┘     └──────────────────┘     │   Tempo / Datadog) │  │
│                          │                 └────────────────────┘  │
│                          │ also exports to                          │
│                   ┌──────▼──────┐                                   │
│                   │  Hawk       │                                   │
│                   │  (existing) │                                   │
│                   └─────────────┘                                   │
└──────────────────────────────────────────────────────────────────────┘
```

**External Dependencies**:
- **OTLP HTTP endpoint**: Any backend accepting `POST /v1/traces` (OTLP HTTP format)
- **OTel SDK**: `go.opentelemetry.io/otel` — already in `go.mod`
- **LLM Providers**: Instrumented via `InstrumentedProvider`; spans carry `AttrLLM*` attributes that translate to `gen_ai.*` on export
- **Agent Runtime**: Unchanged — `StartSpan` / `EndSpan` call sites require no modification


## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│                         Observability System                             │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │                     Tracer Interface                               │  │
│  │   StartSpan / EndSpan / RecordMetric / RecordEvent / Flush        │  │
│  └──────────────────────────┬─────────────────────────────────────────┘  │
│                             │ implements                                  │
│         ┌───────────────────┼────────────────────┬──────────────┐        │
│         ▼                   ▼                    ▼              ▼        │
│  ┌─────────────┐  ┌──────────────────┐  ┌──────────────┐  ┌──────────┐  │
│  │ HawkTracer  │  │  EmbeddedTracer  │  │  OTelTracer  │  │ NoOp     │  │
│  │ (HTTP/Hawk) │  │  (Mem / SQLite)  │  │  (OTLP HTTP) │  │ Tracer   │  │
│  └─────────────┘  └──────────────────┘  └──────┬───────┘  └──────────┘  │
│                                                 │                        │
│                   ┌─────────────────────────────┘                        │
│                   │                                                       │
│                   ▼                                                       │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │                       OTelTracer Internals                         │  │
│  │                                                                    │  │
│  │  ┌───────────────┐     ┌─────────────────┐     ┌───────────────┐  │  │
│  │  │  Loom Span    │────▶│ Attribute        │────▶│  OTel Span   │  │  │
│  │  │  (StartSpan)  │     │ Translator       │     │  (SDK trace) │  │  │
│  │  └───────────────┘     │ Loom → gen_ai.*  │     └──────┬────────┘  │  │
│  │                        └─────────────────┘            │           │  │
│  │                                                        ▼           │  │
│  │  ┌───────────────┐     ┌─────────────────┐     ┌───────────────┐  │  │
│  │  │  Privacy      │────▶│  OTel SDK        │────▶│  OTLP HTTP   │  │  │
│  │  │  Redaction    │     │  BatchSpanProc   │     │  Exporter    │  │  │
│  │  └───────────────┘     └─────────────────┘     └──────┬────────┘  │  │
│  │                                                        │           │  │
│  └────────────────────────────────────────────────────────────────────┘  │
│                                                           │               │
│                                                           ▼               │
│                                              ┌────────────────────────┐   │
│                                              │  OTLP Backend          │   │
│                                              │  (Opik / Jaeger /      │   │
│                                              │   Tempo / Honeycomb)   │   │
│                                              └────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────┘
```

The `OTelTracer` owns an OTel `TracerProvider` internally. On `StartSpan`, it creates both a Loom `*Span` (for the existing context propagation chain) and an OTel span (held in a `sync.Map` keyed by Loom `SpanID`). On `EndSpan`, it translates Loom attributes to `gen_ai.*` semantic conventions, applies privacy redaction, and ends the OTel span — letting the OTel SDK batch and export via OTLP HTTP.


## Components

### OTelTracer

**Responsibility**: Implement the `Tracer` interface, bridging Loom's span lifecycle to the OTel SDK while preserving Loom's context propagation model.

**File**: `pkg/observability/otel.go` (📋 to be created)

**Core Structure**:
```
┌──────────────────────────────────────────────────────┐
│                    OTelTracer                        │
│                                                      │
│  provider    *sdktrace.TracerProvider                │
│  tracer      otelTrace.Tracer                        │
│  activeSpans sync.Map  (loomSpanID → otelSpan)       │
│  redact      func(*Span) *Span  (privacy)            │
│  cfg         OTelConfig                              │
│                                                      │
│  StartSpan(ctx, name, opts) → (ctx, *Span)           │
│    1. Create Loom span (same as NoOpTracer)          │
│    2. Start OTel span via t.tracer.Start()           │
│    3. Store otelSpan in activeSpans[loom.SpanID]     │
│    4. Return context carrying Loom span              │
│                                                      │
│  EndSpan(span)                                       │
│    1. Calculate duration (same as all tracers)       │
│    2. Apply privacy redaction                        │
│    3. Load otelSpan from activeSpans                 │
│    4. Call translateAttrs(otelSpan, span.Attributes) │
│    5. Set OTel span status from span.Status          │
│    6. Call otelSpan.End()                            │
│                                                      │
│  Flush(ctx) → error                                  │
│    ForceFlush on TracerProvider                      │
│                                                      │
│  Shutdown(ctx) → error                               │
│    Graceful shutdown of TracerProvider               │
└──────────────────────────────────────────────────────┘
```

**OTel TracerProvider setup** at construction time:
```
NewOTelTracer(cfg OTelConfig):
  1. Build otlptracehttp.Exporter with cfg.Endpoint + cfg.Headers
  2. Build sdktrace.TracerProvider with:
       - BatchSpanProcessor(exporter)
       - Resource(service.name = cfg.ServiceName, ...)
  3. Register as global (optional) or keep local
  4. Return OTelTracer{provider, tracer, ...}
```

**Build tag**: No additional build tag required. OTel SDK packages are in `go.mod`. Consider `//go:build otel` for clean separation only if binary size is a concern.

**Invariants**:
```
∀ span s: s.SpanID ∈ activeSpans during [StartSpan, EndSpan]
∀ span s: activeSpans[s.SpanID] deleted after EndSpan
OTel span.TraceID = Loom span.TraceID (W3C hex format)
OTel span.ParentSpanID = Loom span.ParentID (when non-empty)
```


### Attribute Translation Layer

**Responsibility**: Map Loom's internal attribute constants (`AttrLLM*`, `AttrTool*`, etc.) to OTel's GenAI semantic conventions (`gen_ai.*`). This layer is what makes traces render correctly in Opik, Grafana, and other backends without custom configuration.

**File**: `pkg/observability/otel_attrs.go` (📋 to be created)

**Translation is applied at `EndSpan` time**, not `StartSpan` — attributes are often set by calling code between the two, so translation must happen on completion.

**Full attribute mapping** (see [Span Mapping Table](#span-mapping-table) for complete list).

**Span kind assignment** by span name prefix:

```
"llm.*"        → SpanKindClient   (outbound LLM API call)
"tool.*"       → SpanKindInternal (internal tool execution)
"agent.*"      → SpanKindInternal (agent orchestration)
"backend.*"    → SpanKindClient   (outbound backend query)
"mcp.*"        → SpanKindClient   (outbound MCP call)
"workflow.*"   → SpanKindInternal (orchestration)
default        → SpanKindInternal
```

**Rationale**: Span kind affects how backends display latency and error rates. LLM and backend calls are modelled as client calls because they initiate outbound I/O with measurable latency.


### OTelConfig

**Responsibility**: Carry all configuration required to construct an `OTelTracer` and resolve from environment variables.

**File**: `pkg/observability/otel_config.go` (📋 to be created)

```
OTelConfig
  Endpoint       string            // OTLP HTTP endpoint URL
  Headers        map[string]string // Request headers (API keys, workspace IDs)
  Insecure       bool              // Skip TLS verification (local dev only)
  ServiceName    string            // resource: service.name
  ServiceVersion string            // resource: service.version
  Timeout        time.Duration     // Per-request timeout (default: 10s)
  FlushInterval  time.Duration     // BatchSpanProcessor flush interval (default: 5s)
  MaxBatchSize   int               // BatchSpanProcessor batch size (default: 512)
  Privacy        PrivacyConfig     // PII redaction (reused from HawkTracer)
```

**Standard OTel environment variable support** (resolved automatically if config fields are empty):

```
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT    ← Endpoint
OTEL_EXPORTER_OTLP_TRACES_HEADERS     ← Headers (comma-separated key=value)
OTEL_SERVICE_NAME                      ← ServiceName
OTEL_SERVICE_VERSION                   ← ServiceVersion
```

These are the canonical env vars used by the OTel spec — any operator familiar with OTel will expect them to work.

**Loom-specific fallback env vars** (when OTel standard vars are not set):

```
LOOM_OTLP_ENDPOINT      ← Endpoint
LOOM_OTLP_HEADERS       ← Headers
LOOM_OTLP_INSECURE      ← Insecure (default: false)
```


### Auto-Select Extension

**Responsibility**: Extend the existing tracer factory to recognise `mode: otel` and construct an `OTelTracer`.

**File**: `pkg/observability/auto_select.go` (📋 to modify)

Current `TracerMode` constants:
```go
TracerModeAuto     TracerMode = "auto"
TracerModeService  TracerMode = "service"
TracerModeEmbedded TracerMode = "embedded"
TracerModeNone     TracerMode = "none"
```

Addition:
```go
TracerModeOTel TracerMode = "otel"   // 📋 add
```

The `autoSelectTracer` switch gains a `case TracerModeOTel:` branch. The auto-selection heuristic for `TracerModeAuto` extends to: if `OTLPEndpoint` is set, `otel` mode is preferred over `embedded`.

**`cmd/looms/config.go` changes**:

```
ObservabilityConfig (existing fields unchanged):
  Enabled       bool
  Provider      string   // "hawk", "otlp"
  Mode          string   // "embedded", "service", "none", "otel"  ← add "otel"
  HawkEndpoint  string
  HawkAPIKey    string
  StorageType   string
  SQLitePath    string
  FlushInterval string

  OTLPEndpoint  string            // 📋 add — OTLP HTTP endpoint
  OTLPHeaders   map[string]string // 📋 add — auth headers
  OTLPInsecure  bool              // 📋 add — skip TLS (dev only)
```

**`cmd/looms/cmd_serve.go` change** — add one case to the tracer switch:

```
case "otel":
    otelTracer, err := observability.NewOTelTracer(observability.OTelConfig{
        Endpoint:    config.Observability.OTLPEndpoint,
        Headers:     config.Observability.OTLPHeaders,
        Insecure:    config.Observability.OTLPInsecure,
        ServiceName: config.Server.Name,
        Privacy:     privacyCfg,
    })
    if err != nil { ... fallback to NoOpTracer }
    tracer = otelTracer
```


## Key Interactions

### Span Export Flow

```
Agent Code      OTelTracer       OTel SDK          OTLP HTTP
    │               │                │                  │
    ├─ StartSpan ──▶│                │                  │
    │               ├─ newLoomSpan   │                  │
    │               ├─ tracer.Start ▶│                  │
    │               │◀─ otelSpan ────┤                  │
    │               ├─ store(loomID, otelSpan)           │
    │◀─ (ctx, span) ┤                │                  │
    │               │                │                  │
    ├─ span.SetAttr  │                │                  │
    ├─ span.AddEvent │                │                  │
    │               │                │                  │
    ├─ EndSpan ─────▶│                │                  │
    │               ├─ calcDuration   │                  │
    │               ├─ redact(span)   │                  │
    │               ├─ translateAttrs │                  │
    │               ├─ load otelSpan  │                  │
    │               ├─ otelSpan.SetAttributes            │
    │               ├─ otelSpan.SetStatus                │
    │               ├─ otelSpan.End ─▶│                  │
    │               │                ├─ BatchProc.OnEnd  │
    │               │                │  (buffered)       │
    │               │                │                  │
    │               │                │ (batch full / tick)
    │               │                ├─ POST /v1/traces ▶│
    │               │                │◀─ 200 OK ─────────┤
    │               │                │                  │
```

**Invariant**: `otelSpan.End()` is called exactly once per `StartSpan`, via `defer tracer.EndSpan(span)`. The OTel SDK enforces single-end semantics; double-end is a no-op in the SDK.


### Attribute Translation Flow

```
Loom Span.Attributes        translateAttrs()        OTel Span
  {                               │                  .SetAttribute(
    "llm.provider": "anthropic"   ├── gen_ai.system      "gen_ai.system", "anthropic")
    "llm.model": "claude-..."     ├── gen_ai.request.model  "claude-...")
    "llm.tokens.input": 1234      ├── gen_ai.usage.input_tokens  1234)
    "llm.tokens.output": 567      ├── gen_ai.usage.output_tokens  567)
    "llm.cost": 0.0023            ├── (pass-through as "loom.llm.cost")
    "tool.name": "query_db"       ├── gen_ai.tool.name  "query_db")
    "session.id": "abc-123"       ├── session.id  "abc-123")
    "error.message": "timeout"    └── exception.message  "timeout")
  }
```

**Pass-through rule**: Loom attributes with no OTel equivalent are forwarded with a `loom.` prefix. This preserves Loom-specific metadata (e.g., `loom.llm.cost`, `loom.pattern.name`) without polluting the `gen_ai.*` namespace.

**Privacy-first ordering**: Redaction runs before translation. Attributes removed by the redactor are never passed to `translateAttrs`.


## Data Structures

### OTelConfig Struct

**Definition** (`pkg/observability/otel_config.go`):
```
OTelConfig
  Endpoint       string            Required. Full OTLP HTTP URL including path.
  Headers        map[string]string Optional. Authorization, workspace, project headers.
  Insecure       bool              Optional. Disables TLS verification. Dev only.
  ServiceName    string            Populates resource attribute service.name.
  ServiceVersion string            Populates resource attribute service.version.
  Timeout        time.Duration     Per-export request timeout. Default: 10s.
  FlushInterval  time.Duration     BatchSpanProcessor schedule delay. Default: 5s.
  MaxBatchSize   int               BatchSpanProcessor max export batch size. Default: 512.
  Privacy        PrivacyConfig     Reused from HawkTracer. PII + credential redaction.
```

**Invariants**:
```
Endpoint != ""                   (validated at construction)
Timeout > 0                      (defaults to 10s if zero)
MaxBatchSize > 0                 (defaults to 512 if zero)
```


### Span Mapping Table

Loom attribute constants (`pkg/observability/instrumentation.go`) → OTel GenAI semantic conventions (OTel semconv v1.28+):

| Loom Constant | Loom Value | OTel Attribute | Notes |
|---|---|---|---|
| `AttrLLMProvider` | `"anthropic"` | `gen_ai.system` | |
| `AttrLLMModel` | `"claude-sonnet-4-5"` | `gen_ai.request.model` | |
| `AttrLLMInputTokens` | `1234` | `gen_ai.usage.input_tokens` | |
| `AttrLLMOutputTokens` | `567` | `gen_ai.usage.output_tokens` | |
| `AttrLLMCacheReadTokens` | `890` | `gen_ai.usage.cache_read_input_tokens` | |
| `AttrLLMTemperature` | `0.7` | `gen_ai.request.temperature` | |
| `AttrLLMMaxTokens` | `4096` | `gen_ai.request.max_tokens` | |
| `AttrLLMStopReason` | `"end_turn"` | `gen_ai.response.finish_reasons` | wrapped as array |
| `AttrToolName` | `"query_db"` | `gen_ai.tool.name` | |
| `AttrSessionID` | `"sess-abc"` | `session.id` | resource attr |
| `AttrAgentID` | `"agent-1"` | `loom.agent.id` | no OTel equiv → prefix |
| `AttrLLMCost` | `0.0023` | `loom.llm.cost` | no OTel equiv → prefix |
| `AttrPatternName` | `"sql-analysis"` | `loom.pattern.name` | no OTel equiv → prefix |
| `AttrErrorMessage` | `"timeout"` | `exception.message` | OTel exception semconv |
| `AttrErrorType` | `"DeadlineExceeded"` | `exception.type` | |
| `ResourceAttrServiceName` | `"looms"` | `service.name` | resource attribute |
| `ResourceAttrServiceVersion` | `"1.3.0"` | `service.version` | resource attribute |

**Span name pass-through**: Loom span names (`llm.completion`, `tool.execute`, `agent.chat`, etc.) are forwarded as-is to OTel. These are already meaningful; backends display them in their trace UIs without transformation.


## Algorithms

### Loom-to-OTel Span Bridge

**Problem**: Loom's `Span` and OTel's `trace.Span` have different lifecycle models. Loom spans are value types (`*Span` struct); OTel spans are interface values managed internally by the SDK. They must be paired for the duration of a Loom span's life.

**Solution**: `sync.Map` pairing by `SpanID`.

```
StartSpan(ctx, name, opts):
  loomSpan = newLoomSpan(ctx, name, opts)          // same as NoOpTracer
  otelCtx, otelSpan = t.tracer.Start(ctx, name,
    trace.WithTimestamp(loomSpan.StartTime),
    trace.WithSpanKind(spanKindFor(name)),
  )
  t.activeSpans.Store(loomSpan.SpanID, otelSpan)
  return ContextWithSpan(otelCtx, loomSpan), loomSpan

EndSpan(span):
  span.EndTime = now()
  span.Duration = span.EndTime - span.StartTime
  redacted = t.redact(span)                        // privacy filter
  raw, ok = t.activeSpans.LoadAndDelete(span.SpanID)
  if !ok: return                                   // span was never started (no-op)
  otelSpan = raw.(trace.Span)
  translateAttrs(otelSpan, redacted.Attributes)
  for _, event := range redacted.Events:
    otelSpan.AddEvent(event.Name, ...)
  if span.Status.Code == StatusError:
    otelSpan.SetStatus(codes.Error, span.Status.Message)
  otelSpan.End(trace.WithTimestamp(span.EndTime))
  t.activeSpans.Delete(span.SpanID)                // cleanup (LoadAndDelete covers this)
```

**Complexity**: O(1) amortised for `sync.Map` operations.

**Race safety**: `sync.Map` is goroutine-safe. Loom's context propagation ensures `EndSpan` is called by the same goroutine that called `StartSpan` (via `defer`), so no concurrent access to the same `SpanID` entry occurs in practice. `sync.Map` handles the edge case where it could.

**TraceID / SpanID format**: OTel uses 128-bit trace IDs and 64-bit span IDs in W3C hex format. Loom uses UUID strings. The bridge converts Loom UUIDs to OTel IDs by stripping hyphens and truncating / zero-padding to the required byte lengths.


### GenAI Attribute Translation

**Problem**: Backends identify LLM spans by the presence of `gen_ai.*` attributes. Without translation, Opik and Grafana would show raw Loom attribute names (`llm.model`, `llm.tokens.input`) instead of recognised GenAI fields.

**Solution**: Static lookup table applied at `EndSpan`.

```
translateAttrs(otelSpan trace.Span, attrs map[string]interface{}):
  for key, value in attrs:
    if otelKey = loomToGenAI[key]; otelKey != "":
      otelSpan.SetAttribute(otelKey, value)
    else:
      otelSpan.SetAttribute("loom." + key, value)   // preserve with namespace
```

`loomToGenAI` is a `map[string]string` constant defined in `otel_attrs.go`. Lookups are O(1). The table is initialised once at package init.

**Invariant**: Every Loom attribute is represented in the exported OTel span — either under its canonical `gen_ai.*` name or under a `loom.*` prefixed fallback. No data is silently dropped.


## Design Trade-offs

### Decision 1: Full OTelTracer vs. OTLPSpanExporter on EmbeddedTracer

**Option A — Full OTelTracer** (recommended):
- Implements `Tracer` interface directly
- Owns the OTel `TracerProvider`
- Configured via `mode: otel` in `looms.yaml`
- Works without `EmbeddedTracer`

**Option B — OTLPSpanExporter**:
- Implements `SpanExporter` interface
- Attached to `EmbeddedTracer` via `WithSpanExporter`
- Enables simultaneous local storage + OTLP export
- Cannot be used standalone (requires `EmbeddedTracer`)

**Chosen: Option A** for the primary implementation, with Option B available as a composition pattern.

**Rationale**:
- Option A is a first-class deployment mode — operators configure a single endpoint and do not need embedded storage
- Option A matches the `HawkTracer` pattern exactly, keeping the codebase consistent
- Option B is still useful for hybrid deployments (local SQLite + remote Opik) and can be built on top of Option A's REST client code without duplication

**Consequences**:
- Two code paths to maintain (OTelTracer + optional OTLPSpanExporter)
- Option A cannot simultaneously write to embedded storage — hybrid mode requires Option B

---

### Decision 2: Translate Attributes at EndSpan vs. at Export

**Option A — Translate at EndSpan** (chosen):
- Attribute translation happens inside `OTelTracer.EndSpan`
- OTel span carries `gen_ai.*` natively
- No custom exporter needed

**Option B — Translate in a custom SpanProcessor**:
- OTel span carries raw Loom attributes
- A `SpanProcessor.OnEnd` transforms to `gen_ai.*` before the exporter sees them

**Chosen: Option A**.

**Rationale**:
- Simpler — no custom `SpanProcessor` to implement or test
- Translation is co-located with the bridge logic in `otel.go`
- `gen_ai.*` attributes are what backends want; there is no value in carrying both

**Consequences**:
- Translated attributes are immutable after `EndSpan` (the OTel span has already ended)
- Cannot inspect raw Loom attributes after export; they are preserved only in redacted form within `loom.*` prefixed attributes

---

### Decision 3: HTTP-only (no gRPC OTLP)

**Chosen: HTTP only** (`otlptracehttp`).

**Rationale**:
- Opik's OTLP endpoint is HTTP-only (no gRPC port documented)
- `otlptracehttp` is already in `go.mod`; `otlptracegrpc` is not — avoiding a new dependency
- All major backends accept OTLP HTTP; gRPC adds latency for span sizes typical in Loom

**Consequences**:
- gRPC-only backends (rare) are not supported
- Binary size remains smaller (one exporter package vs. two)
- Can add `otlptracegrpc` later without breaking the interface


## Constraints and Limitations

### Constraint 1: HTTP-Only OTLP Transport

**Description**: OTel gRPC transport is not implemented.

**Rationale**: Opik, Jaeger (HTTP mode), Grafana Tempo, and Honeycomb all accept OTLP HTTP. gRPC requires an additional dependency not currently in `go.mod`.

**Impact**: Backends that accept only gRPC OTLP will not receive traces.

**Workaround**: Deploy an OTel Collector in front of the gRPC-only backend; configure `otlp_endpoint` to point at the Collector's HTTP receiver.

---

### Constraint 2: No OTel Metrics Export

**Description**: `RecordMetric` data is not exported to the OTLP backend. Loom metrics (token counts, call rates, latency histograms) remain internal to the process.

**Rationale**: OTel metrics export requires `go.opentelemetry.io/otel/sdk/metric`, which is not in `go.mod`. Adding it is a non-trivial dependency bump.

**Impact**: Backend dashboards cannot display aggregate LLM cost or throughput metrics from Loom.

**Workaround**: Backends can derive metrics from spans (token counts appear on `llm.completion` spans). Dedicated metrics export is a planned follow-up.

---

### Constraint 3: Single-Process Trace Scope

**Description**: Traces do not cross process boundaries. If Loom makes an outbound HTTP call to another Loom instance, trace context is not propagated via W3C `traceparent` headers.

**Rationale**: Loom is a single-process server. Multi-process trace propagation requires injecting/extracting OTel context from outbound HTTP requests, which is not part of the current scope.

**Impact**: Multi-agent workflows where agents run in separate processes will show disconnected traces in the backend.

**Workaround**: Manual TraceID correlation using `ContextWithTraceID`.

---

### Constraint 4: Opik Rate Limit

**Description**: Opik Cloud imposes a 10,000 events/minute ingestion limit. A busy Loom instance emitting many spans per second may breach this.

**Mitigation**: OTel's `BatchSpanProcessor` (default max batch 512 spans, 5s interval) naturally smooths burst traffic. The `MaxBatchSize` and `FlushInterval` fields in `OTelConfig` allow tuning for high-throughput deployments.


## Performance Characteristics

### Additional Latency per Span

| Operation | Added Latency (vs. NoOpTracer) | Notes |
|---|---|---|
| `StartSpan` | +2–5µs | OTel SDK span start + `sync.Map` store |
| `EndSpan` (no redaction) | +5–15µs | Attribute translation + OTel span end + BatchProc.OnEnd |
| `EndSpan` (with PII redaction) | +15–60µs | Regex matching added on top |
| Background batch export | 0 (async) | BatchSpanProcessor goroutine, does not block caller |

These are additive to the existing NoOpTracer baseline (~1µs per StartSpan / EndSpan).

### Memory Usage

| Component | Size |
|---|---|
| `sync.Map` entry (per open span) | ~200 bytes |
| OTel SDK BatchSpanProcessor buffer | 512 spans × ~1KB ≈ 512KB |
| `OTelConfig` + TracerProvider overhead | ~50KB |
| **Total steady-state** | **~600KB** |

Peak memory occurs when the batch is full (512 spans buffered). For typical Loom workloads (< 50 concurrent spans), steady-state memory is well under 100KB.

### Export Throughput

BatchSpanProcessor defaults:
- Max export batch size: 512 spans
- Schedule delay: 5s
- Sustained throughput: 512 / 5s = ~100 spans/s

Opik rate limit: 10,000 events/min ≈ 167 events/s. At 100 spans/s, Loom stays within this limit with ~40% headroom.


## Concurrency Model

**OTelTracer itself is goroutine-safe** by design:

- `sync.Map`: Used for `activeSpans` — concurrent read/write/delete without external locking
- OTel SDK `TracerProvider` and `BatchSpanProcessor`: Goroutine-safe by OTel SDK contract
- `otlptracehttp.Exporter`: Goroutine-safe; uses connection pooling internally
- Privacy redaction (`redact()`): Stateless function, no shared state

**Background export goroutine**: Owned entirely by the OTel SDK's `BatchSpanProcessor`. Loom does not need to manage it. `TracerProvider.Shutdown()` signals the goroutine to drain and exit.

**Shutdown sequence**:
```
OTelTracer.Shutdown(ctx):
  1. provider.ForceFlush(ctx)    ← drain buffered spans
  2. provider.Shutdown(ctx)      ← stop background goroutine, close exporter
  3. Return error if context expires
```

This is called from Loom server's graceful shutdown path. The existing `cmd_serve.go` shutdown chain will invoke `tracer.Flush(ctx)`, which forwards to `provider.ForceFlush`.


## Error Handling

### Strategy

The `OTelTracer` follows the same best-effort strategy as `HawkTracer`:
- Export failures are **logged but not propagated** to the calling agent
- A failed export does not affect agent execution
- The OTel SDK internally retries transient HTTP failures (5xx, network errors) via `otlptracehttp`'s built-in retry policy

### Error Categories

| Error | Handling | Recovery |
|---|---|---|
| OTLP endpoint unreachable at startup | Log warning, return NoOpTracer | Fix endpoint config and restart |
| Transient export failure (5xx) | OTel SDK retries (3 attempts, exponential backoff) | Automatic |
| 401 Unauthorized | OTel SDK logs error, drops batch | Fix `otlp_headers` auth token |
| `sync.Map` miss on EndSpan | No-op, log at debug level | Not an error — span may have been created before OTelTracer replaced NoOpTracer |
| TracerProvider.Shutdown timeout | Log warning, return context error | Increase shutdown timeout |


## Security Considerations

### PII Redaction

The same `PrivacyConfig` and `redact()` function used by `HawkTracer` is applied in `OTelTracer.EndSpan` before any attribute is passed to the OTel SDK. Sensitive data never reaches the OTLP backend.

**Scope**: The `redact()` function is defined in `pkg/observability/hawk.go` today. For `OTelTracer`, it will be extracted to a shared `pkg/observability/privacy.go` to avoid importing the Hawk build tag from OTel code.

### Transport Security

- HTTPS is strongly recommended for the `otlp_endpoint` in any non-local deployment
- `OTelConfig.Insecure = true` disables TLS verification and must only be used for local development
- API keys are passed in HTTP headers (not query parameters), consistent with industry practice
- `looms.yaml` field `otlp_headers` is marked as sensitive in the config loader (same treatment as `hawk_api_key`)

### Header Exposure

`otlp_headers` may contain bearer tokens. The config validator will warn if `otlp_headers` is specified without HTTPS and `insecure = false`.


## Backend Compatibility

Any backend that accepts OTLP HTTP `POST /v1/traces` is compatible. The following have been analysed:

| Backend | OTLP HTTP | GenAI semconv | Notes |
|---|---|---|---|
| **Opik** (cloud) | ✅ | ✅ | `https://www.comet.com/opik/api/v1/private/otel/v1/traces`; requires `Authorization` header |
| **Opik** (self-hosted) | ✅ | ✅ | `http://<host>/api/v1/private/otel/v1/traces` |
| **Jaeger** (≥ v1.35) | ✅ | ⚠️ partial | Port 4318; GenAI attrs displayed as raw tags |
| **Grafana Tempo** | ✅ | ⚠️ partial | Port 4318; GenAI attrs available via TraceQL |
| **Honeycomb** | ✅ | ✅ | Renders `gen_ai.*` natively in LLM query panel |
| **Datadog** | ✅ | ✅ | `https://trace.agent.datadoghq.com`; maps to LLM Observability product |
| **New Relic** | ✅ | ⚠️ partial | OTLP endpoint at `otlp.nr-data.net:4318` |
| **OTel Collector** | ✅ | N/A | Use as proxy/fan-out to multiple backends |

The config change to switch backends:

```yaml
# looms.yaml
observability:
  enabled: true
  mode: otel
  otlp_endpoint: http://localhost:5173/api/v1/private/otel/v1/traces  # Opik local
  otlp_headers:
    Authorization: "Bearer <opik-api-key>"

# Switch to Jaeger — no code change needed:
  otlp_endpoint: http://jaeger:4318/v1/traces
  otlp_headers: {}
```


## Related Work

### OpenTelemetry

The OTel specification (CNCF, 2019) defines the Traces API, SDK, and OTLP. Loom's `Tracer` interface predates OTel adoption and was designed independently with a similar but not identical API. The bridge approach (wrapping Loom spans inside OTel spans) is the standard integration pattern for non-OTel frameworks.

- **OTel Go SDK**: `go.opentelemetry.io/otel/sdk/trace` — `TracerProvider`, `BatchSpanProcessor`
- **OTel GenAI Semantic Conventions**: Draft specification defining `gen_ai.*` attribute names for LLM tracing (OTel semconv v1.28+)

### Opik

Opik (Comet ML, 2024) is an LLM observability platform that supports both a native Python SDK and OTLP ingestion. Its OTLP support uses the standard `gen_ai.*` semantic conventions, making it compatible with the translation layer described here without Opik-specific code.

### HawkTracer (existing)

The `HawkTracer` (`pkg/observability/hawk.go`) is the reference implementation for Loom's export pattern: batched async HTTP export with retry and privacy redaction. `OTelTracer` follows the same structural pattern but delegates batching and retry to the OTel SDK's `BatchSpanProcessor` rather than implementing them directly.


## References

1. OpenTelemetry. (2024). *OpenTelemetry Protocol (OTLP) Specification*. https://opentelemetry.io/docs/specs/otlp/

2. OpenTelemetry. (2024). *Semantic Conventions for GenAI systems*. https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/

3. OpenTelemetry Go SDK. (2024). `go.opentelemetry.io/otel` v1.43.0. https://pkg.go.dev/go.opentelemetry.io/otel

4. Comet ML. (2024). *Opik OpenTelemetry Integration*. https://www.comet.com/docs/opik/integrations/opentelemetry

5. W3C. (2021). *Trace Context Level 1*. https://www.w3.org/TR/trace-context/


## Further Reading

### Architecture

- [Observability Architecture](observability.md) — Current tracer design, HawkTracer, EmbeddedTracer
- [Agent System Design](agent-system-design.md) — Where spans are emitted in the conversation loop
- [Loom System Architecture](loom-system-architecture.md) — Overall system context

### Reference

- `pkg/observability/interface.go` — `Tracer` and `SpanExporter` interfaces
- `pkg/observability/instrumentation.go` — All span name and attribute constants
- `pkg/observability/hawk.go` — Reference implementation for export + privacy redaction pattern
- `cmd/looms/config.go` — `ObservabilityConfig` struct (where `OTLPEndpoint` fields will be added)
