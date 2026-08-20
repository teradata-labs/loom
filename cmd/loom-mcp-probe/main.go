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
// subscriptions/listen watch. The probe runs the shipped client and
// transports unmodified — the same negotiation, fallback, and transport
// paths the manager uses in production — against whatever real server it is
// pointed at (its automated tests, by contrast, use scripted HTTP fixtures).
//
// Examples:
//
//	loom-mcp-probe -url http://localhost:8971 -call test_simple_text -watch 5
//	loom-mcp-probe -cmd npx -arg -y -arg @modelcontextprotocol/server-everything \
//	    -arg stdio -call echo -args '{"message":"hello"}'
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
	URL        string        // streamable HTTP endpoint; mutually exclusive with Cmd
	Cmd        string        // stdio server executable (verbatim; args ride CmdArgs); mutually exclusive with URL
	CmdArgs    []string      // arguments for Cmd, one per -arg flag
	HeadersEnv string        // env var holding a JSON object of HTTP headers (auth without argv exposure)
	Pin        string        // protocol_version pin: "" or "auto", "legacy", or an exact revision
	Call       string        // tool to invoke, if any
	Args       string        // JSON arguments for Call
	Answer     string        // JSON object accepted for every elicitation (enables the MRTR driver)
	WatchSec   int           // seconds to hold a subscriptions/listen stream (stateless only)
	Timeout    time.Duration // per-operation timeout: connect, tools/list, tools/call (incl. MRTR rounds), subscription ack
	Verbose    bool
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
	WatchEndErr     error    // nil = held for the full window (client-cancelled at the deadline)
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
	flag.StringVar(&opts.Cmd, "cmd", "", "stdio server executable (path taken verbatim; pass arguments via repeated -arg)")
	flag.Func("arg", "argument for the -cmd executable (repeatable, order-preserving)", func(v string) error {
		opts.CmdArgs = append(opts.CmdArgs, v)
		return nil
	})
	flag.StringVar(&opts.HeadersEnv, "headers-env", "", "name of an env var holding a JSON object of HTTP headers (e.g. auth tokens; never passed on the command line)")
	flag.StringVar(&opts.Pin, "pin", "", `protocol_version pin: "auto" (default), "legacy", or an exact revision`)
	flag.StringVar(&opts.Call, "call", "", "tool to invoke")
	flag.StringVar(&opts.Args, "args", "{}", "JSON arguments for -call")
	flag.StringVar(&opts.Answer, "answer", "", "JSON object accepted for every elicitation; enables the MRTR driver (default: fail fast on input_required)")
	flag.IntVar(&opts.WatchSec, "watch", 0, "seconds to hold a subscriptions/listen stream (stateless connections only)")
	flag.IntVar(&timeoutMs, "timeout", 15000, "per-operation timeout in ms: connect (incl. negotiation probe), tools/list, tools/call with its MRTR rounds, and the subscription acknowledgment")
	flag.BoolVar(&opts.Verbose, "v", false, "debug logging")
	flag.Parse()
	opts.Timeout = time.Duration(timeoutMs) * time.Millisecond

	if (opts.URL == "") == (opts.Cmd == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -url or -cmd is required")
		flag.Usage()
		os.Exit(2)
	}

	rep, err := run(context.Background(), opts, os.Stdout)
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

	rep := &report{}

	// Every flag that can fail validation is checked before the transport is
	// built: a stdio transport starts a subprocess, and a validation error
	// after that point would leak the child with no cleanup registered.
	var callArgs map[string]interface{}
	if opts.Call != "" {
		if err := json.Unmarshal([]byte(opts.Args), &callArgs); err != nil {
			return nil, fmt.Errorf("-args is not a JSON object: %w", err)
		}
	}
	var mrtr client.InputHandler
	if opts.Answer != "" {
		if mrtr, err = buildMRTRHandler(opts.Answer, rep, pr); err != nil {
			return nil, err
		}
	}

	tr, err := buildTransport(opts, logger)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}

	cfg := client.Config{
		Transport:       tr,
		Logger:          logger,
		ProtocolVersion: opts.Pin,
		RequestTimeout:  opts.Timeout,
	}
	if mrtr != nil {
		cfg.MRTR = client.MRTRConfig{Handler: mrtr}
	}

	c := client.NewClient(cfg)
	defer func() { _ = c.Close() }()

	start := time.Now()
	connectCtx, cancelConnect := context.WithTimeout(ctx, opts.Timeout)
	err = c.Connect(connectCtx, protocol.Implementation{Name: "loom-mcp-probe", Version: version.Version})
	cancelConnect()
	if err != nil {
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

	listCtx, cancelList := context.WithTimeout(ctx, opts.Timeout)
	tools, err := c.ListTools(listCtx)
	cancelList()
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
		callCtx, cancelCall := context.WithTimeout(ctx, opts.Timeout)
		err := runCall(callCtx, c, opts, callArgs, rep, pr)
		cancelCall()
		if err != nil {
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

func runCall(ctx context.Context, c *client.Client, opts options, args map[string]interface{}, rep *report, pr printer) error {
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
	// A requested watch is a gate: every path that cannot deliver a healthy,
	// acknowledged subscription held for the full window is a probe failure,
	// never a silent exit 0.
	if !c.IsStateless() {
		return fmt.Errorf("-watch requested, but the connection is legacy (%s): subscriptions/listen is 2026-07-28 only", c.NegotiatedVersion())
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

	// The acknowledgment is the first item on the channel; without it within
	// the operation timeout the subscription was never established.
	ackTimer := time.After(opts.Timeout)
	select {
	case n, ok := <-sub.Notifications:
		if !ok {
			return fmt.Errorf("subscription ended before acknowledgment: %v", endState(sub.Err()))
		}
		if n.Method != protocol.NotificationSubscriptionAcknowledged {
			return fmt.Errorf("first subscription message was %s, not the acknowledgment", n.Method)
		}
	case <-ackTimer:
		return fmt.Errorf("no subscription acknowledgment within %s", opts.Timeout)
	}
	pr.f("  watch      : subscription %s acknowledged, holding for %ds\n", sub.ID, opts.WatchSec)

	deadline := time.After(time.Duration(opts.WatchSec) * time.Second)
	for {
		select {
		case n, ok := <-sub.Notifications:
			if !ok {
				// Any end before the requested window — error or graceful
				// server-side closure — means the watch did not hold.
				rep.WatchEndErr = sub.Err()
				return fmt.Errorf("subscription ended %s before the watch window elapsed: %v",
					sub.ID, endState(rep.WatchEndErr))
			}
			rep.Notifications = append(rep.Notifications, n.Method)
			pr.f("    notif #%d : %s\n", len(rep.Notifications), n.Method)
		case <-deadline:
			subCancel()
			<-sub.Done()
			// Client-initiated cancellation at the deadline is the healthy
			// outcome: the subscription held for the full window.
			pr.f("  watch done : %d notifications, held %ds, end state: client cancelled\n",
				len(rep.Notifications), opts.WatchSec)
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
		headers, err := headersFromEnv(opts.HeadersEnv)
		if err != nil {
			return nil, err
		}
		return transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{
			Endpoint:       opts.URL,
			EnableSessions: true, // legacy servers may mint a session; 2026 servers never do
			Headers:        headers,
			Logger:         logger,
		})
	}
	// The executable is taken verbatim (Windows paths and spaces survive);
	// arguments arrive one per -arg flag, so nothing is ever re-tokenized.
	if strings.TrimSpace(opts.Cmd) == "" {
		return nil, fmt.Errorf("-cmd must name an executable")
	}
	return transport.NewStdioTransport(transport.StdioConfig{
		Command: opts.Cmd,
		Args:    opts.CmdArgs,
		Logger:  logger,
	})
}

// headersFromEnv reads a JSON object of HTTP headers from the named env var.
// Headers ride the environment rather than argv so tokens never appear in
// process listings or shell history.
func headersFromEnv(envName string) (map[string]string, error) {
	if envName == "" {
		return nil, nil
	}
	raw, ok := os.LookupEnv(envName)
	if !ok {
		return nil, fmt.Errorf("-headers-env: environment variable %s is not set", envName)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("-headers-env: %s does not hold a JSON object of strings: %w", envName, err)
	}
	return headers, nil
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
