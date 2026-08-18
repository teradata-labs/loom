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

// applyOTLPEnvOverride inspects the standard and Loom OTLP endpoint
// environment variables and, when one is set, force-enables observability and
// overrides the endpoint/headers/insecure fields from env vars.
//
// This lets the platform (AgentOpsCore) enable and redirect traces at
// deploy-time by injecting a single env var, without requiring the operator
// to patch the agent's looms.yaml config artifact.
//
// Returns the effective OTLP endpoint string (empty when the env var is unset).
func applyOTLPEnvOverride(obs *ObservabilityConfig, logger *zap.Logger) string {
	otlpEnv := observability.ResolveOTLPEndpointEnv()
	if otlpEnv == "" {
		return ""
	}

	if !obs.Enabled {
		logger.Info("Enabling observability (OTLP endpoint environment variable is set)")
		obs.Enabled = true
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

// logOTLPModeOverride emits an Info log when the observability mode is being
// changed to "otel" because of the OTLP env-var injection.
func logOTLPModeOverride(logger *zap.Logger, originalMode, otlpEndpoint string) {
	logger.Info("Overriding observability mode to otel (OTLP endpoint environment variable is set)",
		zap.String("original_mode", originalMode),
		zap.String("otlp_endpoint", otlpEndpoint))
}

func firstConfiguredEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
