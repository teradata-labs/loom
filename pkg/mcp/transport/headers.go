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
// This file implements the 2026-07-28 request-metadata headers (SEP-2243)
// on the Streamable HTTP client: standard header derivation from the
// JSON-RPC body, per-request extra headers carried in the context, and the
// typed HTTP status error the connection-setup fallback logic inspects.
package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// HTTPStatusError reports a non-2xx HTTP response whose body was not a
// JSON-RPC error response. Client connection setup uses it to distinguish
// "this server predates the probed method" (404/405/501 → fall back to the
// initialize handshake) from genuine failures.
type HTTPStatusError struct {
	Code int
	Body []byte
}

func (e *HTTPStatusError) Error() string {
	body := e.Body
	if len(body) > 256 {
		body = body[:256]
	}
	return fmt.Sprintf("HTTP error %d: %s", e.Code, body)
}

// extraHeadersKey carries per-request HTTP headers through the context.
type extraHeadersKey struct{}

// WithExtraHeaders returns a context carrying HTTP headers to set on the
// next Streamable HTTP POST issued under it, merged over any headers already
// carried. The client layer uses this for MCP-Protocol-Version and for
// Mcp-Param-* headers mirrored from x-mcp-header tool parameters; transports
// without HTTP headers (stdio) ignore it.
func WithExtraHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	merged := map[string]string{}
	for k, v := range ExtraHeadersFromContext(ctx) {
		merged[k] = v
	}
	for k, v := range headers {
		merged[k] = v
	}
	return context.WithValue(ctx, extraHeadersKey{}, merged)
}

// ExtraHeadersFromContext returns the per-request headers carried by ctx,
// or nil.
func ExtraHeadersFromContext(ctx context.Context) map[string]string {
	h, _ := ctx.Value(extraHeadersKey{}).(map[string]string)
	return h
}

// requestHeaderPeek extracts the body fields the standard request headers
// mirror, without decoding the full message.
type requestHeaderPeek struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	} `json:"params"`
}

// requestHeaderFields derives the Mcp-Method and Mcp-Name header values from
// a JSON-RPC request body per the 2026-07-28 transport: Mcp-Name carries
// params.name on tools/call and prompts/get, and params.uri on
// resources/read; it is omitted for every other method.
func requestHeaderFields(message []byte) (method, name string) {
	var peek requestHeaderPeek
	if err := json.Unmarshal(message, &peek); err != nil {
		return "", ""
	}
	switch peek.Method {
	case "tools/call", "prompts/get":
		return peek.Method, protocol.EncodeHeaderValue(peek.Params.Name)
	case "resources/read":
		return peek.Method, protocol.EncodeHeaderValue(peek.Params.URI)
	default:
		return peek.Method, ""
	}
}
