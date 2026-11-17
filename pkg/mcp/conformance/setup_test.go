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
package conformance

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreparedContext_SlowDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 45 * time.Second
		var setupCtx context.Context
		ctx := preparedContext(t, timeout, func(ctx context.Context) error {
			setupCtx = ctx
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "package preparation must have a deadline")
			require.Equal(t, 2*time.Minute, time.Until(deadline))

			// A cold download can take longer than the entire handshake budget.
			// synctest advances virtual time, so this test does not wait 46s.
			time.Sleep(timeout + time.Second)
			return ctx.Err()
		})

		require.ErrorIs(t, setupCtx.Err(), context.Canceled)
		require.NoError(t, ctx.Err(), "preparation must not consume the protocol budget")
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, timeout, time.Until(deadline))

		// The fresh deadline must still bound a stalled handshake.
		time.Sleep(timeout)
		synctest.Wait()
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
}
