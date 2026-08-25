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

import (
	"os"
	"sort"
	"strings"
)

// ExpandEnvPlaceholders expands ${VAR} references while preserving bare dollar
// signs and unresolved or malformed placeholders. A double dollar sign emits a
// single literal dollar sign.
func ExpandEnvPlaceholders(value string) string {
	var expanded strings.Builder
	expanded.Grow(len(value))

	for i := 0; i < len(value); {
		if value[i] != '$' {
			expanded.WriteByte(value[i])
			i++
			continue
		}

		if i+1 < len(value) && value[i+1] == '$' {
			expanded.WriteByte('$')
			i += 2
			continue
		}

		if i+1 >= len(value) || value[i+1] != '{' {
			expanded.WriteByte('$')
			i++
			continue
		}

		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			expanded.WriteString(value[i:])
			break
		}
		end += i + 2
		name := value[i+2 : end]
		if replacement, ok := os.LookupEnv(name); ok && name != "" {
			expanded.WriteString(replacement)
		} else {
			expanded.WriteString(value[i : end+1])
		}
		i = end + 1
	}

	return expanded.String()
}

// UnresolvedEnvPlaceholders returns unset variable names referenced with the
// supported ${VAR} syntax. Bare dollar signs and escaped dollars are ignored.
func UnresolvedEnvPlaceholders(value string) []string {
	missing := make(map[string]struct{})
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '$' && i+1 < len(value) && value[i+1] == '$' {
			i++ // skip the escaped dollar — the expander emits a literal '$', no variable reference
			continue
		}
		if value[i] != '$' || value[i+1] != '{' {
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			break
		}
		end += i + 2
		name := value[i+2 : end]
		if name != "" {
			if _, ok := os.LookupEnv(name); !ok {
				missing[name] = struct{}{}
			}
		}
		i = end
	}

	variables := make([]string, 0, len(missing))
	for name := range missing {
		variables = append(variables, name)
	}
	sort.Strings(variables)
	return variables
}
