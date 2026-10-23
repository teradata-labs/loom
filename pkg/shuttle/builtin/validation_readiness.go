// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/validation"
)

// Agent-config validation warns about listed tools that are not ready (for
// example web_search with no provider API key). validation cannot import this
// package, so the readiness lookup is installed here.
func init() {
	validation.SetToolReadinessFunc(builtinReadiness)
}

// builtinReadiness reports the readiness of a builtin tool by name.
func builtinReadiness(name string) (state, summary, remediation string, ok bool) {
	tool := ByName(name)
	if tool == nil {
		return "", "", "", false
	}
	r, reports := shuttle.ReadinessOf(context.Background(), tool)
	if !reports {
		return "", "", "", false
	}
	return string(r.State), r.Summary, r.Remediation, true
}
