// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build !hawk

package observability

import (
	"net/http"
	"time"

	"go.uber.org/zap"
)

// HawkConfig configures the Hawk tracer.
// This type is always available, but the actual HawkTracer implementation
// requires building with -tags hawk.
type HawkConfig struct {
	// Endpoint is the Hawk API endpoint for trace export.
	// Example: "http://localhost:8080/v1/traces"
	Endpoint string

	// APIKey for authentication with Hawk.
	APIKey string

	// BatchSize is the number of spans to buffer before flushing.
	// Default: 100
	BatchSize int

	// FlushInterval is how often to flush buffered spans.
	// Default: 10s
	FlushInterval time.Duration

	// MaxRetries is the maximum number of retry attempts for failed exports.
	// Default: 3
	MaxRetries int

	// RetryBackoff is the initial backoff duration for retries.
	// Default: 1s (doubles with each retry: 1s, 2s, 4s, 8s...)
	RetryBackoff time.Duration

	// Privacy controls PII redaction.
	Privacy PrivacyConfig

	// HTTPClient for custom transport (e.g., timeouts, proxies).
	// If nil, uses http.DefaultClient.
	HTTPClient *http.Client
}

// PrivacyConfig controls what data is redacted before export.
type PrivacyConfig struct {
	// RedactCredentials removes password, api_key, token fields from spans.
	RedactCredentials bool

	// RedactPII removes email, phone, SSN patterns from attribute values.
	RedactPII bool

	// AllowedAttributes is a whitelist of attribute keys that bypass redaction.
	// Example: []string{"session.id", "llm.model", "tool.name"}
	AllowedAttributes []string
}

// HawkJudgeExporterConfig configures the Hawk judge verdict exporter.
// This type is always available, but the actual HawkJudgeExporter implementation
// requires building with -tags hawk.
type HawkJudgeExporterConfig struct {
	// Endpoint is the Hawk API endpoint (default: HAWK_ENDPOINT env or http://localhost:8080)
	Endpoint string

	// APIKey for authentication (default: HAWK_API_KEY env)
	APIKey string

	// BatchSize is the number of verdicts to batch before sending (default: 10)
	BatchSize int

	// FlushInterval is the max time between flushes (default: 5s)
	FlushInterval time.Duration

	// BufferSize is the size of the verdict buffer channel (default: 100)
	BufferSize int

	// Timeout for HTTP requests (default: 10s)
	Timeout time.Duration

	// Logger for diagnostic output (default: nop logger)
	Logger *zap.Logger

	// Tracer for observability (default: nop tracer)
	Tracer Tracer
}

// HawkJudgeExporter is defined here for type checking, but the actual implementation
// is in hawk_judge_exporter.go (requires -tags hawk).
// When built without -tags hawk, stub methods are provided in hawk_judge_exporter_stub.go.
type HawkJudgeExporter struct {
	// Stub type - no fields needed for type checking
}
