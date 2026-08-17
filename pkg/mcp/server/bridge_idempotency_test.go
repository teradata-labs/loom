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
package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/grpc/metadata"
)

func TestForwardIdempotencyKey(t *testing.T) {
	// A stateless request whose _meta carries the vendor idempotency key.
	params := []byte(`{"name":"loom_weave","_meta":{
		"` + protocol.MetaProtocolVersion + `":"` + protocol.Version20260728 + `",
		"` + protocol.MetaIdempotencyKey + `":"key-123"}}`)
	ctx := withRequestMeta(context.Background(), json.RawMessage(params))

	out := forwardIdempotencyKey(ctx)
	md, ok := metadata.FromOutgoingContext(out)
	require.True(t, ok, "key must be forwarded as outgoing gRPC metadata")
	assert.Equal(t, []string{"key-123"}, md.Get(types.IdempotencyKeyMetadataKey))

	// No _meta → context unchanged.
	plain := forwardIdempotencyKey(context.Background())
	_, ok = metadata.FromOutgoingContext(plain)
	assert.False(t, ok)
}
