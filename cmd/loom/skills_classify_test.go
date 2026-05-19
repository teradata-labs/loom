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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseClassifyResponse(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		domain    string
		want      string
		wantError bool
	}{
		{
			name:   "happy path",
			raw:    `{"path":"teradata/performance","reason":"optimizer hints"}`,
			domain: "teradata",
			want:   "teradata/performance",
		},
		{
			name:   "tolerates markdown fence",
			raw:    "```json\n{\"path\":\"teradata/security\",\"reason\":\"x\"}\n```",
			domain: "teradata",
			want:   "teradata/security",
		},
		{
			name:   "tolerates leading prose",
			raw:    "Here you go:\n{\"path\":\"teradata/sql\"}",
			domain: "teradata",
			want:   "teradata/sql",
		},
		{
			name:   "trims surrounding slashes",
			raw:    `{"path":"/teradata/cloud/"}`,
			domain: "teradata",
			want:   "teradata/cloud",
		},
		{
			name:   "domain root alone is acceptable",
			raw:    `{"path":"teradata"}`,
			domain: "teradata",
			want:   "teradata",
		},
		{
			name:      "empty path is rejected",
			raw:       `{"path":""}`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "wrong domain is rejected",
			raw:       `{"path":"general/foo"}`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "uppercase segment is rejected",
			raw:       `{"path":"teradata/Performance"}`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "underscore segment is rejected",
			raw:       `{"path":"teradata/data_types"}`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "embedded space is rejected",
			raw:       `{"path":"teradata/data types"}`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "junk JSON is rejected",
			raw:       `not json`,
			domain:    "teradata",
			wantError: true,
		},
		{
			name:      "domain prefix only as substring is rejected",
			raw:       `{"path":"teradatacloud/foo"}`,
			domain:    "teradata",
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClassifyResponse(tc.raw, tc.domain)
			if tc.wantError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
