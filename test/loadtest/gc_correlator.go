package loadtest

import (
	"runtime"
	"sort"
	"time"
)

// GCCorrelation holds the result of correlating GC pauses with tail latency.
type GCCorrelation struct {
	TotalP95Requests     int     `json:"total_p95_requests"`
	GCAttributedRequests int     `json:"gc_attributed_requests"`
	GCAttributionPct     float64 `json:"gc_attribution_pct"`
	TotalGCPauses        int     `json:"total_gc_pauses"`
	MaxGCPauseUs         int64   `json:"max_gc_pause_us"`
	AvgGCPauseUs         float64 `json:"avg_gc_pause_us"`
	TotalGCPauseUs       int64   `json:"total_gc_pause_us"`
}

// CorrelateGCWithLatency checks whether requests with latency > P95 overlap
// with a GC pause window. This answers "what causes the tail?"
//
// It compares per-request timestamps and latencies against the GC pause log
// from runtime.MemStats. The GC pause ring buffer holds the most recent 256 pauses.
func CorrelateGCWithLatency(results []result, runStart time.Time) GCCorrelation {
	if len(results) == 0 {
		return GCCorrelation{}
	}

	// Capture GC pause data
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	// Build GC pause windows from the ring buffer
	// PauseEnd contains the end timestamps, PauseNs contains durations
	numPauses := int(mem.NumGC)
	if numPauses > 256 {
		numPauses = 256 // ring buffer size
	}

	type gcPause struct {
		startNs int64
		endNs   int64
	}
	pauses := make([]gcPause, 0, numPauses)
	for i := range numPauses {
		idx := (int(mem.NumGC) - numPauses + i) % 256
		endNs := int64(mem.PauseEnd[idx])
		durationNs := int64(mem.PauseNs[idx])
		if endNs > 0 && durationNs > 0 {
			pauses = append(pauses, gcPause{
				startNs: endNs - durationNs,
				endNs:   endNs,
			})
		}
	}

	if len(pauses) == 0 {
		return GCCorrelation{}
	}

	// Compute P95 latency threshold
	latencies := make([]time.Duration, len(results))
	for i, r := range results {
		latencies[i] = r.latency
	}
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})
	p95Threshold := percentile(latencies, 0.95)

	// Find requests above P95
	var totalP95 int
	var gcAttributed int

	for _, r := range results {
		if r.latency < p95Threshold {
			continue
		}
		totalP95++

		// Check if this request's execution window overlaps with any GC pause
		reqStartNs := r.startedAt.UnixNano()
		reqEndNs := reqStartNs + r.latency.Nanoseconds()

		for _, p := range pauses {
			// Overlap check: request [reqStart, reqEnd] intersects GC [gcStart, gcEnd]
			if reqStartNs < p.endNs && reqEndNs > p.startNs {
				gcAttributed++
				break
			}
		}
	}

	// Compute pause statistics
	var totalPauseNs int64
	var maxPauseNs int64
	for _, p := range pauses {
		dur := p.endNs - p.startNs
		totalPauseNs += dur
		if dur > maxPauseNs {
			maxPauseNs = dur
		}
	}

	var pct float64
	if totalP95 > 0 {
		pct = float64(gcAttributed) / float64(totalP95) * 100
	}

	return GCCorrelation{
		TotalP95Requests:     totalP95,
		GCAttributedRequests: gcAttributed,
		GCAttributionPct:     pct,
		TotalGCPauses:        len(pauses),
		MaxGCPauseUs:         maxPauseNs / 1000,
		AvgGCPauseUs:         float64(totalPauseNs) / float64(len(pauses)) / 1000,
		TotalGCPauseUs:       totalPauseNs / 1000,
	}
}
