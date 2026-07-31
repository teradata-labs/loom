// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   string
		wantPass   bool
		wantReason string
		wantOK     bool
	}{
		{name: "plain PASS", response: "PASS", wantPass: true, wantOK: true},
		{name: "PASS with trailing newline and explanation", response: "PASS\nAll criteria satisfied.", wantPass: true, wantOK: true},
		{name: "lowercase pass", response: "pass", wantPass: true, wantOK: true},
		{name: "PASS with surrounding whitespace", response: "   PASS   \n", wantPass: true, wantOK: true},
		{name: "leading blank lines before verdict", response: "\n\nPASS", wantPass: true, wantOK: true},
		{name: "FAIL with reason", response: "FAIL: missing table name", wantPass: false, wantReason: "missing table name", wantOK: true},
		{name: "bare FAIL", response: "FAIL", wantPass: false, wantReason: "", wantOK: true},
		{name: "lowercase fail with reason", response: "fail: nope", wantPass: false, wantReason: "nope", wantOK: true},
		{name: "FAIL reason on multiple lines takes first line", response: "FAIL: bad format\nmore detail here", wantPass: false, wantReason: "bad format", wantOK: true},
		{name: "empty response is malformed", response: "", wantOK: false},
		{name: "whitespace-only response is malformed", response: "  \n \t ", wantOK: false},
		{name: "prose verdict is malformed", response: "The output looks good to me!", wantOK: false},
		{name: "verdict buried mid-line is malformed", response: "I would say PASS overall", wantOK: false},
		{name: "PASSING is not PASS", response: "PASSING", wantOK: false},
		{name: "FAILURE is not FAIL", response: "FAILURE: x", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pass, reason, ok := ParseVerdict(tt.response)
			assert.Equal(t, tt.wantOK, ok, "ok")
			if !tt.wantOK {
				return
			}
			assert.Equal(t, tt.wantPass, pass, "pass")
			assert.Equal(t, tt.wantReason, reason, "reason")
		})
	}
}
