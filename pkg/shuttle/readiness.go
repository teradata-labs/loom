// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import "context"

// ReadinessState says whether a registered tool can do what it advertises.
// Registration only proves the tool is installed; a tool that depends on
// credentials or external configuration may still be unable to run.
type ReadinessState string

const (
	// ReadinessReady means the tool can perform its advertised operation.
	ReadinessReady ReadinessState = "ready"
	// ReadinessDegraded means the tool runs, but with reduced capability
	// (for example, only a limited fallback provider is configured).
	ReadinessDegraded ReadinessState = "degraded"
	// ReadinessUnavailable means the tool cannot perform its operation.
	ReadinessUnavailable ReadinessState = "unavailable"
)

// ToolReadiness describes a tool's current readiness.
// Summary and Remediation are shown to the LLM and written to logs, so they
// must never contain secrets.
type ToolReadiness struct {
	State ReadinessState
	// Summary is a short, LLM-facing statement of what is and is not usable.
	Summary string
	// Remediation tells an operator how to reach ReadinessReady.
	// Empty when the tool is ready.
	Remediation string
}

// ReadinessReporter is implemented by tools whose usability depends on
// runtime configuration. Readiness is evaluated on each call so it reflects
// configuration applied after the tool was constructed.
type ReadinessReporter interface {
	Readiness(ctx context.Context) ToolReadiness
}

// ReadinessOf returns the tool's readiness, and false if the tool does not
// report readiness (such tools are assumed ready).
func ReadinessOf(ctx context.Context, tool Tool) (ToolReadiness, bool) {
	reporter, ok := tool.(ReadinessReporter)
	if !ok {
		return ToolReadiness{}, false
	}
	return reporter.Readiness(ctx), true
}
