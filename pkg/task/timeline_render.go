// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"fmt"
	"strings"
	"time"
)

// RenderOpts controls plain-text timeline rendering.
type RenderOpts struct {
	// DetailBytes caps each event's inline detail. 0 hides detail entirely.
	DetailBytes int
	// ShowSource appends the originating table, for debugging provenance.
	ShowSource bool
	// Now anchors relative timestamps. Zero uses the first event's time.
	Now time.Time
}

// RenderTimeline formats a timeline for a terminal or a log.
//
// This is a reference renderer, not the product UI: it exists so the read model
// can be inspected without a browser, and so the event vocabulary can be
// reviewed as text before any pixels are designed. A UI should consume
// TimelineEvent directly rather than parsing this.
func RenderTimeline(res *TimelineResult, opts RenderOpts) string {
	if res == nil || len(res.Events) == 0 {
		return "(no recorded activity for this task)\n"
	}

	var b strings.Builder
	origin := opts.Now
	if origin.IsZero() {
		origin = res.Events[0].OccurredAt
	}

	for _, e := range res.Events {
		offset := e.OccurredAt.Sub(origin).Round(time.Second)
		fmt.Fprintf(&b, "%8s  %-16s %s\n", fmtOffset(offset), glyphFor(e), e.Summary)

		if opts.DetailBytes > 0 && e.Detail != "" {
			excerpt, cut := e.Excerpt(opts.DetailBytes)
			for _, line := range strings.Split(strings.TrimRight(excerpt, "\n"), "\n") {
				fmt.Fprintf(&b, "%8s  %-16s   │ %s\n", "", "", line)
			}
			if cut {
				fmt.Fprintf(&b, "%8s  %-16s   │ …(truncated)\n", "", "")
			}
		}
		if e.DurationMs > 0 {
			fmt.Fprintf(&b, "%8s  %-16s   └ %dms\n", "", "", e.DurationMs)
		}
		if opts.ShowSource {
			fmt.Fprintf(&b, "%8s  %-16s   ~ %s#%s\n", "", "", e.SourceTable, e.SourceID)
		}
	}

	if res.Truncated {
		fmt.Fprintf(&b, "\n(showing %d of %d events)\n", len(res.Events), res.TotalMatched)
	}
	// Partial sources are surfaced, never swallowed: a reader must be able to
	// tell an empty source from a broken one.
	if len(res.PartialSources) > 0 {
		fmt.Fprintf(&b, "\n⚠ incomplete — these sources failed: %s\n",
			strings.Join(res.PartialSources, ", "))
	}
	return b.String()
}

func fmtOffset(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("+%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("+%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// glyphFor labels an event kind for scanning. Text, not icons, so the output is
// greppable and terminal-safe.
func glyphFor(e TimelineEvent) string {
	switch e.Kind {
	case TimelineKindLifecycle:
		return "[lifecycle]"
	case TimelineKindToolCall:
		return "[tool→]"
	case TimelineKindToolResult:
		if e.Success != nil && !*e.Success {
			return "[tool✗]"
		}
		return "[tool✓]"
	case TimelineKindAssistant:
		return "[agent]"
	case TimelineKindUser:
		return "[user]"
	case TimelineKindHumanRequest:
		return "[ask human]"
	case TimelineKindHumanResponse:
		return "[human]"
	default:
		return "[event]"
	}
}
