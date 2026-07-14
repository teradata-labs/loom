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
	// Env: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or LOOM_OTLP_ENDPOINT
	// Example (Opik local):  http://localhost:5173/api/v1/private/otel/v1/traces
	// Example (Jaeger):      http://jaeger:4318/v1/traces
	Endpoint string

	// Headers are sent with every OTLP HTTP request (e.g. Authorization: Bearer <key>).
	// Env: OTEL_EXPORTER_OTLP_TRACES_HEADERS (format: "key=val,key2=val2")
	Headers map[string]string

	// Insecure disables TLS certificate verification. Use for local dev only.
	// Env: LOOM_OTLP_INSECURE
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

// resolveOTelConfig fills zero-value fields from environment variables.
func resolveOTelConfig(cfg OTelConfig) OTelConfig {
	if cfg.Endpoint == "" {
		cfg.Endpoint = firstEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "LOOM_OTLP_ENDPOINT")
	}
	if len(cfg.Headers) == 0 {
		if raw := firstEnv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "LOOM_OTLP_HEADERS"); raw != "" {
			cfg.Headers = parseHeadersEnv(raw)
		}
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = firstEnv("OTEL_SERVICE_NAME", "")
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

// parseHeadersEnv parses "key=val,key2=val2" into a map.
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
