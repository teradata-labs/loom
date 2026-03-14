package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/storage"
)

func TestSetSharedMemoryThreshold(t *testing.T) {
	tests := []struct {
		name          string
		threshold     int64
		wantThreshold int64
		wantEffective int64 // the effective threshold after resolution
	}{
		{
			name:          "default is -1 (use storage default)",
			threshold:     -1,
			wantThreshold: -1,
			wantEffective: int64(storage.DefaultSharedMemoryThreshold),
		},
		{
			name:          "zero means always reference",
			threshold:     0,
			wantThreshold: 0,
			wantEffective: 0,
		},
		{
			name:          "positive value means custom byte threshold",
			threshold:     4096,
			wantThreshold: 4096,
			wantEffective: 4096,
		},
		{
			name:          "large threshold",
			threshold:     1024 * 1024,
			wantThreshold: 1024 * 1024,
			wantEffective: 1024 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewAgent(nil, nil)

			// Verify default
			if tt.threshold == -1 {
				assert.Equal(t, int64(-1), agent.sharedMemoryThreshold,
					"new agent should default to -1")
			} else {
				agent.SetSharedMemoryThreshold(tt.threshold)
				assert.Equal(t, tt.wantThreshold, agent.sharedMemoryThreshold)
			}

			// Verify effective threshold computation
			effective := int64(storage.DefaultSharedMemoryThreshold)
			if agent.sharedMemoryThreshold >= 0 {
				effective = agent.sharedMemoryThreshold
			}
			assert.Equal(t, tt.wantEffective, effective,
				"effective threshold should match expected value")
		})
	}
}

func TestNewAgent_DefaultSharedMemoryThreshold(t *testing.T) {
	agent := NewAgent(nil, nil)
	require.NotNil(t, agent)
	assert.Equal(t, int64(-1), agent.sharedMemoryThreshold,
		"NewAgent should initialize sharedMemoryThreshold to -1")
}

func TestSetSharedMemoryThreshold_Overwrite(t *testing.T) {
	agent := NewAgent(nil, nil)

	agent.SetSharedMemoryThreshold(1000)
	assert.Equal(t, int64(1000), agent.sharedMemoryThreshold)

	agent.SetSharedMemoryThreshold(2000)
	assert.Equal(t, int64(2000), agent.sharedMemoryThreshold)

	// Reset back to default
	agent.SetSharedMemoryThreshold(-1)
	assert.Equal(t, int64(-1), agent.sharedMemoryThreshold)
}
