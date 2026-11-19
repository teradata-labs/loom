package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/types"
)

// latencyPrompt is one fixed input size. Filler text is deterministic, so
// every arm and round sends identical bytes.
type latencyPrompt struct {
	Name       string
	ApproxToks int
}

var latencyPrompts = []latencyPrompt{
	{"p200", 200},
	{"p2000", 2000},
	{"p20000", 20000},
}

const latencyFiller = "The quarterly operations report covers shipping volumes, returns, and supplier delays across four regions. "

// latencySample is one timed call. It is appended to latency.jsonl and never rewritten.
type latencySample struct {
	Round      int     `json:"round"`
	Warmup     bool    `json:"warmup"`
	Arm        string  `json:"arm"`
	Model      string  `json:"model"`
	Prompt     string  `json:"prompt"`
	TTFTms     float64 `json:"ttft_ms"`
	TotalMS    float64 `json:"total_ms"`
	InputToks  int     `json:"input_tokens"`
	OutputToks int     `json:"output_tokens"`
	CostUSD    float64 `json:"cost_usd"`
	// Cache token counts explain cost differences between rounds: the
	// 2026-11-18 run could not show that later GPT rounds were cheaper because
	// their input was served from cache.
	CacheRead  int    `json:"cache_read_tokens"`
	CacheWrite int    `json:"cache_write_tokens"`
	StartedAt  string `json:"started_at"`
	Error      string `json:"error,omitempty"`
}

// runLatencyCmd times streamed calls per arm and prompt size. Arm order is
// shuffled per round with a fixed seed, so drift over the session is spread
// across arms.
func runLatencyCmd(args []string) error {
	fs := flag.NewFlagSet("latency", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	armList := fs.String("arm", "G-S,G-O", "comma-separated arm ids")
	rounds := fs.Int("rounds", 30, "measured rounds")
	warmup := fs.Int("warmup", 3, "discarded warm-up rounds")
	maxOut := fs.Int("max-output", 300, "max output tokens per call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	jsonlPath := filepath.Join(*out, "latency.jsonl")
	if _, err := os.Stat(jsonlPath); err == nil {
		return fmt.Errorf("refusing to overwrite %s", jsonlPath)
	}
	f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	defer func() { _ = w.Flush() }()

	var clients []struct {
		arm    arm
		client *vercelgateway.Client
	}
	for _, id := range strings.Split(*armList, ",") {
		a, ok := arms[strings.TrimSpace(id)]
		if !ok {
			return fmt.Errorf("unknown arm %q", id)
		}
		c, err := vercelgateway.NewClient(vercelgateway.Config{
			Model:             a.Slug,
			APIKey:            vercelgateway.ResolveAPIKey(""),
			MaxTokens:         *maxOut,
			RateLimiterConfig: rateLimiterFor(),
		})
		if err != nil {
			return fmt.Errorf("%s: %w", a.ID, err)
		}
		clients = append(clients, struct {
			arm    arm
			client *vercelgateway.Client
		}{a, c})
	}

	rng := rand.New(rand.NewSource(20261118))
	total := *warmup + *rounds
	var spent float64
	for r := 1; r <= total; r++ {
		order := rng.Perm(len(clients))
		for _, p := range latencyPrompts {
			for _, idx := range order {
				cl := clients[idx]
				s := timedCall(cl.client, cl.arm, p, r, r <= *warmup)
				spent += s.CostUSD
				b, err := json.Marshal(s)
				if err != nil {
					return err
				}
				if _, err := w.Write(append(b, '\n')); err != nil {
					return err
				}
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Printf("round %d/%d done, spent $%.4f\n", r, total, spent)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println("wrote", jsonlPath)
	return nil
}

// timedCall sends one streamed request and records time to first token and
// total time. The first token callback fixes TTFT.
func timedCall(client *vercelgateway.Client, a arm, p latencyPrompt, round int, warm bool) latencySample {
	s := latencySample{Round: round, Warmup: warm, Arm: a.ID, Model: a.Slug, Prompt: p.Name}
	msgs := []types.Message{{
		Role:    "user",
		Content: "Summarize the following text in three sentences.\n\n" + fillerOf(p.ApproxToks),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var first time.Time
	start := time.Now()
	s.StartedAt = start.UTC().Format(time.RFC3339Nano)
	resp, err := client.ChatStream(ctx, msgs, nil, func(token string) {
		if first.IsZero() && token != "" {
			first = time.Now()
		}
	})
	end := time.Now()
	s.TotalMS = float64(end.Sub(start).Microseconds()) / 1000
	if !first.IsZero() {
		s.TTFTms = float64(first.Sub(start).Microseconds()) / 1000
	}
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.InputToks = resp.Usage.InputTokens
	s.OutputToks = resp.Usage.OutputTokens
	s.CostUSD = resp.Usage.CostUSD
	s.CacheRead = resp.Usage.CacheReadInputTokens
	s.CacheWrite = resp.Usage.CacheCreationInputTokens
	return s
}

// fillerOf returns about approxToks tokens of filler. A token is taken as
// four characters, which is only used to size the prompts; the measured token
// counts come from the response.
func fillerOf(approxToks int) string {
	want := approxToks * 4
	var b strings.Builder
	for b.Len() < want {
		b.WriteString(latencyFiller)
	}
	return b.String()[:want]
}

// latencySummary is the per-arm, per-prompt statistic reported.
type latencySummary struct {
	Arm         string  `json:"arm"`
	Prompt      string  `json:"prompt"`
	N           int     `json:"n"`
	Errors      int     `json:"errors"`
	TTFTMedian  float64 `json:"ttft_ms_median"`
	TTFTp95     float64 `json:"ttft_ms_p95"`
	TTFTCIlo    float64 `json:"ttft_ms_median_ci_lo"`
	TTFTCIhi    float64 `json:"ttft_ms_median_ci_hi"`
	TotalMedian float64 `json:"total_ms_median"`
	TotalP95    float64 `json:"total_ms_p95"`
	TokPerSec   float64 `json:"output_tok_per_sec_median"`
}

// runLatencySummaryCmd reads latency.jsonl and writes a new summary file.
func runLatencySummaryCmd(args []string) error {
	fs := flag.NewFlagSet("latency-summary", flag.ContinueOnError)
	out := fs.String("out", "", "results directory holding latency.jsonl (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	samples, err := readSamples(filepath.Join(*out, "latency.jsonl"))
	if err != nil {
		return err
	}

	type key struct{ arm, prompt string }
	groups := map[key][]latencySample{}
	for _, s := range samples {
		if s.Warmup {
			continue
		}
		groups[key{s.Arm, s.Prompt}] = append(groups[key{s.Arm, s.Prompt}], s)
	}
	var keys []key
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].prompt != keys[j].prompt {
			return keys[i].prompt < keys[j].prompt
		}
		return keys[i].arm < keys[j].arm
	})

	var sums []latencySummary
	fmt.Printf("%-5s %-8s %-4s %-6s %-22s %-10s %-10s %-12s\n", "arm", "prompt", "n", "errs", "ttft median [95% CI]", "ttft p95", "total p95", "tok/s median")
	for _, k := range keys {
		g := groups[k]
		var ok []latencySample
		errs := 0
		for _, s := range g {
			if s.Error != "" {
				errs++
				continue
			}
			ok = append(ok, s)
		}
		ttft := make([]float64, 0, len(ok))
		total := make([]float64, 0, len(ok))
		rate := make([]float64, 0, len(ok))
		for _, s := range ok {
			ttft = append(ttft, s.TTFTms)
			total = append(total, s.TotalMS)
			if d := s.TotalMS - s.TTFTms; d > 0 {
				rate = append(rate, float64(s.OutputToks)/(d/1000))
			}
		}
		sum := latencySummary{Arm: k.arm, Prompt: k.prompt, N: len(g), Errors: errs}
		if len(ttft) > 0 {
			sum.TTFTMedian = percentile(ttft, 0.5)
			sum.TTFTp95 = percentile(ttft, 0.95)
			sum.TTFTCIlo, sum.TTFTCIhi = bootstrapMedianCI(ttft, 2000, 42)
			sum.TotalMedian = percentile(total, 0.5)
			sum.TotalP95 = percentile(total, 0.95)
			sum.TokPerSec = percentile(rate, 0.5)
		}
		sums = append(sums, sum)
		fmt.Printf("%-5s %-8s %-4d %-6d %8.0f [%.0f, %.0f]   %-10.0f %-10.0f %-12.1f\n",
			sum.Arm, sum.Prompt, sum.N, sum.Errors, sum.TTFTMedian, sum.TTFTCIlo, sum.TTFTCIhi,
			sum.TTFTp95, sum.TotalP95, sum.TokPerSec)
	}

	path := filepath.Join(*out, fmt.Sprintf("latency-summary-%s.json", time.Now().UTC().Format("20060102T150405Z")))
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite %s", path)
	}
	b, err := json.MarshalIndent(sums, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func readSamples(path string) ([]latencySample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []latencySample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var s latencySample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// percentile returns the p-quantile of xs (linear interpolation). xs is not modified.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	pos := p * float64(len(s)-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= len(s) {
		return s[len(s)-1]
	}
	frac := pos - float64(lo)
	return s[lo]*(1-frac) + s[hi]*frac
}

// bootstrapMedianCI returns a 95% percentile bootstrap interval for the
// median, with a fixed seed so the interval is reproducible.
func bootstrapMedianCI(xs []float64, iters int, seed int64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewSource(seed))
	meds := make([]float64, iters)
	buf := make([]float64, len(xs))
	for i := 0; i < iters; i++ {
		for j := range buf {
			buf[j] = xs[rng.Intn(len(xs))]
		}
		meds[i] = percentile(buf, 0.5)
	}
	return percentile(meds, 0.025), percentile(meds, 0.975)
}
