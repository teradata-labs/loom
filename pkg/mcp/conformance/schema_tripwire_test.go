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
// Schema tripwire (Phase 8): every wire identifier pkg/mcp emits must exist
// in the vendored official 2026-07-28 schema. This is the cheap local drift
// check; the interop suite (interop_test.go, build tag "interop") is the
// behavioral one. The interop suite already caught the protocolVersions →
// supportedVersions drift this test would now trip on.
package conformance

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestVendoredSchemaContainsEmittedIdentifiers(t *testing.T) {
	raw, err := os.ReadFile("testdata/schema.2026-07-28.json")
	require.NoError(t, err, "vendored schema missing; see testdata/README.md")

	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.NotEmpty(t, schema.Defs)

	// Definitions our implementation mirrors as Go types.
	for _, def := range []string{
		"DiscoverResult", "InputRequiredResult", "HeaderMismatchError",
		"UnsupportedProtocolVersionError", "ElicitRequest",
	} {
		assert.Contains(t, schema.Defs, def, "schema definition %s missing", def)
	}

	// Field names and reserved keys we serialize.
	text := string(raw)
	for _, ident := range []string{
		"supportedVersions", "inputRequests", "inputResponses", "requestState",
		"resultType", "ttlMs", "cacheScope", "x-mcp-header",
		protocol.MetaProtocolVersion, protocol.MetaClientCapabilities,
		protocol.MetaClientInfo, protocol.MetaServerInfo,
		protocol.MetaSubscriptionID, protocol.MetaLogLevel,
		"toolsListChanged", "resourceSubscriptions",
	} {
		assert.True(t, strings.Contains(text, ident),
			"wire identifier %q not found in the official schema — local drift or upstream change", ident)
	}
}
