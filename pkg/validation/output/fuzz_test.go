// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import (
	"encoding/json"
	"testing"
)

// FuzzExtractJSON asserts ExtractJSON never panics and, when it returns a
// non-empty result, the result is valid JSON.
func FuzzExtractJSON(f *testing.F) {
	f.Add(`{"key": "value"}`)
	f.Add("prose {\"a\":1} prose")
	f.Add("```json\n[1,2]\n```")
	f.Add(`{"key": "unclosed`)
	f.Add("{}{}{}")
	f.Add(`{"esc": "\"}{\""}`)
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		got := ExtractJSON(text)
		if got == "" {
			return
		}
		var v any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatalf("ExtractJSON returned invalid JSON %q from input %q: %v", got, text, err)
		}
	})
}

// FuzzValidateJSONSchema asserts ValidateJSONSchema never panics for
// arbitrary document/schema pairs.
func FuzzValidateJSONSchema(f *testing.F) {
	f.Add(`{"name":"x"}`, `{"type":"object","required":["name"]}`)
	f.Add("no json here", `{"type":"object"}`)
	f.Add(`{"a":1}`, `{"type": nonsense`)
	f.Add(`[1,2,3]`, `{"type":"array","items":{"type":"integer"}}`)
	f.Add("", "")

	f.Fuzz(func(t *testing.T, text, schema string) {
		_, _ = ValidateJSONSchema(text, schema)
	})
}
