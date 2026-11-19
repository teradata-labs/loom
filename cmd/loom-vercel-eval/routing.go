package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/types"
)

// routingCase is one routing probe. Model is the requested model; Routing is
// what loom sends in providerOptions.gateway.
type routingCase struct {
	ID      string
	Model   string
	Routing *vercelgateway.Routing
	Expect  string // what the plan predicts; recorded, not asserted
	Repeats int
}

var routingCases = []routingCase{
	{ID: "R1-only-openai", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Only: []string{"openai"}},
		Expect: "succeeds; served provider is openai", Repeats: 1},
	{ID: "R1-only-anthropic-on-openai", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Only: []string{"anthropic"}},
		Expect: "rejected (no anthropic upstream for an openai model)", Repeats: 1},
	{ID: "R2-only-unsatisfiable", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Only: []string{"no-such-provider"}},
		Expect: "rejected, deterministic error, no silent retry", Repeats: 1},
	{ID: "R3-order-azure-then-openai", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Order: []string{"azure", "openai"}},
		Expect: "succeeds; served provider reported in metadata", Repeats: 1},
	{ID: "R4-sort-ttft", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Sort: vercelgateway.SortTTFT},
		Expect: "served provider; compare with sort cost over repeats", Repeats: 10},
	{ID: "R4-sort-cost", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{Sort: vercelgateway.SortCost},
		Expect: "served provider; compare with sort ttft over repeats", Repeats: 10},
	{ID: "R5-fallback-primary-invalid", Model: "openai/no-such-model-eval", Routing: &vercelgateway.Routing{FallbackModels: []string{"openai/gpt-5.5"}},
		Expect: "gateway.fallback_used true; served model gpt-5.5", Repeats: 1},
	{ID: "R6-zero-data-retention", Model: "openai/gpt-5.5", Routing: &vercelgateway.Routing{ZeroDataRetention: true},
		Expect: "accepted; served providers report ZDR (metadata may not say)", Repeats: 1},
	{ID: "R6-zdr-anthropic", Model: "anthropic/claude-sonnet-5.5", Routing: &vercelgateway.Routing{ZeroDataRetention: true},
		Expect: "accepted", Repeats: 1},
}

// routingObservation is the raw outcome of one probe call.
type routingObservation struct {
	CaseID     string            `json:"case_id"`
	Repeat     int               `json:"repeat"`
	Model      string            `json:"requested_model"`
	Expect     string            `json:"expect"`
	Error      string            `json:"error,omitempty"`
	Gateway    map[string]string `json:"gateway_metadata"`
	CostUSD    float64           `json:"cost_usd"`
	OutputText string            `json:"output_text,omitempty"`
	StartedAt  string            `json:"started_at"`
}

// runRoutingCmd runs each routing case and writes every raw observation.
func runRoutingCmd(args []string) error {
	fs := flag.NewFlagSet("routing", flag.ContinueOnError)
	out := fs.String("out", "", "results directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	path := filepath.Join(*out, "routing.json")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite %s", path)
	}

	var obs []routingObservation
	for _, c := range routingCases {
		for rep := 1; rep <= c.Repeats; rep++ {
			o := routingCall(c, rep)
			obs = append(obs, o)
			status := "ok"
			if o.Error != "" {
				status = "ERR " + o.Error
			}
			fmt.Printf("%-30s rep%-2d %s served=%q\n", c.ID, rep, status, o.Gateway["gateway.served_provider"])
		}
	}
	b, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// routingCall sends one probe. The gateway's own metadata is copied as-is,
// keeping every gateway.* key, so the served provider is read from the
// response rather than assumed.
func routingCall(c routingCase, rep int) routingObservation {
	o := routingObservation{CaseID: c.ID, Repeat: rep, Model: c.Model, Expect: c.Expect, Gateway: map[string]string{}}
	o.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)

	client, err := vercelgateway.NewClient(vercelgateway.Config{
		Model:             c.Model,
		APIKey:            vercelgateway.ResolveAPIKey(""),
		MaxTokens:         64,
		Routing:           c.Routing,
		RateLimiterConfig: rateLimiterFor(),
	})
	if err != nil {
		o.Error = "client: " + err.Error()
		return o
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := client.Chat(ctx, []types.Message{{Role: "user", Content: "Reply with OK."}}, nil)
	if err != nil {
		o.Error = err.Error()
		return o
	}
	o.CostUSD = resp.Usage.CostUSD
	o.OutputText = resp.Content
	for k, v := range resp.Metadata {
		if len(k) >= len("gateway.") && k[:len("gateway.")] == "gateway." {
			o.Gateway[k] = fmt.Sprint(v)
		}
	}
	return o
}
