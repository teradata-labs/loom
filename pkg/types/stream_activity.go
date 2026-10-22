// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package types

import "context"

type streamActivityKey struct{}

// WithStreamActivity returns a context carrying fn, which streaming providers
// invoke (via NotifyStreamActivity) for every wire delta that does NOT reach
// the TokenCallback — tool-input JSON fragments today. TokenCallback is text
// only by contract (its tokens become the user-visible partial response), so
// without this hook a long tool-argument generation is indistinguishable
// from a stalled stream to anything watching for activity.
//
// fn must be cheap and non-blocking, like TokenCallback: it runs inline on
// the goroutine reading the stream.
func WithStreamActivity(ctx context.Context, fn func()) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, streamActivityKey{}, fn)
}

// NotifyStreamActivity invokes the hook registered with WithStreamActivity, if
// any, and reports whether one was present. Providers call it once per
// tool-input delta; a context without a hook makes it a no-op.
func NotifyStreamActivity(ctx context.Context) bool {
	fn, ok := ctx.Value(streamActivityKey{}).(func())
	if !ok || fn == nil {
		return false
	}
	fn()
	return true
}

// ToolInputProgress describes a tool call whose arguments are still streaming.
// Providers report it alongside NotifyStreamActivity so consumers can show the
// call (by name) before its arguments are complete.
type ToolInputProgress struct {
	// Index is the call's position within the response, stable for the stream.
	Index int
	// ToolCallID and ToolName stay empty until the provider has sent them;
	// some proxies send arguments first.
	ToolCallID string
	ToolName   string
	// Bytes is the length of the argument JSON received so far for this call.
	Bytes int
}

type toolInputProgressKey struct{}

// WithToolInputProgress returns a context carrying fn, which streaming
// providers invoke (via NotifyToolInputProgress) for every tool-input delta.
// Like the stream-activity hook it runs inline on the goroutine reading the
// stream, so fn must be cheap and non-blocking.
func WithToolInputProgress(ctx context.Context, fn func(ToolInputProgress)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, toolInputProgressKey{}, fn)
}

// NotifyToolInputProgress invokes the hook registered with
// WithToolInputProgress, if any, and reports whether one was present.
func NotifyToolInputProgress(ctx context.Context, p ToolInputProgress) bool {
	fn, ok := ctx.Value(toolInputProgressKey{}).(func(ToolInputProgress))
	if !ok || fn == nil {
		return false
	}
	fn(p)
	return true
}
