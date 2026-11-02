// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A tool state that fails parks with what the command said, not only with its
// exit code: the person who resumes it reads the reason from the park.
func TestAFailedToolStateParksWithWhatTheCommandSaid(t *testing.T) {
	r := &shuttle.Result{
		Success: false,
		Error:   &shuttle.Error{Code: "EXIT_ERROR", Message: "Command exited with code 2"},
		Data: map[string]interface{}{
			"stdout":    "go work init …",
			"stderr":    "make: *** [loom.publish] Error 1\ngo: cannot find main module",
			"exit_code": 2,
		},
	}
	got := toolError(r)
	for _, want := range []string{"exited with code 2", "cannot find main module"} {
		if !strings.Contains(got, want) {
			t.Errorf("the park should carry %q, got:\n%s", want, got)
		}
	}
	if got := toolError(&shuttle.Result{Success: false, Error: &shuttle.Error{Message: "denied"}}); got != "denied" {
		t.Errorf("a result with no output keeps the plain message, got %q", got)
	}
	long := strings.Repeat("x", 2000) + "the end"
	if got := toolError(&shuttle.Result{Error: &shuttle.Error{Message: "m"}, Data: map[string]interface{}{"stderr": long}}); !strings.HasSuffix(got, "the end") || len(got) > 1300 {
		t.Errorf("a long stderr is kept from its end and bounded, got %d bytes", len(got))
	}
}
