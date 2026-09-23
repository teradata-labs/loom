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
package protocol

import "testing"

func TestAwaitResourceURI(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]interface{}
		want string
	}{
		{name: "nil meta", meta: nil, want: ""},
		{name: "absent key", meta: map[string]interface{}{"other": 1}, want: ""},
		{
			name: "well-formed",
			meta: map[string]interface{}{MetaAwaitResource: map[string]interface{}{"uri": "gdp://jobs/1"}},
			want: "gdp://jobs/1",
		},
		{
			name: "wrong value shape",
			meta: map[string]interface{}{MetaAwaitResource: "gdp://jobs/1"},
			want: "",
		},
		{
			name: "missing uri field",
			meta: map[string]interface{}{MetaAwaitResource: map[string]interface{}{"name": "x"}},
			want: "",
		},
		{
			name: "non-string uri",
			meta: map[string]interface{}{MetaAwaitResource: map[string]interface{}{"uri": 7}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AwaitResourceURI(tt.meta); got != tt.want {
				t.Errorf("AwaitResourceURI() = %q, want %q", got, tt.want)
			}
		})
	}
}
