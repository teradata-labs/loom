package loadtest

import (
	"sort"
	"time"
)

// ThroughputBucket holds throughput data for one second of a benchmark run.
type ThroughputBucket struct {
	Second   int     `json:"second"`
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	P50Us    int64   `json:"p50_us"`
	P99Us    int64   `json:"p99_us"`
	MeanUs   float64 `json:"mean_us"`
}

// BuildTimeSeries bins raw results into 1-second throughput buckets.
// runStart is the time the run began; results must have startedAt set.
func BuildTimeSeries(results []result, runStart time.Time) []ThroughputBucket {
	if len(results) == 0 {
		return nil
	}

	// Find the max second offset
	var maxSecond int
	for _, r := range results {
		sec := int(r.startedAt.Sub(runStart).Seconds())
		if sec > maxSecond {
			maxSecond = sec
		}
	}

	// Bin results by second
	type bin struct {
		latencies []time.Duration
		errors    int
	}
	bins := make([]bin, maxSecond+1)
	for i := range bins {
		bins[i].latencies = make([]time.Duration, 0, 64)
	}

	for _, r := range results {
		sec := int(r.startedAt.Sub(runStart).Seconds())
		if sec < 0 {
			sec = 0
		}
		if sec > maxSecond {
			sec = maxSecond
		}
		bins[sec].latencies = append(bins[sec].latencies, r.latency)
		if r.err != nil {
			bins[sec].errors++
		}
	}

	// Build buckets
	buckets := make([]ThroughputBucket, 0, len(bins))
	for i, b := range bins {
		if len(b.latencies) == 0 {
			buckets = append(buckets, ThroughputBucket{Second: i})
			continue
		}

		sort.Slice(b.latencies, func(a, c int) bool {
			return b.latencies[a] < b.latencies[c]
		})

		var totalNs int64
		for _, l := range b.latencies {
			totalNs += l.Nanoseconds()
		}

		buckets = append(buckets, ThroughputBucket{
			Second:   i,
			Requests: len(b.latencies),
			Errors:   b.errors,
			P50Us:    percentile(b.latencies, 0.50).Microseconds(),
			P99Us:    percentile(b.latencies, 0.99).Microseconds(),
			MeanUs:   float64(totalNs) / float64(len(b.latencies)) / 1000,
		})
	}

	return buckets
}

// BuildLatencyHistogram returns the raw latency values in microseconds.
// Downstream consumers can bucket these however they want.
func BuildLatencyHistogram(results []result) []int64 {
	latencies := make([]int64, 0, len(results))
	for _, r := range results {
		latencies = append(latencies, r.latency.Microseconds())
	}
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})
	return latencies
}
