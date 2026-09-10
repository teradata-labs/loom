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
package main

import (
	"os"

	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// applyOTLPEnvOverride inspects standard and Loom OTLP endpoint environment
// variables when OTLP was selected in configuration, then overrides the
// endpoint/headers/insecure fields from environment values.
//
// A generic OTEL_EXPORTER_OTLP_* variable must not enable observability or
// change a configured Hawk/embedded mode: those variables are often injected
// by cluster-wide instrumentation. Operators opt in with mode: otel or an
// explicit observability.otlp_endpoint.
//
// Returns the effective OTLP endpoint string (empty when the env var is unset).
func applyOTLPEnvOverride(obs *ObservabilityConfig, logger *zap.Logger) string {
	if obs.Mode != "otel" && obs.OTLPEndpoint == "" {
		return ""
	}

	otlpEnv := observability.ResolveOTLPEndpointEnv()
	if otlpEnv == "" {
		return ""
	}

	// Env var always wins over config-file values so the platform can relocate
	// the collector without rebuilding the agent artifact.
	obs.OTLPEndpoint = otlpEnv

	if raw := firstConfiguredEnv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS", "LOOM_OTLP_HEADERS"); raw != "" {
		obs.OTLPHeaders = observability.ParseHeadersEnv(raw)
	}

	if os.Getenv("LOOM_OTLP_INSECURE") == "true" {
		obs.OTLPInsecure = true
	}

	return otlpEnv
}

func firstConfiguredEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func otlpServiceName(defaultName string) string {
	if serviceName := os.Getenv("OTEL_SERVICE_NAME"); serviceName != "" {
		return serviceName
	}
	return defaultName
}
