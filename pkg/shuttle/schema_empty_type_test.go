// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"encoding/json"
	"strings"
	"testing"
)

// A property an MCP server describes only through anyOf (a nullable string,
// say) has no type of its own. It must reach the model with no "type" key:
// an empty one is not JSON Schema, and Bedrock refuses the whole request.
func TestAnyOfOnlyPropertyCarriesNoEmptyType(t *testing.T) {
	raw := `{"type":"object","properties":{"sql":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null}},"required":[]}`
	s, err := FromJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(NormalizeSchema(s).Properties)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"type":""`) {
		t.Fatalf("empty type sent: %s", out)
	}
	if !strings.Contains(string(out), `"anyOf"`) {
		t.Fatalf("anyOf lost: %s", out)
	}
}
