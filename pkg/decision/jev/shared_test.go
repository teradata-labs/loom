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

package jev

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestSharedReturnsOneClientPerConfig(t *testing.T) {
	ResetShared()
	t.Cleanup(ResetShared)

	base := Config{APIKey: "jv_test_not_a_real_key", RequestsPerMinute: 28}
	a, err := Shared(base)
	require.NoError(t, err)
	b, err := Shared(base)
	require.NoError(t, err)
	assert.Same(t, a, b, "identical config: one client, one limiter")

	other := base
	other.RequestsPerMinute = 60
	c, err := Shared(other)
	require.NoError(t, err)
	assert.NotSame(t, a, c, "a different budget is a different client")

	keyed := base
	keyed.APIKey = "jv_test_other_not_a_real_key"
	d, err := Shared(keyed)
	require.NoError(t, err)
	assert.NotSame(t, a, d, "different credentials never share")

	_, err = Shared(Config{})
	assert.Error(t, err, "New's validation still applies")
}

func TestSharedIsSafeUnderConcurrentFirstUse(t *testing.T) {
	ResetShared()
	t.Cleanup(ResetShared)
	cfg := Config{APIKey: "jv_test_not_a_real_key"}
	var wg sync.WaitGroup
	got := make([]*Client, 16)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := Shared(cfg)
			if err == nil {
				got[i] = c
			}
		}(i)
	}
	wg.Wait()
	for _, c := range got {
		require.NotNil(t, c)
		assert.Same(t, got[0], c)
	}
}

func TestFromDecisionConfigCarriesRequestsPerMinute(t *testing.T) {
	cfg, err := fromDecisionConfig(&loomv1.DecisionConfig{RequestsPerMinute: 28}, func(k string) string {
		if k == EnvAIGatewayAPIKey {
			return "vck_test_not_a_real_key"
		}
		return ""
	})
	require.NoError(t, err)
	assert.Equal(t, 28.0, cfg.RequestsPerMinute)
	assert.Equal(t, VercelGatewayBaseURL, cfg.BaseURL)
	assert.Equal(t, VercelGatewayModel, cfg.Model)
}
