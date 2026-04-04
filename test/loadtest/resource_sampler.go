package loadtest

import (
	"runtime"
	"sync"
	"time"
)

// ResourceSample holds a single point-in-time snapshot of runtime metrics.
type ResourceSample struct {
	Second       int     `json:"second"`
	Goroutines   int     `json:"goroutines"`
	HeapAllocMB  float64 `json:"heap_alloc_mb"`
	HeapSysMB    float64 `json:"heap_sys_mb"`
	NumGC        uint32  `json:"num_gc"`
	GCPauseTotUs int64   `json:"gc_pause_total_us"`
}

// ResourceSampler collects runtime metrics at 1-second intervals.
type ResourceSampler struct {
	mu      sync.Mutex
	samples []ResourceSample
	stopCh  chan struct{}
	wg      sync.WaitGroup
	start   time.Time
}

// NewResourceSampler creates a sampler. Call Start() to begin collection.
func NewResourceSampler() *ResourceSampler {
	return &ResourceSampler{
		samples: make([]ResourceSample, 0, 128),
		stopCh:  make(chan struct{}),
	}
}

// Start begins collecting samples every second.
func (s *ResourceSampler) Start() {
	s.start = time.Now()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		// Take an immediate sample at t=0
		s.takeSample()

		for {
			select {
			case <-ticker.C:
				s.takeSample()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop ends collection and returns all samples.
func (s *ResourceSampler) Stop() []ResourceSample {
	close(s.stopCh)
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ResourceSample, len(s.samples))
	copy(result, s.samples)
	return result
}

func (s *ResourceSampler) takeSample() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	sample := ResourceSample{
		Second:       int(time.Since(s.start).Seconds()),
		Goroutines:   runtime.NumGoroutine(),
		HeapAllocMB:  float64(mem.HeapAlloc) / (1024 * 1024),
		HeapSysMB:    float64(mem.HeapSys) / (1024 * 1024),
		NumGC:        mem.NumGC,
		GCPauseTotUs: int64(mem.PauseTotalNs) / 1000,
	}

	s.mu.Lock()
	s.samples = append(s.samples, sample)
	s.mu.Unlock()
}
