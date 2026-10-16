package agent

import (
	"context"
	"testing"
)

// A context that dropped its progress callback reports none, however many
// layers were added on top of the one that carried it.
func TestContextWithoutProgressCallbackDropsIt(t *testing.T) {
	var called bool
	ctx := ContextWithProgressCallback(context.Background(), func(ProgressEvent) { called = true })
	if ProgressCallbackFromContext(ctx) == nil {
		t.Fatal("the callback did not ride on the context")
	}
	ctx = context.WithValue(ContextWithoutProgressCallback(ctx), "k", "v")
	if cb := ProgressCallbackFromContext(ctx); cb != nil {
		cb(ProgressEvent{})
		t.Fatalf("the callback survived the drop (called=%v)", called)
	}
}
