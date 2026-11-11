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

// Error codes the transport synthesizes ITSELF, for failures that never came
// from the peer.
//
// MCP 2026-07-28 ("Error Codes", basic/index) partitions the JSON-RPC
// server-error range and closes both halves to a code like these:
//
//	-32000..-32019  legacy. "New codes MUST NOT be allocated in this
//	                sub-range, and new implementations SHOULD NOT use codes
//	                from this sub-range at all."
//	-32020..-32099  reserved for the MCP specification. "Implementations
//	                MUST NOT emit any code from this sub-range that is not
//	                defined by this specification."
//
// The same section names this exact situation and says what to do with it:
// "Errors that are purely local to an implementation (for example, a request
// timeout raised inside an SDK) are not currently assigned codes by this
// specification. Implementations surfacing local errors in JSON-RPC-shaped
// structures should ensure they cannot be mistaken for errors received from
// the peer... New error codes for purposes not defined by this specification
// SHOULD be allocated outside the JSON-RPC reserved range (-32768 to
// -32000)."
//
// So loom allocates its own locally-synthesized transport errors in
// -33000..-33099, below the reserved range. A code from that block cannot be
// confused with anything a server sent, which is the property the spec asks
// for. Allocate sequentially; record every code here.
const (
	// CodeResponseTooLarge is synthesized when one response message exceeded
	// StreamableHTTPConfig.MaxResponseBytes and was rejected rather than
	// buffered.
	//
	// Distinct from CodeStreamLost deliberately: a lost stream says "re-issue
	// the request", and re-issuing an oversized response fails the same way
	// every time. The caller's recovery is to raise the limit, or to ask the
	// server for less — not to retry.
	//
	// It was -32002 in an earlier revision of this branch, which was wrong
	// twice over: the MCP specification defines -32002 as "Resource not
	// found" in 2025-06-18 and 2025-11-25 (server/resources, "Error
	// Handling"), so a consumer of the revision loom targets would read a
	// size refusal as "the resource does not exist" — and on the PR's own
	// resources/read file-delivery path that is precisely the wrong
	// conclusion. -32001 was wrong before that, colliding with the
	// TypeScript SDK's ErrorCode.RequestTimeout.
	CodeResponseTooLarge = -33001
)

// CodeStreamLost is the error code the transport synthesizes when a response
// stream ends before the final response for an in-flight request arrived. The
// 2026-07-28 revision removed stream resumption, so the caller's recovery is
// to re-issue the request as a new request (with the same idempotency key).
//
// It sits at -32000, inside the legacy sub-range that new implementations are
// told not to use, and it shares that value with the TypeScript SDK's
// ErrorCode.ConnectionClosed — which means the same thing to a caller, so the
// overlap is harmless. Left as it is deliberately: the value predates this
// policy and is grandfathered ("existing SDK usage is grandfathered", the
// 2026-07-28 changelog), and callers already match on it, so moving it would
// be a breaking change unrelated to the bound this file's other code serves.
// Moving it to the -33000 block is a sound follow-up, not a drive-by.
const CodeStreamLost = -32000

// requestHeaderPeek extracts the body fields the standard request headers
// mirror, without decoding the full message.
type requestHeaderPeek struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	} `json:"params"`
}

// requestID extracts the raw JSON-RPC id from a request body, or nil for
// notifications and unparseable bodies.
func requestID(message []byte) json.RawMessage {
	var peek requestHeaderPeek
	if err := json.Unmarshal(message, &peek); err != nil {
		return nil
	}
	if string(peek.ID) == "null" {
		return nil
	}
	return peek.ID
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
