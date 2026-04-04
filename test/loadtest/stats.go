package loadtest

import (
	"math"
	"sort"
)

// Stats holds aggregate statistics computed from multiple benchmark runs.
type Stats struct {
	Median  float64 `json:"median"`
	Mean    float64 `json:"mean"`
	StdDev  float64 `json:"stddev"`
	CI95Low float64 `json:"ci95_low"`
	CI95Hi  float64 `json:"ci95_high"`
	CV      float64 `json:"cv_pct"` // coefficient of variation as percentage
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
}

// ComputeStats computes aggregate statistics from a slice of values.
// Returns zero-value Stats if the input is empty.
func ComputeStats(values []float64) Stats {
	n := len(values)
	if n == 0 {
		return Stats{}
	}

	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)

	s := Stats{
		Min: sorted[0],
		Max: sorted[n-1],
	}

	// Mean
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	s.Mean = sum / float64(n)

	// Median
	if n%2 == 0 {
		s.Median = (sorted[n/2-1] + sorted[n/2]) / 2
	} else {
		s.Median = sorted[n/2]
	}

	// Standard deviation (sample, not population — use n-1)
	if n > 1 {
		var sumSqDiff float64
		for _, v := range sorted {
			diff := v - s.Mean
			sumSqDiff += diff * diff
		}
		s.StdDev = math.Sqrt(sumSqDiff / float64(n-1))
	}

	// 95% confidence interval: mean ± 1.96 * (stddev / sqrt(N))
	if n > 1 {
		marginOfError := 1.96 * (s.StdDev / math.Sqrt(float64(n)))
		s.CI95Low = s.Mean - marginOfError
		s.CI95Hi = s.Mean + marginOfError
	} else {
		s.CI95Low = s.Mean
		s.CI95Hi = s.Mean
	}

	// Coefficient of variation
	if s.Mean != 0 {
		s.CV = (s.StdDev / s.Mean) * 100
	}

	return s
}
