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
