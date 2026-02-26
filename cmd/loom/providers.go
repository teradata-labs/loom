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
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Manage LLM provider pool",
	Long:  `List and switch named LLM providers in the server's provider pool.`,
}

var listProvidersCmd = &cobra.Command{
	Use:   "list",
	Short: "List available providers in the pool",
	Run:   runListProviders,
}

var switchModelCmd = &cobra.Command{
	Use:   "switch <provider-name>",
	Short: "Switch to a named provider in the pool",
	Args:  cobra.ExactArgs(1),
	Run:   runSwitchModel,
}

var switchModelSession string
var switchModelThread string

func init() {
	switchModelCmd.Flags().StringVar(&switchModelSession, "session", "", "Session ID to switch model for")
	switchModelCmd.Flags().StringVar(&switchModelThread, "thread", "", "Thread (agent) ID")

	providersCmd.AddCommand(listProvidersCmd)
	providersCmd.AddCommand(switchModelCmd)
}

func newClient() (*client.Client, func(), error) {
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", serverAddr, err)
	}
	return c, func() { _ = c.Close() }, nil
}

func runListProviders(_ *cobra.Command, _ []string) {
	c, cleanup, err := newClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.ListProviders(ctx, &loomv1.ListProvidersRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing providers: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Providers) == 0 {
		fmt.Println("No provider pool configured. Add a 'providers:' section to looms.yaml.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tMODEL\tACTIVE")
	fmt.Fprintln(w, "----\t--------\t-----\t------")
	for _, p := range resp.Providers {
		active := ""
		if p.Name == resp.ActiveProvider {
			active = "✓"
		}
		model := ""
		if p.Config != nil {
			model = p.Config.Model
		}
		prov := ""
		if p.Config != nil {
			prov = p.Config.Provider
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, prov, model, active)
	}
	_ = w.Flush()
}

func runSwitchModel(_ *cobra.Command, args []string) {
	providerName := args[0]

	c, cleanup, err := newClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.SwitchModelByProvider(ctx, switchModelSession, switchModelThread, providerName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error switching provider: %v\n", err)
		os.Exit(1)
	}

	if resp.Success {
		fmt.Printf("Switched to provider: %s\n", providerName)
		if resp.NewModel != nil {
			fmt.Printf("  Provider: %s  Model: %s\n", resp.NewModel.Provider, resp.NewModel.Id)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Switch failed: %s\n", resp.Message)
		os.Exit(1)
	}
}
