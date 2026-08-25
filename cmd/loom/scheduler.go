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
)

var schedulerCmd = &cobra.Command{
	Use:   "scheduler",
	Short: "Inspect and tune the LLM slot scheduler",
	Long: `Read the server's LLM slot-scheduler state and adjust a scope's ceiling.

The scheduler admits LLM calls per provider quota scope: a call that cannot
fit the current window parks until capacity frees rather than failing. These
commands expose what it is doing — how much capacity it believes it has, who
is parked and in which priority class — and let an operator override the
ceiling on a running server (for a drain, or to reproduce contention).`,
}

var schedulerScope string

var schedulerStateCmd = &cobra.Command{
	Use:   "state",
	Short: "Show per-scope capacity and queue depth",
	Run:   runSchedulerState,
}

var schedulerWaitersCmd = &cobra.Command{
	Use:   "waiters",
	Short: "List slot requests currently parked, oldest first",
	Run:   runSchedulerWaiters,
}

var (
	setTPM           int64
	setStarvationAge int32
	setUtilization   float32
	setHeadroom      float32
)

var schedulerSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Override a scope's scheduler configuration",
	Long: `Override scheduler configuration for one provider quota scope.

Only the flags you pass are changed. --tpm 0 restores calibration from
provider response headers (or the scope's rate_limit configuration when the
provider states no telemetry).`,
	Run: runSchedulerSet,
}

func init() {
	schedulerCmd.PersistentFlags().StringVar(&schedulerScope, "scope", "", "Provider quota scope (default: all scopes)")

	schedulerSetCmd.Flags().Int64Var(&setTPM, "tpm", -1, "Enforced tokens per minute (0 = recalibrate from provider headers)")
	schedulerSetCmd.Flags().Int32Var(&setStarvationAge, "starvation-age-s", -1, "Seconds before a parked request is promoted one priority class")
	schedulerSetCmd.Flags().Float32Var(&setUtilization, "utilization", -1, "Target utilization of measured capacity, (0, 1]")
	schedulerSetCmd.Flags().Float32Var(&setHeadroom, "interactive-headroom", -1, "Fraction of the budget reserved for interactive turns, [0, 1)")

	schedulerCmd.AddCommand(schedulerStateCmd)
	schedulerCmd.AddCommand(schedulerWaitersCmd)
	schedulerCmd.AddCommand(schedulerSetCmd)
}

func schedulerClient() (loomv1.LLMSchedulerServiceClient, func(), error) {
	c, cleanup, err := newClient()
	if err != nil {
		return nil, nil, err
	}
	return c.SchedulerClient(), cleanup, nil
}

func runSchedulerState(_ *cobra.Command, _ []string) {
	sc, cleanup, err := schedulerClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := sc.GetSlotState(ctx, &loomv1.GetSlotStateRequest{Scope: schedulerScope})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading slot state: %v\n", err)
		os.Exit(1)
	}
	if len(resp.States) == 0 {
		fmt.Println("No scheduler scopes. The slot scheduler is enabled per server with llm.scheduler_enabled;")
		fmt.Println("a scope appears once its provider has served a call.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SCOPE\tTPM\tPARKED\tRESERVED\tGRANTS\tPROMOTIONS\tACTIVE\tDOOR Q\tNEXT WAKE")
	_, _ = fmt.Fprintln(w, "-----\t---\t------\t--------\t------\t----------\t------\t------\t---------")
	for _, s := range resp.States {
		wake := "-"
		if s.NextWake != nil {
			if d := time.Until(s.NextWake.AsTime()); d > 0 {
				wake = d.Truncate(time.Millisecond).String()
			}
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			s.Scope, s.EffectiveTokensPerMinute, s.ParkedRequests, s.ReservedTokensOutstanding,
			s.GrantsTotal, s.PromotionsTotal, s.ActiveConversations, s.DoorQueueDepth, wake)
	}
	_ = w.Flush()
}

func runSchedulerWaiters(_ *cobra.Command, _ []string) {
	sc, cleanup, err := schedulerClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := sc.ListWaiters(ctx, &loomv1.ListWaitersRequest{Scope: schedulerScope})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing waiters: %v\n", err)
		os.Exit(1)
	}
	if len(resp.Waiters) == 0 {
		fmt.Println("No parked slot requests — every call is being admitted on arrival.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "WAITING\tCLASS\tORIGIN\tPROMOTIONS\tAGENT\tCONVERSATION")
	_, _ = fmt.Fprintln(w, "-------\t-----\t------\t----------\t-----\t------------")
	for _, waiter := range resp.Waiters {
		age := "-"
		if waiter.WaitingSince != nil {
			age = time.Since(waiter.WaitingSince.AsTime()).Truncate(time.Second).String()
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			age, shortClass(waiter.Class), shortOrigin(waiter.Origin),
			waiter.Promotions, waiter.AgentName, waiter.ConversationId)
	}
	_ = w.Flush()
}

// shortClass renders the priority class without its proto prefix.
func shortClass(c loomv1.SlotPriorityClass) string {
	switch c {
	case loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW:
		return "new"
	case loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT:
		return "in-flight"
	case loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER:
		return "holder"
	default:
		return "unspecified"
	}
}

// shortOrigin renders the band without its proto prefix.
func shortOrigin(o loomv1.SlotOrigin) string {
	switch o {
	case loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE:
		return "interactive"
	case loomv1.SlotOrigin_SLOT_ORIGIN_BATCH:
		return "batch"
	default:
		return "unspecified"
	}
}

func runSchedulerSet(_ *cobra.Command, _ []string) {
	if schedulerScope == "" {
		fmt.Fprintln(os.Stderr, "Error: --scope is required (see `loom scheduler state` for scope names)")
		os.Exit(1)
	}
	cfg := &loomv1.LLMSchedulerConfig{}
	changed := false
	if setTPM >= 0 {
		cfg.TokensPerMinute = setTPM
		changed = true
	}
	if setStarvationAge >= 0 {
		cfg.StarvationAgeS = setStarvationAge
		changed = true
	}
	if setUtilization >= 0 {
		cfg.UtilizationTarget = setUtilization
		changed = true
	}
	if setHeadroom >= 0 {
		cfg.InteractiveHeadroom = setHeadroom
		changed = true
	}
	if !changed {
		fmt.Fprintln(os.Stderr, "Error: pass at least one of --tpm, --starvation-age-s, --utilization, --interactive-headroom")
		os.Exit(1)
	}

	sc, cleanup, err := schedulerClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := sc.SetSchedulerConfig(ctx, &loomv1.SetSchedulerConfigRequest{Scope: schedulerScope, Config: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting scheduler config: %v\n", err)
		os.Exit(1)
	}
	if resp.State != nil {
		fmt.Printf("scope %s: effective %d tokens/min, %d parked, %d reserved\n",
			resp.State.Scope, resp.State.EffectiveTokensPerMinute,
			resp.State.ParkedRequests, resp.State.ReservedTokensOutstanding)
		return
	}
	fmt.Println("Applied.")
}
