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

package shelljail

import (
	"net/url"
	"sort"
	"strings"
)

// sensitiveEnvNames are withheld by exact (upper-cased) name.
var sensitiveEnvNames = map[string]bool{
	"DATABASE_PASSWORD": true,
	"DB_PASS":           true,
}

// sensitiveEnvSubstrings are withheld wherever they appear in a name.
var sensitiveEnvSubstrings = []string{"SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "PRIVATE_KEY"}

// sensitiveEnvSegments are withheld when they are a whole underscore-separated
// part of a name, so GH_TOKEN and AWS_ACCESS_KEY_ID match while
// TOKENIZERS_PARALLELISM and KEYBOARD_LAYOUT do not.
var sensitiveEnvSegments = map[string]bool{"TOKEN": true, "KEY": true, "PASS": true, "DSN": true}

// SensitiveEnvName reports whether an environment variable's name looks like
// it holds a credential. Names are compared upper-cased.
func SensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	if sensitiveEnvNames[upper] || strings.HasSuffix(upper, "DATABASE_URL") {
		return true
	}
	for _, sub := range sensitiveEnvSubstrings {
		if strings.Contains(upper, sub) {
			return true
		}
	}
	for _, seg := range strings.Split(upper, "_") {
		if sensitiveEnvSegments[seg] {
			return true
		}
	}
	return false
}

// URLHasPassword reports whether value is a URL with a password in its
// userinfo (postgres://user:pw@host).
func URLHasPassword(value string) bool {
	if !strings.Contains(value, "://") || !strings.Contains(value, "@") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.User == nil {
		return false
	}
	_, ok := u.User.Password()
	return ok
}

// execEnvNames change what a program executes or loads, so a jailed command
// may not set them for the programs it starts.
var execEnvNames = map[string]bool{
	"PAGER": true, "EDITOR": true, "VISUAL": true, "BASH_ENV": true, "ENV": true,
	"LESSOPEN": true, "LESSCLOSE": true, "NODE_OPTIONS": true, "PERL5OPT": true,
	"PERL5LIB": true, "PERLLIB": true, "RUBYOPT": true, "RUBYLIB": true,
	"SHELLOPTS": true, "BASHOPTS": true, "PROMPT_COMMAND": true, "IFS": true,
	"PS4": true, "MANPAGER": true, "BROWSER": true, "SSH_ASKPASS": true,
}

// execEnvPrefixes are name prefixes with the same effect.
var execEnvPrefixes = []string{"LD_", "DYLD_", "GIT_", "PYTHON", "BASH_FUNC_"}

// ExecEnvName reports whether name changes what a program executes.
func ExecEnvName(name string) bool {
	if execEnvNames[name] {
		return true
	}
	for _, p := range execEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// FilterEnv splits NAME=value entries into those a jailed program may receive
// and the names it may not: credentials (SensitiveEnvName, URLHasPassword)
// and names that change what a program executes (ExecEnvName). The dropped
// names are sorted and unique.
func FilterEnv(entries []string) (kept, dropped []string) {
	seen := map[string]bool{}
	for _, kv := range entries {
		name, value, _ := strings.Cut(kv, "=")
		if SensitiveEnvName(name) || URLHasPassword(value) || ExecEnvName(name) {
			if !seen[name] {
				seen[name] = true
				dropped = append(dropped, name)
			}
			continue
		}
		kept = append(kept, kv)
	}
	sort.Strings(dropped)
	return kept, dropped
}
