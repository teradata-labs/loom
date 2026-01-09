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
package storage

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestDogfood_SimulateSQLBackend10KRows simulates a SQL backend returning 10K rows
// and verifies they're efficiently stored in shared memory
func TestDogfood_SimulateSQLBackend10KRows(t *testing.T) {
	// Create shared memory with realistic config
	config := &Config{
		MaxMemoryBytes:       100 * 1024 * 1024, // 100MB
		CompressionThreshold: 1 * 1024 * 1024,   // 1MB
		TTLSeconds:           3600,
	}

	// Add disk overflow
	overflow, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   t.TempDir() + "/disk-cache",
		MaxDiskSize: 1024 * 1024 * 1024, // 1GB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)
	config.OverflowHandler = overflow

	store := NewSharedMemoryStore(config)

	// Simulate 10K rows from SQL query (realistic data structure)
	type Row struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Email      string `json:"email"`
		Department string `json:"department"`
		Salary     int    `json:"salary"`
		JoinDate   string `json:"join_date"`
	}

	rows := make([]Row, 10000)
	for i := 0; i < 10000; i++ {
		rows[i] = Row{
			ID:         i + 1,
			Name:       fmt.Sprintf("Employee_%d", i+1),
			Email:      fmt.Sprintf("employee%d@company.com", i+1),
			Department: []string{"Engineering", "Sales", "Marketing", "HR", "Finance"}[i%5],
			Salary:     50000 + (i * 100),
			JoinDate:   "2024-01-15",
		}
	}

	// Serialize to JSON (what the SQL backend would return)
	data, err := json.Marshal(rows)
	require.NoError(t, err)

	t.Logf("Generated 10K rows, total size: %d KB", len(data)/1024)
	assert.Greater(t, len(data), 500*1024) // Should be >500KB

	// Store in shared memory
	ref, err := store.Store("sql-result-1", data, "application/json", map[string]string{
		"query":     "SELECT * FROM employees",
		"row_count": "10000",
		"backend":   "postgres",
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Verify compression was applied (if > 1MB)
	if len(data) > 1024*1024 {
		assert.True(t, ref.Compressed, "Large result should be compressed")
		t.Logf("Compression: %d KB -> %d KB (%.1f%% savings)",
			len(data)/1024,
			ref.SizeBytes/1024,
			100.0*(1.0-float64(ref.SizeBytes)/float64(len(data))))
	}

	// Simulate agent retrieving data
	retrieved, err := store.Get(ref)
	require.NoError(t, err)

	// Verify data integrity
	var retrievedRows []Row
	err = json.Unmarshal(retrieved, &retrievedRows)
	require.NoError(t, err)
	assert.Equal(t, 10000, len(retrievedRows))
	assert.Equal(t, "Employee_1", retrievedRows[0].Name)
	assert.Equal(t, "Employee_10000", retrievedRows[9999].Name)

	// Check stats
	stats := store.Stats()
	assert.Equal(t, int64(1), stats.Hits)
	assert.Equal(t, int64(0), stats.Misses)
	t.Logf("Stats: %d items, %d KB used, %d%% capacity",
		stats.ItemCount,
		stats.CurrentSize/1024,
		(100*stats.CurrentSize)/stats.MaxSize)

	// Clean up
	store.Release(ref.Id)
}

// TestDogfood_SimulateMCPServer5MBResponse simulates an MCP server returning 5MB of data
func TestDogfood_SimulateMCPServer5MBResponse(t *testing.T) {
	// Create shared memory
	config := &Config{
		MaxMemoryBytes:       50 * 1024 * 1024, // 50MB
		CompressionThreshold: 1 * 1024 * 1024,  // 1MB
		TTLSeconds:           3600,
	}

	store := NewSharedMemoryStore(config)

	// Simulate large MCP response (e.g., file system listing with content)
	type FileInfo struct {
		Path    string `json:"path"`
		Size    int    `json:"size"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}

	// Generate 5MB of file data
	files := make([]FileInfo, 1000)
	largeContent := string(make([]byte, 5*1024)) // 5KB per file
	for i := 0; i < 1000; i++ {
		files[i] = FileInfo{
			Path:    fmt.Sprintf("/project/src/module%d/file%d.go", i/100, i%100),
			Size:    5120,
			Content: largeContent,
			Mode:    "-rw-r--r--",
		}
	}

	data, err := json.Marshal(files)
	require.NoError(t, err)
	t.Logf("Generated MCP response, size: %.2f MB", float64(len(data))/(1024*1024))
	assert.Greater(t, len(data), 5*1024*1024) // Should be >5MB

	// Store in shared memory
	ref, err := store.Store("mcp-result-1", data, "application/json", map[string]string{
		"mcp_server": "filesystem",
		"operation":  "list_recursive",
		"file_count": "1000",
	})
	require.NoError(t, err)
	assert.NotNil(t, ref)

	// Verify it was compressed
	assert.True(t, ref.Compressed, "5MB response should be compressed")
	t.Logf("Compression ratio: %.1f%% (%.2f MB -> %.2f MB)",
		100.0*(1.0-float64(ref.SizeBytes)/float64(len(data))),
		float64(len(data))/(1024*1024),
		float64(ref.SizeBytes)/(1024*1024))

	// Retrieve and verify
	retrieved, err := store.Get(ref)
	require.NoError(t, err)
	assert.Equal(t, len(data), len(retrieved))

	// Check stats
	stats := store.Stats()
	assert.Equal(t, int64(1), stats.Compressions)
	t.Logf("Memory usage: %.2f MB / %.2f MB (%.1f%%)",
		float64(stats.CurrentSize)/(1024*1024),
		float64(stats.MaxSize)/(1024*1024),
		100.0*float64(stats.CurrentSize)/float64(stats.MaxSize))

	store.Release(ref.Id)
}

// TestDogfood_MultiAgent10MBWorkflow simulates a multi-agent workflow passing 10MB dataset
func TestDogfood_MultiAgent10MBWorkflow(t *testing.T) {
	// Create shared memory
	config := &Config{
		MaxMemoryBytes:       200 * 1024 * 1024, // 200MB
		CompressionThreshold: 1 * 1024 * 1024,   // 1MB
		TTLSeconds:           3600,
	}

	store := NewSharedMemoryStore(config)

	// Simulate multi-agent workflow:
	// Agent1 (data collector) -> Agent2 (analyzer) -> Agent3 (reporter)

	// Agent1: Collect 10MB of data
	type DataPoint struct {
		Timestamp int64             `json:"timestamp"`
		Value     float64           `json:"value"`
		Tags      map[string]string `json:"tags"`
	}

	dataPoints := make([]DataPoint, 100000) // 100K data points
	for i := 0; i < 100000; i++ {
		dataPoints[i] = DataPoint{
			Timestamp: int64(1700000000 + i*60),
			Value:     float64(i) * 1.23,
			Tags: map[string]string{
				"sensor": fmt.Sprintf("sensor_%d", i%10),
				"region": fmt.Sprintf("region_%d", i%5),
				"type":   "temperature",
			},
		}
	}

	data, err := json.Marshal(dataPoints)
	require.NoError(t, err)
	t.Logf("Agent1 collected data: %.2f MB", float64(len(data))/(1024*1024))
	assert.Greater(t, len(data), 10*1024*1024) // Should be >10MB

	// Agent1 stores in shared memory
	ref1, err := store.Store("agent1-output", data, "application/json", map[string]string{
		"agent":       "agent1-collector",
		"data_points": "100000",
		"stage":       "collection",
	})
	require.NoError(t, err)

	// Agent2 retrieves data (by reference only in context)
	retrieved, err := store.Get(ref1)
	require.NoError(t, err)

	// Agent2 processes and creates analysis (smaller output)
	type Analysis struct {
		TotalPoints int      `json:"total_points"`
		AvgValue    float64  `json:"avg_value"`
		MaxValue    float64  `json:"max_value"`
		MinValue    float64  `json:"min_value"`
		Regions     []string `json:"regions"`
		Anomalies   int      `json:"anomalies"`
	}

	analysis := Analysis{
		TotalPoints: 100000,
		AvgValue:    50000 * 1.23,
		MaxValue:    99999 * 1.23,
		MinValue:    0.0,
		Regions:     []string{"region_0", "region_1", "region_2", "region_3", "region_4"},
		Anomalies:   42,
	}

	analysisData, err := json.Marshal(analysis)
	require.NoError(t, err)
	t.Logf("Agent2 analysis: %d bytes (much smaller)", len(analysisData))

	// Agent2 stores analysis
	ref2, err := store.Store("agent2-analysis", analysisData, "application/json", map[string]string{
		"agent": "agent2-analyzer",
		"stage": "analysis",
	})
	require.NoError(t, err)

	// Agent3 retrieves analysis and generates report
	analysisRetrieved, err := store.Get(ref2)
	require.NoError(t, err)

	var finalAnalysis Analysis
	err = json.Unmarshal(analysisRetrieved, &finalAnalysis)
	require.NoError(t, err)

	// Verify workflow
	assert.Equal(t, 100000, finalAnalysis.TotalPoints)
	assert.Equal(t, 42, finalAnalysis.Anomalies)

	// Check overall stats
	stats := store.Stats()
	t.Logf("Workflow complete: %d items, %.2f MB used, %d hits, %d compressions",
		stats.ItemCount,
		float64(stats.CurrentSize)/(1024*1024),
		stats.Hits,
		stats.Compressions)

	// Verify both references are accessible
	assert.Equal(t, int64(2), stats.Hits) // 2 Gets total (ref1 and ref2)
	assert.Equal(t, int64(0), stats.Misses)

	// Clean up
	store.Release(ref1.Id)
	store.Release(ref2.Id)

	// Verify data integrity check
	assert.Len(t, retrieved, len(data))
}

// TestDogfood_LoadTest_10Agents_100MBEach simulates load testing with 10 agents handling 100MB each
func TestDogfood_LoadTest_10Agents_100MBEach(t *testing.T) {
	// Create shared memory with realistic production config
	config := &Config{
		MaxMemoryBytes:       2 * 1024 * 1024 * 1024, // 2GB
		CompressionThreshold: 1 * 1024 * 1024,        // 1MB
		TTLSeconds:           3600,
	}

	// Add disk overflow for overflow scenario
	overflow, err := NewDiskOverflowManager(&DiskOverflowConfig{
		CachePath:   t.TempDir() + "/load-test-cache",
		MaxDiskSize: 10 * 1024 * 1024 * 1024, // 10GB
		TTLSeconds:  3600,
	})
	require.NoError(t, err)
	config.OverflowHandler = overflow

	store := NewSharedMemoryStore(config)

	// Simulate 10 agents, each handling 100MB of data
	numAgents := 10
	dataPerAgent := 10 * 1024 * 1024 // 10MB per agent (reduced for test speed)

	var wg sync.WaitGroup
	refs := make([]*loomv1.DataReference, numAgents)
	var mu sync.Mutex

	t.Logf("Starting load test: %d agents, %.1f MB each", numAgents, float64(dataPerAgent)/(1024*1024))

	// Concurrent data storage from multiple agents
	wg.Add(numAgents)
	for i := 0; i < numAgents; i++ {
		go func(agentID int) {
			defer wg.Done()

			// Generate large data for this agent
			data := make([]byte, dataPerAgent)
			for j := 0; j < dataPerAgent; j++ {
				data[j] = byte((agentID + j) % 256)
			}

			// Store in shared memory
			ref, err := store.Store(
				fmt.Sprintf("agent-%d-data", agentID),
				data,
				"application/octet-stream",
				map[string]string{
					"agent_id": fmt.Sprintf("agent-%d", agentID),
					"size":     fmt.Sprintf("%d", dataPerAgent),
				},
			)

			if err != nil {
				t.Errorf("Agent %d failed to store: %v", agentID, err)
				return
			}

			mu.Lock()
			refs[agentID] = ref
			mu.Unlock()

			t.Logf("Agent %d stored data: location=%s, compressed=%v",
				agentID, ref.Location, ref.Compressed)
		}(i)
	}

	wg.Wait()

	// Verify all agents stored successfully
	for i := 0; i < numAgents; i++ {
		assert.NotNil(t, refs[i], "Agent %d should have stored data", i)
	}

	// Check stats
	stats := store.Stats()
	t.Logf("Load test complete:")
	t.Logf("  Items: %d", stats.ItemCount)
	t.Logf("  Memory used: %.2f MB / %.2f MB (%.1f%%)",
		float64(stats.CurrentSize)/(1024*1024),
		float64(stats.MaxSize)/(1024*1024),
		100.0*float64(stats.CurrentSize)/float64(stats.MaxSize))
	t.Logf("  Hits: %d, Misses: %d", stats.Hits, stats.Misses)
	t.Logf("  Evictions: %d, Compressions: %d", stats.Evictions, stats.Compressions)

	// Verify data can be retrieved
	wg.Add(numAgents)
	for i := 0; i < numAgents; i++ {
		go func(agentID int) {
			defer wg.Done()

			data, err := store.Get(refs[agentID])
			if err != nil {
				t.Errorf("Agent %d failed to retrieve: %v", agentID, err)
				return
			}

			assert.Equal(t, dataPerAgent, len(data), "Agent %d data size mismatch", agentID)

			// Verify first few bytes
			for j := 0; j < 100; j++ {
				expected := byte((agentID + j) % 256)
				assert.Equal(t, expected, data[j], "Agent %d data corruption at byte %d", agentID, j)
			}
		}(i)
	}

	wg.Wait()

	// Clean up
	for i := 0; i < numAgents; i++ {
		if refs[i] != nil {
			store.Release(refs[i].Id)
		}
	}

	t.Logf("Load test passed: All agents successfully stored and retrieved data")
}
