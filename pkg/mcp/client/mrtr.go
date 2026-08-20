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
// This file implements the client half of Multi Round-Trip Requests
// (2026-07-28, SEP-2322): answering a server's inputRequests and retrying the
// original request.
package client

import (
	"context"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// DefaultMRTRMaxRounds caps retry rounds per logical call when MRTRConfig
// does not set one. Each round costs a full round trip (and typically a
// human interaction), so the default is deliberately small.
const DefaultMRTRMaxRounds = 5

// InputHandler answers a server's inputRequests during an MRTR exchange.
// Keys of the returned map must match the request keys. Returning an error
// aborts the exchange and fails the original call.
type InputHandler func(ctx context.Context, reqs protocol.InputRequests) (protocol.InputResponses, error)

// MRTRConfig configures how the client drives Multi Round-Trip Requests.
// A nil Handler preserves fail-fast: input_required results surface as
// InputRequiredNotSupportedError, which is correct for headless contexts
// that cannot answer elicitations.
type MRTRConfig struct {
	Handler   InputHandler
	MaxRounds int // <= 0 means DefaultMRTRMaxRounds
}

func (m MRTRConfig) maxRounds() int {
	if m.MaxRounds > 0 {
		return m.MaxRounds
	}
	return DefaultMRTRMaxRounds
}

// MRTRRoundsExceededError reports an MRTR exchange that did not complete
// within the configured round budget.
type MRTRRoundsExceededError struct {
	Method string
	Rounds int
}

func (e *MRTRRoundsExceededError) Error() string {
	return fmt.Sprintf("%s did not complete after %d MRTR rounds", e.Method, e.Rounds)
}
