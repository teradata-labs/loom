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
		setThreshold  bool  // whether to call SetSharedMemoryThreshold before asserting
		threshold     int64 // value passed to SetSharedMemoryThreshold when setThreshold is true
		wantThreshold int64 // the stored field value
		wantEffective int64 // the effective threshold after resolution
	}{
		{
			name:          "fresh agent defaults to the positive storage default",
			setThreshold:  false,
			wantThreshold: int64(storage.DefaultSharedMemoryThreshold),
			wantEffective: int64(storage.DefaultSharedMemoryThreshold),
		},
		{
			name:          "-1 selects the storage default at resolution time",
			setThreshold:  true,
			threshold:     -1,
			wantThreshold: -1,
			wantEffective: int64(storage.DefaultSharedMemoryThreshold),
		},
		{
			name:          "zero means always reference",
			setThreshold:  true,
			threshold:     0,
			wantThreshold: 0,
			wantEffective: 0,
		},
		{
			name:          "positive value means custom byte threshold",
			setThreshold:  true,
			threshold:     4096,
			wantThreshold: 4096,
			wantEffective: 4096,
		},
		{
			name:          "large threshold",
			setThreshold:  true,
			threshold:     1024 * 1024,
			wantThreshold: 1024 * 1024,
			wantEffective: 1024 * 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := NewAgent(nil, nil)

			if tt.setThreshold {
				agent.SetSharedMemoryThreshold(tt.threshold)
			}
			assert.Equal(t, tt.wantThreshold, agent.sharedMemoryThreshold)

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
	assert.Equal(t, int64(storage.DefaultSharedMemoryThreshold), agent.sharedMemoryThreshold,
		"NewAgent initializes sharedMemoryThreshold to the positive storage default (64 KiB)")
	assert.Positive(t, agent.sharedMemoryThreshold,
		"the default threshold is positive, never the -1 inline-everything sentinel")
}

func TestSetSharedMemoryThreshold_Overwrite(t *testing.T) {
	agent := NewAgent(nil, nil)

	agent.SetSharedMemoryThreshold(1000)
	assert.Equal(t, int64(1000), agent.sharedMemoryThreshold)

	agent.SetSharedMemoryThreshold(2000)
	assert.Equal(t, int64(2000), agent.sharedMemoryThreshold)

	// -1 is the sentinel that defers to the storage default at resolution time.
	agent.SetSharedMemoryThreshold(-1)
	assert.Equal(t, int64(-1), agent.sharedMemoryThreshold)
}

// TestSetSharedMemoryThreshold_PropagatesToExecutor pins that a threshold set
// AFTER shared memory is wired (the order the registry uses) reaches the
// executor, so both offload sites agree. Before the fix the executor kept the
// value captured at SetSharedMemory time.
func TestSetSharedMemoryThreshold_PropagatesToExecutor(t *testing.T) {
	agent := NewAgent(nil, nil)
	store := storage.NewSharedMemoryStore(&storage.Config{MaxMemoryBytes: 1 << 20})

	agent.SetSharedMemory(store)         // executor captures the default
	agent.SetSharedMemoryThreshold(4096) // later reconfigure — must propagate
	assert.Equal(t, int64(4096), agent.executor.SharedMemoryThreshold(),
		"the executor's offload threshold tracks a post-wiring setter call")
}
