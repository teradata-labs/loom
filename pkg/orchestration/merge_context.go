// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// mergeContext is a minimal implementation of agent.Context for merge operations.
// It wraps a standard context.Context and provides the required Session() and Tracer() methods
// to satisfy the agent.Context interface for LLM-based merge operations.
type mergeContext struct {
	context.Context
	session *agent.Session
	tracer  observability.Tracer
}

func (c *mergeContext) Session() *agent.Session {
	return c.session
}

func (c *mergeContext) Tracer() observability.Tracer {
	return c.tracer
}

func (c *mergeContext) ProgressCallback() agent.ProgressCallback {
	return nil
}
