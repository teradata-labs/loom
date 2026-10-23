// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import "sync/atomic"

// ToolReadinessFunc reports whether a named tool can do its job in the
// current process: its state ("ready", "degraded" or "unavailable"), a short
// summary and an operator remediation. ok is false for tools it does not know
// or that do not report readiness.
//
// It is a hook because this package cannot import pkg/shuttle/builtin (that
// package imports this one); builtin registers it from its init.
type ToolReadinessFunc func(name string) (state, summary, remediation string, ok bool)

var toolReadiness atomic.Pointer[ToolReadinessFunc] //nolint:gochecknoglobals

// SetToolReadinessFunc installs the readiness hook used by agent validation.
// nil removes it.
func SetToolReadinessFunc(f ToolReadinessFunc) {
	if f == nil {
		toolReadiness.Store(nil)
		return
	}
	toolReadiness.Store(&f)
}

// readinessWarning returns a warning for a listed tool that is not ready, or
// nil when it is ready, unknown, or no hook is installed.
func readinessWarning(toolName string) *ValidationWarning {
	f := toolReadiness.Load()
	if f == nil {
		return nil
	}
	state, summary, remediation, ok := (*f)(toolName)
	if !ok || state == "" || state == "ready" {
		return nil
	}
	return &ValidationWarning{
		Field:   "spec.tools",
		Message: "Tool '" + toolName + "' is " + state + " in this environment: " + summary,
		Fix:     remediation,
	}
}
