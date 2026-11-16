// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// ExtraBody is a set of top-level JSON members a gateway-fronting client adds
// to every request body, beside the fields the client itself writes. The
// Vercel AI Gateway reads its routing options this way
// ({"providerOptions": {"gateway": {...}}}).
//
// Keys that name a field of the client's own request type are rejected: a
// gateway option must never silently replace the model, the messages or the
// tools the client chose.
type ExtraBody map[string]any

// JSONFieldNames returns the JSON member names a struct type marshals, read
// from its `json` tags (untagged exported fields use the Go field name). It
// is how a client derives the reserved keys for ValidateExtraBody from its own
// request type, so the reserved set never drifts from the wire struct.
func JSONFieldNames(v any) map[string]struct{} {
	names := make(map[string]struct{})
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		names[name] = struct{}{}
	}
	return names
}

// ValidateExtraBody checks extra against the reserved member names of the
// client's request type and confirms every value marshals to JSON. It returns
// the marshalled object ({"k":v,...}, keys sorted) for MergeExtraBody, or nil
// when extra is empty.
func ValidateExtraBody(extra ExtraBody, reserved map[string]struct{}) ([]byte, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	var collisions []string
	for k := range extra {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("extra body: empty key")
		}
		if !utf8.ValidString(k) {
			// json.Marshal would rewrite the key to U+FFFD, so the member the
			// gateway sees would not be the one configured.
			return nil, fmt.Errorf("extra body: key %q is not valid UTF-8", k)
		}
		if _, clash := reserved[k]; clash {
			collisions = append(collisions, k)
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return nil, fmt.Errorf("extra body: key(s) %s collide with fields the client sets itself", strings.Join(collisions, ", "))
	}
	raw, err := json.Marshal(map[string]any(extra))
	if err != nil {
		return nil, fmt.Errorf("extra body: %w", err)
	}
	return raw, nil
}

// MergeExtraBody appends the members of extraJSON (a JSON object from
// ValidateExtraBody) to the JSON object body. Keys are not de-duplicated
// here: ValidateExtraBody has already rejected every key the request type can
// write. body is returned unchanged when extraJSON is empty or "{}".
func MergeExtraBody(body, extraJSON []byte) ([]byte, error) {
	extra := bytes.TrimSpace(extraJSON)
	if len(extra) == 0 || bytes.Equal(extra, []byte("{}")) {
		return body, nil
	}
	b := bytes.TrimSpace(body)
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return nil, fmt.Errorf("extra body: request body is not a JSON object")
	}
	if extra[0] != '{' || extra[len(extra)-1] != '}' {
		return nil, fmt.Errorf("extra body: extra members are not a JSON object")
	}
	inner := extra[1 : len(extra)-1]
	// No precomputed capacity: summing the two lengths is an arithmetic
	// overflow candidate (CodeQL go/allocation-size-overflow), and append
	// grows the buffer safely.
	out := append([]byte(nil), b[:len(b)-1]...)
	if len(bytes.TrimSpace(b[1:len(b)-1])) > 0 {
		out = append(out, ',')
	}
	out = append(out, inner...)
	out = append(out, '}')
	return out, nil
}
