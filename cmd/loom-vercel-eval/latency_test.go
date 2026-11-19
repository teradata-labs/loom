package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPercentileInterpolates(t *testing.T) {
	xs := []float64{4, 1, 3, 2}
	assert.InDelta(t, 2.5, percentile(xs, 0.5), 1e-9)
	assert.InDelta(t, 1, percentile(xs, 0), 1e-9)
	assert.InDelta(t, 4, percentile(xs, 1), 1e-9)
	assert.InDelta(t, 3.85, percentile(xs, 0.95), 1e-9)
	// The input must not be reordered in place.
	assert.Equal(t, []float64{4, 1, 3, 2}, xs)
}

func TestPercentileEmpty(t *testing.T) {
	assert.Equal(t, float64(0), percentile(nil, 0.5))
}

func TestBootstrapCIContainsMedianAndIsReproducible(t *testing.T) {
	xs := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	lo, hi := bootstrapMedianCI(xs, 2000, 42)
	med := percentile(xs, 0.5)
	assert.True(t, lo <= med && med <= hi, "CI [%v, %v] should contain median %v", lo, hi, med)
	lo2, hi2 := bootstrapMedianCI(xs, 2000, 42)
	assert.Equal(t, lo, lo2)
	assert.Equal(t, hi, hi2)
}

func TestFillerHasRequestedLength(t *testing.T) {
	for _, n := range []int{200, 2000, 20000} {
		assert.Equal(t, n*4, len(fillerOf(n)), "filler for %d tokens", n)
	}
}
