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

package client

import (
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// responseTooLargeError makes one predicate hold for a size refusal whichever
// read path produced it.
//
// The two paths cannot deliver it the same way. The JSON read happens inside
// Send, so it fails Send and the caller gets a *transport.ResponseTooLargeError
// directly. An SSE refusal happens in the stream goroutine long after Send
// returned, so the only route back to the caller is a synthesized JSON-RPC
// error routed to the pending request by id — a *protocol.Error, which no
// errors.Is on a transport sentinel would match. That asymmetry is an
// implementation detail, and a caller should not have to match two unrelated
// error shapes to recognise one condition.
//
// So the client adapts the synthesized form on the way out: the message is
// unchanged, errors.Is(err, transport.ErrResponseTooLarge) is true, and
// errors.As(err, &*protocol.Error) still reaches the original error for a
// caller that wants the JSON-RPC code.
//
// *protocol.Error is held in a named field rather than embedded: an embedded
// *protocol.Error would introduce a field named Error that shadows the
// promoted Error() method, leaving the type not implementing error at all.
type responseTooLargeError struct {
	rpc *protocol.Error
}

func (e *responseTooLargeError) Error() string { return e.rpc.Error() }

// Unwrap keeps the JSON-RPC error reachable with errors.As.
func (e *responseTooLargeError) Unwrap() error { return e.rpc }

// Is matches the transport's sentinel, which is the one predicate.
func (e *responseTooLargeError) Is(target error) bool {
	return target == transport.ErrResponseTooLarge
}

// adaptResponseError normalizes a JSON-RPC error answer before it leaves the
// client. Only the locally synthesized size refusal is adapted; every other
// error is returned exactly as received.
//
// It keys on transport.CodeResponseTooLarge, which loom allocates from the
// -33000..-33099 range it reserves for errors its own transport synthesizes.
// A peer is not supposed to emit that code — the MCP 2026-07-28 policy tells
// implementations to allocate their own codes outside the JSON-RPC reserved
// range, so another implementation could in principle pick the same number,
// and such an answer would be reported as a size refusal. The code is the only
// thing that survives the message channel, so it is the only thing available
// to key on; the trade is deliberate and recorded here.
func adaptResponseError(rpcErr *protocol.Error) error {
	if rpcErr == nil {
		return nil
	}
	if rpcErr.Code == transport.CodeResponseTooLarge {
		return &responseTooLargeError{rpc: rpcErr}
	}
	return rpcErr
}
