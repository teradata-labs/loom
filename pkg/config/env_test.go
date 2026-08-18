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
package config

import "testing"

func TestExpandEnvPlaceholders(t *testing.T) {
	t.Setenv("LOOM_TEST_SECRET", "resolved$secret")

	tests := map[string]struct {
		input string
		want  string
	}{
		"braced variable":       {input: "Bearer ${LOOM_TEST_SECRET}", want: "Bearer resolved$secret"},
		"bare dollar preserved": {input: "sk-ab$Cd9xyz", want: "sk-ab$Cd9xyz"},
		"double dollar escaped": {input: "pa$$word", want: "pa$word"},
		"unresolved preserved":  {input: "${LOOM_TEST_UNSET}", want: "${LOOM_TEST_UNSET}"},
		"malformed preserved":   {input: "before-${LOOM_TEST_SECRET", want: "before-${LOOM_TEST_SECRET"},
		"empty preserved":       {input: "${}", want: "${}"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := ExpandEnvPlaceholders(test.input); got != test.want {
				t.Fatalf("ExpandEnvPlaceholders(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestUnresolvedEnvPlaceholders(t *testing.T) {
	t.Setenv("LOOM_TEST_SET", "value")
	got := UnresolvedEnvPlaceholders("$bare $$ ${LOOM_TEST_SET} ${MISSING_B} ${MISSING_A} ${MISSING_B}")
	want := []string{"MISSING_A", "MISSING_B"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("UnresolvedEnvPlaceholders() = %v, want %v", got, want)
	}
}
