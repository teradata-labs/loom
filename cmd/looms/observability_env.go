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

// applyOTLPEnvOverride inspects the OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
// environment variable and, when set, force-enables observability and
// overrides the endpoint/headers/insecure fields from env vars.
//
// This lets the platform (AgentOpsCore) enable and redirect traces at
// deploy-time by injecting a single env var, without requiring the operator
// to patch the agent's looms.yaml config artifact.
//
// Returns the effective OTLP endpoint string (empty when the env var is unset).
func applyOTLPEnvOverride(obs *ObservabilityConfig, logger *zap.Logger) string {
	otlpEnv := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if otlpEnv == "" {
		return ""
	}

	if !obs.Enabled {
		logger.Info("Enabling observability (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set)")
		obs.Enabled = true
	}

	// Env var always wins over config-file values so the platform can relocate
	// the collector without rebuilding the agent artifact.
	obs.OTLPEndpoint = otlpEnv

	if raw := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"); raw != "" {
		obs.OTLPHeaders = observability.ParseHeadersEnv(raw)
	}

	if os.Getenv("LOOM_OTLP_INSECURE") == "true" {
		obs.OTLPInsecure = true
	}

	return otlpEnv
}

// logOTLPModeOverride emits an Info log when the observability mode is being
// changed to "otel" because of the OTLP env-var injection.
func logOTLPModeOverride(logger *zap.Logger, originalMode, otlpEndpoint string) {
	logger.Info("Overriding observability mode to otel (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set)",
		zap.String("original_mode", originalMode),
		zap.String("otlp_endpoint", otlpEndpoint))
}

// expandOTLPConfig expands ${VAR} placeholders in the OTLP endpoint and headers
// so the platform can write placeholders in looms.yaml and supply real values
// via pod env vars (same pattern as MCP auth headers).
//
// Note: a literal '$' in a value must be written as '$$' to avoid expansion.
func expandOTLPConfig(obs *ObservabilityConfig) {
	obs.OTLPEndpoint = os.ExpandEnv(obs.OTLPEndpoint)
	for k, v := range obs.OTLPHeaders {
		obs.OTLPHeaders[k] = os.ExpandEnv(v)
	}
}
