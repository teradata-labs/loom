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

package transport

import (
	"errors"
	"fmt"
	"math"
)

// DefaultMaxResponseBytes bounds one JSON-RPC message read from a
// streamable-http server when StreamableHTTPConfig.MaxResponseBytes is unset.
//
// 16 MiB. A tool result is held whole in memory while it is decoded, so the
// bound is what stands between one oversized or hostile response and the
// process's heap. It is deliberately generous: an MCP result that legitimately
// carries a large payload (a base64 resource blob, a wide query result) must
// still arrive, and a caller that needs more can raise it per transport.
const DefaultMaxResponseBytes int64 = 16 << 20

// ErrResponseTooLarge is the sentinel every *ResponseTooLargeError matches
// under errors.Is, so a caller need not know the concrete type to tell this
// failure from a transport error.
//
// It means the read STOPPED at the limit: no truncated message is delivered,
// because a truncated JSON-RPC message is not a message. A caller that wants
// the data must raise the limit and ask again.
var ErrResponseTooLarge = errors.New("MCP response exceeds the configured size limit")

// ResponseTooLargeError reports which limit was exceeded.
type ResponseTooLargeError struct {
	// Limit is the byte bound that was exceeded.
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("MCP response exceeds the %d byte limit", e.Limit)
}

// Is makes errors.Is(err, ErrResponseTooLarge) true for this type.
func (e *ResponseTooLargeError) Is(target error) bool {
	return target == ErrResponseTooLarge
}

// MaxResponseLimit is the largest bound effectiveResponseLimit will return,
// and so the value that means "effectively no limit": 4 EiB is past every
// real response, and past what any machine could hold.
//
// It exists because both read paths add a little to the limit before using
// it — one byte on the JSON path, so a body exactly at the limit still reads,
// and sseLineOverheadBytes on the SSE path for a line's prefix and
// terminator. At math.MaxInt64 those additions overflow to negative, and the
// symptom is not a refusal but a HANG: io.LimitReader with a negative N
// returns EOF at once, the empty body is taken as a notification
// acknowledgment, and the caller waits for a reply that never comes. The SSE
// path returns io.EOF just as promptly.
//
// Clamping here makes that arithmetic safe wherever the limit came from —
// config, proto, or a caller building StreamableHTTPConfig by hand — rather
// than at each use.
const MaxResponseLimit int64 = math.MaxInt64 / 2

// effectiveResponseLimit maps a non-positive limit to the default, so a caller
// that leaves MaxResponseBytes zero gets the default bound rather than a
// transport that rejects every response, and clamps an enormous one to
// MaxResponseLimit so the read paths' own arithmetic cannot overflow.
func effectiveResponseLimit(limit int64) int64 {
	if limit <= 0 {
		return DefaultMaxResponseBytes
	}
	if limit > MaxResponseLimit {
		return MaxResponseLimit
	}
	return limit
}
