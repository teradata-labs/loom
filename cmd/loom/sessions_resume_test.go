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

package main

import (
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestParseGapOutcome(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
		result  string
		want    loomv1.ExecutionGapOutcome
		wantErr bool
	}{
		{name: "succeeded with JSON", outcome: "succeeded", result: `{"rows":3}`, want: loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_SUCCEEDED},
		{name: "succeeded without result", outcome: "succeeded", wantErr: true},
		{name: "succeeded with invalid JSON", outcome: "succeeded", result: "{rows", wantErr: true},
		{name: "failed", outcome: "failed", want: loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_FAILED},
		{name: "unknown outcome", outcome: "maybe", wantErr: true},
		{name: "missing outcome", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGapOutcome(tt.outcome, tt.result)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGapOutcome(%q, %q) = %v, want an error", tt.outcome, tt.result, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("parseGapOutcome(%q, %q) = %v, %v; want %v", tt.outcome, tt.result, got, err, tt.want)
			}
		})
	}
}

func TestFormatResumeResponse(t *testing.T) {
	tests := []struct {
		name     string
		resp     *loomv1.ResumeSessionResponse
		wantOK   bool
		contains []string
	}{
		{
			name:     "resumed",
			resp:     &loomv1.ResumeSessionResponse{Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_RESUMED, Content: "the reply"},
			wantOK:   true,
			contains: []string{"Resumed session s1", "the reply"},
		},
		{
			name:     "nothing to resume",
			resp:     &loomv1.ResumeSessionResponse{Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_NOTHING_TO_RESUME},
			wantOK:   true,
			contains: []string{"Nothing to resume"},
		},
		{
			name:     "parked",
			resp:     &loomv1.ResumeSessionResponse{Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_PARKED},
			wantOK:   true,
			contains: []string{"waiting for a human decision"},
		},
		{
			name: "execution gap names the call and the commands",
			resp: &loomv1.ResumeSessionResponse{
				Outcome: loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_EXECUTION_GAP,
				Gap:     &loomv1.ExecutionGap{ToolName: "send_email", ToolUseId: "c9", StepIndex: 4, BatchPosition: 1},
			},
			wantOK:   false,
			contains: []string{`"send_email"`, "c9", "step 4", "position 1", "resolve-gap s1 --outcome succeeded", "resolve-gap s1 --outcome failed"},
		},
		{
			name:     "unspecified outcome is an error",
			resp:     &loomv1.ResumeSessionResponse{},
			wantOK:   false,
			contains: []string{"Unexpected resume outcome"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, ok := formatResumeResponse("s1", tt.resp)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v; text:\n%s", ok, tt.wantOK, text)
			}
			for _, want := range tt.contains {
				if !strings.Contains(text, want) {
					t.Errorf("output missing %q:\n%s", want, text)
				}
			}
		})
	}
}
