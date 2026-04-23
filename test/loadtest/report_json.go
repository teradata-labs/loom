package loadtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// JSONReport is the top-level output structure matching the spec's Section 6 schema.
type JSONReport struct {
	BenchmarkVersion string          `json:"benchmark_version"`
	LoomVersion      string          `json:"loom_version"`
	Environment      EnvironmentInfo `json:"environment"`
	Scenario         string          `json:"scenario"`
	Config           JSONConfig      `json:"config"`
	WarmupRuns       int             `json:"warmup_runs"`
	MeasuredRuns     int             `json:"measured_runs"`
	Runs             []JSONRunResult `json:"runs"`
	Aggregate        JSONAggregate   `json:"aggregate"`
}

// JSONConfig is a JSON-safe subset of the harness configuration.
// It excludes function pointers and non-serializable fields.
type JSONConfig struct {
	Concurrency         int     `json:"concurrency"`
	TotalRequests       int     `json:"total_requests,omitempty"`
	DurationMs          int64   `json:"duration_ms,omitempty"`
	LLMBaseLatencyMs    int64   `json:"llm_base_latency_ms"`
	LLMJitterMs         int64   `json:"llm_jitter_ms"`
	LLMErrorRate        float64 `json:"llm_error_rate"`
	LLMConcurrencyLimit int     `json:"llm_concurrency_limit"`
	UseStreaming        bool    `json:"use_streaming"`
	NumAgents           int     `json:"num_agents"`
}

// JSONRunResult holds the data for a single benchmark run.
type JSONRunResult struct {
	RunNumber          int                `json:"run_number"`
	ThroughputRPS      float64            `json:"throughput_rps"`
	LatencyP50Us       int64              `json:"latency_p50_us"`
	LatencyP90Us       int64              `json:"latency_p90_us"`
	LatencyP95Us       int64              `json:"latency_p95_us"`
	LatencyP99Us       int64              `json:"latency_p99_us"`
	LatencyMinUs       int64              `json:"latency_min_us"`
	LatencyMaxUs       int64              `json:"latency_max_us"`
	LatencyAvgUs       float64            `json:"latency_avg_us"`
	ErrorCount         int64              `json:"error_count"`
	ErrorRate          float64            `json:"error_rate"`
	TotalRequests      int64              `json:"total_requests"`
	WallTimeMs         int64              `json:"wall_time_ms"`
	ThroughputTimeline []ThroughputBucket `json:"throughput_timeline,omitempty"`
	LatencyHistogram   []int64            `json:"latency_histogram,omitempty"`
	ResourceTimeline   []ResourceSample   `json:"resource_timeline,omitempty"`
	GCCorrelation      *GCCorrelation     `json:"gc_correlation,omitempty"`
}

// JSONAggregate holds aggregate statistics across all runs.
type JSONAggregate struct {
	ThroughputRPS Stats `json:"throughput_rps"`
	LatencyP50Us  Stats `json:"latency_p50_us"`
	LatencyP90Us  Stats `json:"latency_p90_us"`
	LatencyP95Us  Stats `json:"latency_p95_us"`
	LatencyP99Us  Stats `json:"latency_p99_us"`
}

// RunResult holds all data collected from a single benchmark run.
type RunResult struct {
	Report             *Report
	RawResults         []result
	RunStart           time.Time
	ThroughputTimeline []ThroughputBucket
	LatencyHistogram   []int64
	ResourceTimeline   []ResourceSample
	GCCorrelation      *GCCorrelation
}

// BenchmarkReport holds the complete benchmark output.
type BenchmarkReport struct {
	Environment   EnvironmentInfo
	ScenarioName  string
	HarnessConfig HarnessConfig
	WarmupRuns    int
	Runs          []RunResult
	Aggregate     JSONAggregate
}

// ToJSON converts a BenchmarkReport to the JSON output format.
func (br *BenchmarkReport) ToJSON() *JSONReport {
	cfg := br.HarnessConfig
	jr := &JSONReport{
		BenchmarkVersion: "1.0.0",
		LoomVersion:      getLoomVersion(),
		Environment:      br.Environment,
		Scenario:         br.ScenarioName,
		Config: JSONConfig{
			Concurrency:         cfg.Concurrency,
			TotalRequests:       cfg.TotalRequests,
			DurationMs:          cfg.Duration.Milliseconds(),
			LLMBaseLatencyMs:    cfg.LLMConfig.BaseLatency.Milliseconds(),
			LLMJitterMs:         cfg.LLMConfig.LatencyJitter.Milliseconds(),
			LLMErrorRate:        cfg.LLMConfig.ErrorRate,
			LLMConcurrencyLimit: cfg.LLMConcurrencyLimit,
			UseStreaming:        cfg.UseStreaming,
			NumAgents:           cfg.NumAgents,
		},
		WarmupRuns:   br.WarmupRuns,
		MeasuredRuns: len(br.Runs),
		Aggregate:    br.Aggregate,
	}

	for i, run := range br.Runs {
		r := run.Report
		jr.Runs = append(jr.Runs, JSONRunResult{
			RunNumber:          i + 1,
			ThroughputRPS:      r.RequestsPerSecond,
			LatencyP50Us:       r.P50.Microseconds(),
			LatencyP90Us:       r.P90.Microseconds(),
			LatencyP95Us:       r.P95.Microseconds(),
			LatencyP99Us:       r.P99.Microseconds(),
			LatencyMinUs:       r.Min.Microseconds(),
			LatencyMaxUs:       r.Max.Microseconds(),
			LatencyAvgUs:       float64(r.Avg.Microseconds()),
			ErrorCount:         r.Errors,
			ErrorRate:          r.ErrorRate,
			TotalRequests:      r.RequestsCompleted,
			WallTimeMs:         r.WallTime.Milliseconds(),
			ThroughputTimeline: run.ThroughputTimeline,
			LatencyHistogram:   run.LatencyHistogram,
			ResourceTimeline:   run.ResourceTimeline,
			GCCorrelation:      run.GCCorrelation,
		})
	}

	return jr
}

// WriteJSON writes the benchmark report to a JSON file.
func WriteJSON(path string, report *BenchmarkReport) error {
	jr := report.ToJSON()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	data, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

func getLoomVersion() string {
	if data, err := os.ReadFile("VERSION"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "unknown"
}
