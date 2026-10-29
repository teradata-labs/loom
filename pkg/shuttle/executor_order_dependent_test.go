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

package shuttle

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An executor-level permission checker that may hold a call for human approval
// makes admission order-dependent even with no chain attached; a yolo or plain
// name-level checker does not.
func TestExecutor_AdmissionOrderDependent_PermissionChecker(t *testing.T) {
	cases := []struct {
		name    string
		checker *PermissionChecker
		want    bool
	}{
		{"no checker, no chain", nil, false},
		{"name-level checker", NewPermissionChecker(PermissionConfig{AllowedTools: []string{"read"}}), false},
		{"require_approval", NewPermissionChecker(PermissionConfig{RequireApproval: true}), true},
		{"require_approval with yolo", NewPermissionChecker(PermissionConfig{RequireApproval: true, YOLO: true}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewExecutor(NewRegistry())
			e.SetPermissionChecker(tc.checker)
			assert.Equal(t, tc.want, e.AdmissionOrderDependent())
		})
	}
}
