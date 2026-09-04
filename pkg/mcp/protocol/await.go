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
// This file defines the loom-specific result-_meta marker a server stamps on
// a SUCCESSFUL tools/call result to say "the real outcome of this call is the
// named resource's terminal state — await it instead of treating this result
// as final". It is the success-path counterpart of the failure-path
// resource_link convention resource_wait consumes: core MCP 2026-07-28 has no
// native long-running-operation marker (the tasks extension is a separate,
// unadopted spec), so the marker lives under loom's own reverse-DNS prefix
// per the _meta key naming rules.
package protocol

// MetaAwaitResource is the result-_meta key. Its value is an object naming
// the resource to await: {"uri": "<resource uri>"}.
const MetaAwaitResource = "com.teradata.loom/await-resource"

// AwaitResourceURI extracts the awaited resource URI from a result's _meta,
// returning "" when the marker is absent or malformed. Malformed markers are
// deliberately not errors: the result is still a valid tool result, it just
// doesn't ask to be awaited.
func AwaitResourceURI(meta map[string]interface{}) string {
	if meta == nil {
		return ""
	}
	obj, ok := meta[MetaAwaitResource].(map[string]interface{})
	if !ok {
		return ""
	}
	uri, _ := obj["uri"].(string)
	return uri
}
