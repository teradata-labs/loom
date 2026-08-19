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

// loom-mcp-probe connects Loom's MCP client to a real MCP server — streamable
// HTTP or stdio — and reports what actually negotiated: protocol revision,
// era (stateless 2026-07-28 core vs legacy initialize handshake), server
// identity, the tool list, an optional tool call, and an optional
// subscriptions/listen watch. No fakes anywhere: it exercises the same
// negotiation, fallback, and transport paths the manager uses in production,
// against whatever server it is pointed at.
//
// Examples:
//
//	loom-mcp-probe -url http://localhost:8971 -call test_simple_text -watch 5
//	loom-mcp-probe -cmd "npx -y @modelcontextprotocol/server-everything stdio" \
//	    -call echo -args '{"message":"hello"}'
//	loom-mcp-probe -url http://localhost:9090/mcp -pin legacy
//	loom-mcp-probe -url http://localhost:8971 -call test_elicitation \
//	    -args '{"message":"pick a username"}' -answer '{"username":"loom"}'
//
// With -answer set, the probe drives Multi Round-Trip Requests (MRTR,
// 2026-07-28): a server's input_required interim result is answered by
// accepting every elicitation with the -answer object, and the original
// call is retried — the same loop the manager's HITL adapter drives in
// production, with a canned human.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/teradata-labs/loom/internal/version"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// options selects the server to probe and what to exercise against it.
type options struct {
	URL      string        // streamable HTTP endpoint; mutually exclusive with Cmd
	Cmd      string        // stdio server command line; mutually exclusive with URL
	Pin      string        // protocol_version pin: "" or "auto", "legacy", or an exact revision
	Call     string        // tool to invoke, if any
	Args     string        // JSON arguments for Call
	Answer   string        // JSON object accepted for every elicitation (enables the MRTR driver)
	WatchSec int           // seconds to hold a subscriptions/listen stream (stateless only)
	Timeout  time.Duration // negotiation-probe/request timeout
	Verbose  bool
}

// report is what the probe observed on the wire.
type report struct {
	ConnectDuration time.Duration
	Negotiated      string
	Stateless       bool
	ServerInfo      protocol.Implementation
	ToolCount       int
	ToolNames       []string // first few, for display
	CallOutput      string   // first content text of the tool call, if Call was set
	Elicited        []string // elicitation messages answered by the MRTR driver
	Notifications   []string // methods received while watching, if WatchSec > 0
	WatchEndErr     error    // nil = graceful or client-cancelled
	WatchSkipped    bool     // legacy connection cannot subscribe
}

const toolNamesShown = 6

// printer writes best-effort human-readable progress: a failed write to the
// progress sink must not fail the probe itself.
type printer struct{ w io.Writer }

func (p printer) f(format string, a ...interface{}) {
	_, _ = fmt.Fprintf(p.w, format, a...)
}

func main() {
	opts := options{}
	var timeoutMs int
	flag.StringVar(&opts.URL, "url", "", "streamable HTTP endpoint (e.g. http://localhost:8971)")
	flag.StringVar(&opts.Cmd, "cmd", "", "stdio server command line (space-separated)")
	flag.StringVar(&opts.Pin, "pin", "", `protocol_version pin: "auto" (default), "legacy", or an exact revision`)
	flag.StringVar(&opts.Call, "call", "", "tool to invoke")
	flag.StringVar(&opts.Args, "args", "{}", "JSON arguments for -call")
	flag.StringVar(&opts.Answer, "answer", "", "JSON object accepted for every elicitation; enables the MRTR driver (default: fail fast on input_required)")
	flag.IntVar(&opts.WatchSec, "watch", 0, "seconds to hold a subscriptions/listen stream (stateless connections only)")
	flag.IntVar(&timeoutMs, "timeout", 15000, "negotiation-probe/request timeout in ms")
	flag.BoolVar(&opts.Verbose, "v", false, "debug logging")
	flag.Parse()
	opts.Timeout = time.Duration(timeoutMs) * time.Millisecond

	if (opts.URL == "") == (opts.Cmd == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -url or -cmd is required")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rep, err := run(ctx, opts, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PROBE FAILED: %v\n", err)
		os.Exit(1)
	}
	_ = rep
}

// run performs the probe and streams human-readable progress to out. The
// returned report carries the same observations for programmatic use.
func run(ctx context.Context, opts options, out io.Writer) (*report, error) {
	pr := printer{w: out}
	logger, err := buildLogger(opts.Verbose)
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}

	tr, err := buildTransport(opts, logger)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}

	rep := &report{}

	cfg := client.Config{
		Transport:       tr,
		Logger:          logger,
		ProtocolVersion: opts.Pin,
		RequestTimeout:  opts.Timeout,
	}
	if opts.Answer != "" {
		mrtr, err := buildMRTRHandler(opts.Answer, rep, pr)
		if err != nil {
			return nil, err
		}
		cfg.MRTR = client.MRTRConfig{Handler: mrtr}
	}

	c := client.NewClient(cfg)
	defer func() { _ = c.Close() }()

	start := time.Now()
	if err := c.Connect(ctx, protocol.Implementation{Name: "loom-mcp-probe", Version: version.Version}); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	rep.ConnectDuration = time.Since(start)
	rep.Negotiated = c.NegotiatedVersion()
	rep.Stateless = c.IsStateless()
	rep.ServerInfo = c.ServerInfo()

	pr.f("CONNECTED in %s\n", rep.ConnectDuration.Round(time.Millisecond))
	pr.f("  negotiated : %s\n", rep.Negotiated)
	pr.f("  era        : %s\n", era(rep.Stateless))
	pr.f("  serverInfo : %s %s\n", rep.ServerInfo.Name, rep.ServerInfo.Version)

	tools, err := c.ListTools(ctx)
	if err != nil {
		return rep, fmt.Errorf("tools/list: %w", err)
	}
	rep.ToolCount = len(tools)
	for i, t := range tools {
		if i == toolNamesShown {
			break
		}
		rep.ToolNames = append(rep.ToolNames, t.Name)
	}
	pr.f("  tools      : %d (%s%s)\n",
		rep.ToolCount, strings.Join(rep.ToolNames, ", "), more(rep.ToolCount, toolNamesShown))

	if opts.Call != "" {
		if err := runCall(ctx, c, opts, rep, pr); err != nil {
			return rep, err
		}
	}

	if opts.WatchSec > 0 {
		if err := runWatch(ctx, c, opts, rep, pr); err != nil {
			return rep, err
		}
	}

	return rep, nil
}

func runCall(ctx context.Context, c *client.Client, opts options, rep *report, pr printer) error {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(opts.Args), &args); err != nil {
		return fmt.Errorf("-args is not a JSON object: %w", err)
	}
	res, err := c.CallTool(ctx, opts.Call, args)
	if err != nil {
		return fmt.Errorf("tools/call %s: %w", opts.Call, err)
	}
	if r, ok := res.(*protocol.CallToolResult); ok && len(r.Content) > 0 {
		rep.CallOutput = r.Content[0].Text
	}
	display := rep.CallOutput
	if len(display) > 120 {
		display = display[:120] + "…"
	}
	pr.f("  call %s → %q\n", opts.Call, display)
	return nil
}

func runWatch(ctx context.Context, c *client.Client, opts options, rep *report, pr printer) error {
	if !c.IsStateless() {
		rep.WatchSkipped = true
		pr.f("  watch      : skipped (legacy connection; subscriptions/listen is 2026-07-28)\n")
		return nil
	}
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	sub, err := c.Subscribe(subCtx, protocol.NotificationFilter{
		ToolsListChanged:     true,
		ResourcesListChanged: true,
	})
	if err != nil {
		return fmt.Errorf("subscriptions/listen: %w", err)
	}
	pr.f("  watch      : subscription %s open for %ds\n", sub.ID, opts.WatchSec)

	deadline := time.After(time.Duration(opts.WatchSec) * time.Second)
	for {
		select {
		case n, ok := <-sub.Notifications:
			if !ok {
				rep.WatchEndErr = sub.Err()
				pr.f("  watch done : %d notifications, end state: %v\n",
					len(rep.Notifications), endState(rep.WatchEndErr))
				return nil
			}
			rep.Notifications = append(rep.Notifications, n.Method)
			pr.f("    notif #%d : %s\n", len(rep.Notifications), n.Method)
		case <-deadline:
			subCancel()
			<-sub.Done()
			// Client-initiated cancellation ends the watch; the context
			// cancellation recorded on the subscription is expected, not an
			// abnormal end.
			pr.f("  watch done : %d notifications, end state: client cancelled\n",
				len(rep.Notifications))
			return nil
		}
	}
}

// buildMRTRHandler answers every elicitation in a server's input_required
// result by accepting it with the -answer object — a canned human standing in
// for the manager's HITL adapter. Non-elicitation input requests (sampling,
// roots) are refused so the exchange fails loudly rather than fabricating a
// model response.
func buildMRTRHandler(answer string, rep *report, pr printer) (client.InputHandler, error) {
	var content map[string]interface{}
	if err := json.Unmarshal([]byte(answer), &content); err != nil {
		return nil, fmt.Errorf("-answer is not a JSON object: %w", err)
	}
	accept, err := json.Marshal(map[string]interface{}{
		"action":  "accept",
		"content": content,
	})
	if err != nil {
		return nil, err
	}

	return func(_ context.Context, reqs protocol.InputRequests) (protocol.InputResponses, error) {
		responses := protocol.InputResponses{}
		for id, r := range reqs {
			if r.Method != "elicitation/create" {
				return nil, fmt.Errorf("input request %q is %s; the probe only answers elicitation/create", id, r.Method)
			}
			var params struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(r.Params, &params)
			rep.Elicited = append(rep.Elicited, params.Message)
			pr.f("  elicited   : %q → accepting with -answer\n", params.Message)
			responses[id] = accept
		}
		return responses, nil
	}, nil
}

func buildTransport(opts options, logger *zap.Logger) (transport.Transport, error) {
	if opts.URL != "" {
		return transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{
			Endpoint:       opts.URL,
			EnableSessions: true, // legacy servers may mint a session; 2026 servers never do
			Logger:         logger,
		})
	}
	parts := strings.Fields(opts.Cmd)
	return transport.NewStdioTransport(transport.StdioConfig{
		Command: parts[0],
		Args:    parts[1:],
		Logger:  logger,
	})
}

func buildLogger(verbose bool) (*zap.Logger, error) {
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	if verbose {
		cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	}
	return cfg.Build()
}

func era(stateless bool) string {
	if stateless {
		return "stateless (2026-07-28 core)"
	}
	return "legacy (initialize handshake)"
}

func more(total, shown int) string {
	if total > shown {
		return ", …"
	}
	return ""
}

func endState(err error) string {
	if err == nil {
		return "graceful (server closed the subscription)"
	}
	return err.Error()
}
