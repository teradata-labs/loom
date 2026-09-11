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
package observability

import (
	"os"
	"strings"
	"time"

	loomconfig "github.com/teradata-labs/loom/pkg/config"
)

// SpanFilterConfig controls which spans are forwarded to the OTLP backend.
// Useful for suppressing infrastructure/startup spans when using backends like
// Opik that are focused on LLM observability.
type SpanFilterConfig struct {
	// IncludePrefixes whitelists spans whose names start with any of these strings.
	// Empty slice means all spans are exported (no filtering).
	// Example: []string{"llm.", "agent.", "tool.", "backend.", "mcp."}
	IncludePrefixes []string
}

// OTelConfig holds configuration for the OTelTracer.
// Fields are resolved from explicit values first, then standard OTel env vars,
// then Loom-specific fallback env vars.
type OTelConfig struct {
	// Endpoint is the full OTLP HTTP URL including path.
	// Resolved from env in priority order:
	//   OTEL_EXPORTER_OTLP_TRACES_ENDPOINT — signal-specific, used verbatim.
	//   OTEL_EXPORTER_OTLP_ENDPOINT        — OTel-spec base URL; /v1/traces is appended.
	//   LOOM_OTLP_ENDPOINT                 — Loom-specific fallback, used verbatim.
	// Values may contain ${VAR} placeholders. Bare dollar signs are preserved;
	// write $$ to emit one literal dollar sign.
	// Example (Opik local):  http://localhost:5173/api/v1/private/otel/v1/traces
	// Example (Jaeger):      http://jaeger:4318/v1/traces
	Endpoint string

	// Headers are sent with every OTLP HTTP request (e.g. Authorization: Bearer <key>).
	// Resolved from env in priority order:
	//   OTEL_EXPORTER_OTLP_TRACES_HEADERS — signal-specific (format: "key=val,key2=val2").
	//   OTEL_EXPORTER_OTLP_HEADERS        — OTel-spec base headers var, same format.
	//   LOOM_OTLP_HEADERS                 — Loom-specific fallback.
	// Values may contain ${VAR} placeholders; use '$$' for a literal '$'.
	Headers map[string]string

	// Insecure selects plaintext HTTP transport. Use for local dev only.
	// Env: LOOM_OTLP_INSECURE=true
	Insecure bool

	// ServiceName populates the resource attribute service.name.
	// Env: OTEL_SERVICE_NAME
	ServiceName string

	// ServiceVersion populates the resource attribute service.version.
	// Env: OTEL_SERVICE_VERSION
	ServiceVersion string

	// Timeout is the per-export request timeout. Default: 10s.
	Timeout time.Duration

	// FlushInterval is the BatchSpanProcessor schedule delay. Default: 5s.
	FlushInterval time.Duration

	// MaxBatchSize is the maximum spans per OTLP export request. Default: 512.
	MaxBatchSize int

	// Privacy controls PII and credential redaction before export.
	Privacy PrivacyConfig

	// SpanFilter limits which spans are exported. Empty IncludePrefixes = export all.
	SpanFilter SpanFilterConfig
}

// resolveOTLPEndpointEnv returns the OTLP traces endpoint from environment
// variables, applying OTel-spec semantics:
//   - OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: signal-specific; used verbatim.
//   - OTEL_EXPORTER_OTLP_ENDPOINT: base URL per spec; /v1/traces is appended.
//   - LOOM_OTLP_ENDPOINT: Loom-specific fallback; used verbatim.
func ResolveOTLPEndpointEnv() string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); v != "" {
		return v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		return strings.TrimRight(v, "/") + "/v1/traces"
	}
	return os.Getenv("LOOM_OTLP_ENDPOINT")
}

// resolveOTelConfig fills zero-value fields from environment variables.
func resolveOTelConfig(cfg OTelConfig) OTelConfig {
	if cfg.Endpoint == "" {
		cfg.Endpoint = ResolveOTLPEndpointEnv()
	}
	cfg.Endpoint = loomconfig.ExpandEnvPlaceholders(cfg.Endpoint)
	if len(cfg.Headers) == 0 {
		if raw := firstEnv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS", "LOOM_OTLP_HEADERS"); raw != "" {
			cfg.Headers = parseHeadersEnv(raw)
		}
	}
	if len(cfg.Headers) > 0 {
		headers := make(map[string]string, len(cfg.Headers))
		for key, value := range cfg.Headers {
			headers[key] = loomconfig.ExpandEnvPlaceholders(value)
		}
		cfg.Headers = headers
	}
	if !cfg.Insecure {
		cfg.Insecure = os.Getenv("LOOM_OTLP_INSECURE") == "true"
	}
	if cfg.ServiceName == "" {
		if name := os.Getenv("OTEL_SERVICE_NAME"); name != "" {
			cfg.ServiceName = name
		} else {
			cfg.ServiceName = "loom" // safe default: non-empty service.name in every exported span
		}
	}
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = os.Getenv("OTEL_SERVICE_VERSION")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = 5 * time.Second
	}
	if cfg.MaxBatchSize == 0 {
		cfg.MaxBatchSize = 512
	}
	return cfg
}

// ParseHeadersEnv parses a comma-separated "key=value" string into a map.
// This is the format used by OTEL_EXPORTER_OTLP_TRACES_HEADERS.
func ParseHeadersEnv(raw string) map[string]string {
	return parseHeadersEnv(raw)
}

func parseHeadersEnv(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if idx := strings.IndexByte(pair, '='); idx > 0 {
			out[pair[:idx]] = pair[idx+1:]
		}
	}
	return out
}

// firstEnv returns the first non-empty value among the given env var names.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if k != "" {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
	}
	return ""
}
