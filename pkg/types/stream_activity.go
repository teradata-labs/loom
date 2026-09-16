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
