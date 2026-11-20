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
package observability

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// RedactedValue replaces a credential value removed before export.
const RedactedValue = "[REDACTED]"

// RedactText removes recognized credentials and PII from s. If redaction is
// still changing the text on the eighth pass, it replaces the whole input.
func RedactText(s string) string {
	return redactUntilStable(s, func(text string) string {
		return redactPIIString(redactCredentialStringOnce(text))
	})
}

func redactUntilStable(s string, pass func(string) string) string {
	for i := 0; i < 8; i++ {
		next := pass(s)
		if next == s {
			return s
		}
		s = next
	}
	return RedactedValue
}

// Credential redaction of attribute values. Dropping attributes whose own key
// names a credential (isCredentialKey) is not enough: tool arguments, tool
// results and the tool calls an LLM makes are exported as JSON or text inside
// one attribute (input.value, output.value), so a credential can sit at any
// depth of a value whose key says nothing. Values are cleaned in two layers:
//
//  1. Structure: a value that is a JSON document is decoded, every field whose
//     key names a credential is replaced at any depth, and every string leaf is
//     cleaned recursively (a leaf may itself hold JSON or text).
//  2. Patterns: the remaining text is searched for quoted key/value pairs with a
//     credential key (including JSON escaped inside a string, and truncated JSON
//     that does not decode), credential header lines, name=value assignments, credentials named in
//     prose, Bearer/Basic
//     credentials, well-known key formats, JWTs and credential URL parameters.
//
// The patterns favour leaking nothing over keeping text: the description of a
// tool parameter named api_key, for example, is redacted too.

// credentialValueKeys are field names whose value is a credential, after
// normalisation (lower case, '-' and '.' become '_').
var credentialValueKeys = map[string]bool{ //nolint:gochecknoglobals
	"password": true, "passwd": true, "pwd": true, "secret": true, "token": true,
	"api_key": true, "apikey": true, "x_api_key": true, "access_token": true, "refresh_token": true,
	"id_token": true, "auth_token": true, "session_token": true, "bearer": true,
	"authorization": true, "proxy_authorization": true, "cookie": true, "set_cookie": true,
	"client_secret": true, "private_key": true, "ssh_key": true, "aws_secret_access_key": true,
	"aws_session_token": true, "auth": true, "credential": true, "credentials": true,
	"x_auth_token": true, "x_amz_security_token": true,
}

// isCredentialField is shared by structured values and span/event keys. Only
// a name that is "token" or ends in "_token" counts as a token credential.
func isCredentialField(name string) bool {
	var normalized strings.Builder
	var previous rune
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			normalized.WriteByte('_')
		}
		switch r {
		case '-', '.', ' ', '/', ':':
			normalized.WriteByte('_')
		default:
			normalized.WriteRune(unicode.ToLower(r))
		}
		previous = r
	}
	k := normalized.String()
	if credentialValueKeys[k] {
		return true
	}
	return strings.Contains(k, "password") || strings.Contains(k, "passwd") ||
		strings.Contains(k, "secret") || strings.Contains(k, "authorization") ||
		strings.Contains(k, "cookie") || strings.Contains(k, "private_key") || strings.Contains(k, "privatekey") ||
		strings.Contains(k, "access_key") || strings.Contains(k, "accesskey") || strings.HasSuffix(k, "_token") ||
		(strings.Contains(k, "api") && strings.Contains(k, "key"))
}

var (
	// "key": value in plain JSON text. The value is a JSON string (escapes
	// honoured), an array or object without nesting, a number or a literal.
	plainCredentialPairPattern = regexp.MustCompile(
		`"([A-Za-z0-9_.\-]{1,64})"(\s*:\s*)("(?:[^"\\]|\\.)*"|\[[^\[\]]*\]|\{[^{}]*\}|-?[0-9][0-9.eE+\-]*|true|false|null)`)
	// key": "value" — a JSON string pair whose opening quote was consumed by an
	// earlier unclosed quote in surrounding text.
	unbalancedCredentialPairPattern = regexp.MustCompile(
		`\b([A-Za-z][A-Za-z0-9_.\-]{0,63})"(\s*:\s*)("(?:[^"\\]|\\.)*")`)
	// \"key\":\"value\" — JSON escaped inside a JSON string (for example a
	// tool result carried in an LLM span's input). One or more backslashes
	// precede each quote.
	escapedCredentialPairPattern = regexp.MustCompile(
		`(\\+)"([A-Za-z0-9_.\-]{1,64})\\+"(\s*:\s*)\\+"(?:[^"\\]|\\[^"])*\\+"`)
	// Header lines: "Authorization: Bearer x", "Cookie: a=b" (not JSON).
	credentialHeaderPattern = regexp.MustCompile(
		`(?im)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key|x-auth-token)([ \t]*:[ \t]*)[^\r\n"]+`)
	// name=value and name: value outside JSON (log lines, env dumps, YAML,
	// function calls like connect(user="dbc", password="...")); replaced only
	// when the name is a credential field.
	credentialAssignmentPattern = regexp.MustCompile(
		`\b([A-Za-z][A-Za-z0-9_.\-]{0,63})([ \t]*[=:][ \t]*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s&"',;]+)`)
	// A credential word followed by its value in prose ("the password is
	// hunter2", "cookie sid=abc1"); replaced only when the value looks like a
	// credential (see credentialLike).
	credentialMentionPattern = regexp.MustCompile(
		`(?i)\b(passwords?|passwd|api[ _-]?keys?|access[ _-]?tokens?|tokens?|secrets?|cookies?)((?:\s+(?:is|was|are|were))?[ \t]*[:=]?[ \t]+)([^\s,;"'<>()\[\]{}]{6,})`)
	// Bearer and Basic credentials anywhere in text.
	authSchemePattern = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9\-._~+/]{8,}=*`)
	// Well-known key formats: OpenAI/Anthropic (sk-), Tavily, AWS access key
	// IDs, GitHub, Slack, Google API keys, GitLab.
	knownKeyPattern = regexp.MustCompile(
		`\b(?:sk-[A-Za-z0-9_\-]{16,}|tvly-[A-Za-z0-9_\-]{16,}|(?:AKIA|ASIA)[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|xox[abprs]-[A-Za-z0-9\-]{10,}|AIza[0-9A-Za-z_\-]{35}|glpat-[A-Za-z0-9_\-]{20,})`)
	// JSON Web Tokens.
	jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)
	// A JWT whose signature was cut off at a preview window is still sensitive.
	jwtPrefixPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}(?:\.[A-Za-z0-9_\-]*)?`)
	// Credential parameters in URLs.
	urlCredentialParamPattern = regexp.MustCompile(
		`(?i)([?&](?:api_key|apikey|access_token|refresh_token|token|password|secret|key|auth|sig|signature)=)[^&#\s"'\\]+`)
)

// redactCredentialString applies the same bounded, fail-closed passes without
// changing PII when only credential redaction is configured.
func redactCredentialString(s string) string {
	return redactUntilStable(s, redactCredentialStringOnce)
}

func redactCredentialStringOnce(s string) string {
	if out, ok := redactCredentialJSONDocument(s); ok {
		s = out
	}
	s = redactCredentialContainers(s)
	s = redactPlainCredentialPairs(s)
	s = escapedCredentialPairPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := escapedCredentialPairPattern.FindStringSubmatch(m)
		if !isCredentialField(sub[2]) {
			return m
		}
		q := sub[1] + `"`
		return q + sub[2] + q + sub[3] + q + RedactedValue + q
	})
	s = credentialHeaderPattern.ReplaceAllString(s, "${1}${2}"+RedactedValue)
	s = credentialAssignmentPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := credentialAssignmentPattern.FindStringSubmatch(m)
		if !isCredentialField(sub[1]) {
			return m
		}
		val := sub[3]
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			if val[1:len(val)-1] == RedactedValue {
				return m
			}
			q := val[:1]
			return sub[1] + sub[2] + q + RedactedValue + q
		}
		suffix := ""
		openParens := strings.Count(val, "(")
		closeParens := strings.Count(val, ")")
		for closeParens > openParens && strings.HasSuffix(val, ")") {
			val = val[:len(val)-1]
			suffix += ")"
			closeParens--
		}
		if val == RedactedValue {
			return m
		}
		return sub[1] + sub[2] + RedactedValue + suffix
	})
	s = credentialMentionPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := credentialMentionPattern.FindStringSubmatch(m)
		if sub[3] == RedactedValue || !credentialLike(sub[3]) {
			return m
		}
		return sub[1] + sub[2] + RedactedValue
	})
	s = authSchemePattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := authSchemePattern.FindStringSubmatch(m)
		token := m[len(sub[1])+len(sub[2]):]
		// A credential has a digit, base64 padding, or length; a word in prose
		// ("basic understanding", "bearer authentication") has none of them.
		if !strings.ContainsAny(token, "0123456789=") && len(token) < 20 {
			return m
		}
		return sub[1] + sub[2] + RedactedValue
	})
	s = knownKeyPattern.ReplaceAllString(s, RedactedValue)
	s = jwtPattern.ReplaceAllString(s, RedactedValue)
	s = jwtPrefixPattern.ReplaceAllString(s, RedactedValue)
	s = urlCredentialParamPattern.ReplaceAllString(s, "${1}"+RedactedValue)
	return redactUnterminatedCredentialValue(s)
}

// credentialValueOpenerPattern matches a credential-shaped key followed by the
// opening of a quoted, array or object value: "key": " (plain or escaped JSON),
// "key": [ or {, and key=" / key: ' assignments. The value itself is not
// matched; redactUnterminatedCredentialValue checks whether it is closed.
var credentialValueOpenerPattern = regexp.MustCompile( //nolint:gochecknoglobals
	`(?:(\\*)"([A-Za-z0-9_.\-]{1,64})\\*"|\b([A-Za-z][A-Za-z0-9_.\-]{0,63}))([ \t]*[:=][ \t]*)(\\*"|'|\[|\{)`)

// redactCredentialContainers handles nested objects and arrays even when the
// surrounding text is not a complete JSON document. An unfinished value is
// redacted through the end of the available text.
func redactCredentialContainers(s string) string {
	matches := credentialValueOpenerPattern.FindAllStringSubmatchIndex(s, -1)
	var out strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] < last {
			continue
		}
		opener := s[m[10]:m[11]]
		if opener != "{" && opener != "[" {
			continue
		}
		key := ""
		if m[4] >= 0 {
			key = s[m[4]:m[5]]
		} else if m[6] >= 0 {
			key = s[m[6]:m[7]]
		}
		if !isCredentialField(key) {
			continue
		}
		end, closed := containerEnd(s[m[11]:], opener[0])
		out.WriteString(s[last:m[10]])
		if m[4] >= 0 {
			quote := s[m[2]:m[3]] + `"`
			out.WriteString(quote + RedactedValue + quote)
		} else {
			out.WriteString(RedactedValue)
		}
		if !closed {
			return out.String()
		}
		last = m[11] + end
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

// containerEnd returns the index after the matching close, ignoring brackets
// inside strings. A mismatched close is treated as an unfinished value.
func containerEnd(rest string, opener byte) (int, bool) {
	stack := []byte{'}'}
	if opener == '[' {
		stack[0] = ']'
	}
	inString := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[':
			stack = append(stack, ']')
		case '{':
			stack = append(stack, '}')
		case ']', '}':
			if c != stack[len(stack)-1] {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// redactUnterminatedCredentialValue handles text cut inside a credential value,
// as happens when a tool result or LLM input is bounded before it is redacted:
// the patterns above need a closing quote or bracket, so the start of a long
// value whose end was cut off would otherwise survive. When the value of a
// credential key is not closed before the end of s, everything from the value
// on is replaced by RedactedValue. The earliest such key wins.
func redactUnterminatedCredentialValue(s string) string {
	for _, m := range credentialValueOpenerPattern.FindAllStringSubmatchIndex(s, -1) {
		key := ""
		switch {
		case m[4] >= 0:
			key = s[m[4]:m[5]]
		case m[6] >= 0:
			key = s[m[6]:m[7]]
		}
		if !isCredentialField(key) {
			continue
		}
		opener := s[m[10]:m[11]]
		if valueClosed(s[m[11]:], opener) {
			continue
		}
		if s[m[11]:] == RedactedValue {
			return s
		}
		return s[:m[11]] + RedactedValue
	}
	return s
}

// valueClosed reports whether rest, the text after a value's opener, contains
// the matching close: an unescaped '"' for a plain JSON string, the same run of
// backslashes and '"' for an escaped one, "'" for a single-quoted value, and
// the balancing bracket (outside JSON strings) for an array or object.
func valueClosed(rest, opener string) bool {
	switch opener {
	case `"`:
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '\\':
				i++
			case '"':
				return true
			}
		}
		return false
	case "'":
		return strings.Contains(rest, "'")
	case "[", "{":
		_, closed := containerEnd(rest, opener[0])
		return closed
	default: // escaped quote: backslashes then '"'
		return strings.Contains(rest, opener)
	}
}

// credentialLike reports whether a word after a credential word looks like a
// credential rather than prose: it mixes letters and digits, contains '=', or
// is at least 20 characters long. "token limit", "password policy" and
// "tokens 1500" are kept.
func credentialLike(v string) bool {
	if strings.Contains(v, "=") || len(v) >= 20 {
		return true
	}
	return strings.ContainsAny(v, "0123456789") &&
		strings.IndexFunc(v, func(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }) >= 0
}

// redactPlainCredentialPairs replaces the value of every "key": value pair in
// plain JSON text whose key names a credential. A pair whose value is a small
// object or array is matched as a whole, so its contents are cleaned
// recursively; pairs nested deeper are matched on their own, innermost first.
func redactPlainCredentialPairs(s string) string {
	s = plainCredentialPairPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := plainCredentialPairPattern.FindStringSubmatch(m)
		key, sep, value := sub[1], sub[2], sub[3]
		if value == RedactedValue || value == `"`+RedactedValue+`"` {
			return m
		}
		if isCredentialField(key) {
			return `"` + key + `"` + sep + `"` + RedactedValue + `"`
		}
		if len(value) >= 2 && (value[0] == '{' || value[0] == '[') {
			return `"` + key + `"` + sep + value[:1] + redactPlainCredentialPairs(value[1:len(value)-1]) + value[len(value)-1:]
		}
		return m
	})
	return unbalancedCredentialPairPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := unbalancedCredentialPairPattern.FindStringSubmatch(m)
		key, sep, value := sub[1], sub[2], sub[3]
		if value == RedactedValue || value == `"`+RedactedValue+`"` {
			return m
		}
		if isCredentialField(key) {
			return key + `"` + sep + `"` + RedactedValue + `"`
		}
		return m
	})
}

// redactCredentialJSONDocument cleans s when the whole string is a JSON object
// or array. It returns the re-encoded document and true only when something
// was replaced, so documents without credentials keep their exact text.
func redactCredentialJSONDocument(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if len(t) < 2 {
		return "", false
	}
	object := t[0] == '{' && t[len(t)-1] == '}'
	array := t[0] == '[' && t[len(t)-1] == ']'
	if !object && !array {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var doc interface{}
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return "", false
	}
	cleaned, changed := redactCredentialStructure(doc)
	if !changed {
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cleaned); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}

// redactCredentialStructure returns a copy of v with credential fields replaced
// at any depth and string leaves cleaned, and whether anything changed. The
// caller's value is never modified.
func redactCredentialStructure(v interface{}) (interface{}, bool) {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		changed := false
		for k, item := range val {
			if isCredentialField(k) {
				out[k] = RedactedValue
				changed = changed || item != RedactedValue
				continue
			}
			c, ch := redactCredentialStructure(item)
			out[k] = c
			changed = changed || ch
		}
		return out, changed
	case map[string]string:
		out := make(map[string]string, len(val))
		changed := false
		for k, item := range val {
			switch {
			case isCredentialField(k):
				out[k] = RedactedValue
				changed = changed || item != RedactedValue
			default:
				out[k] = redactCredentialString(item)
				changed = changed || out[k] != item
			}
		}
		return out, changed
	case []interface{}:
		out := make([]interface{}, len(val))
		changed := false
		for i, item := range val {
			c, ch := redactCredentialStructure(item)
			out[i] = c
			changed = changed || ch
		}
		return out, changed
	case string:
		r := redactCredentialString(val)
		return r, r != val
	default:
		return v, false
	}
}
