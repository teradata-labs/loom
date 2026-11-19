package main

// memoryTasks are C1–C5: scripted multi-turn sessions. The checks read the
// final reply only. Each session keeps one agent and one session id, so the
// facts must be carried in the conversation.
func memoryTasks() []task {
	return []task{
		{
			ID:       "C1",
			Category: "memory",
			Summary:  "recall a fact stated several turns earlier",
			Turns: []string{
				"For the staging environment, the port is 5443. Remember it.",
				"What is a good name for a new Go package that handles retries?",
				"Explain in one sentence what a context.Context is.",
				"Suggest one file name for a README about deployment.",
				"What staging port did I give you? Reply with the number only.",
			},
			Checks: []check{
				{Kind: checkContains, Value: "5443"},
			},
		},
		{
			ID:       "C2",
			Category: "memory",
			Summary:  "a later correction replaces an earlier fact",
			Turns: []string{
				"The on-call engineer this week is Priya.",
				"We use PagerDuty for alerts.",
				"In one line, what does PagerDuty do?",
				"Correction: on-call changed. It is now Marcus, not Priya.",
				"What is 17 times 3?",
				"Who is on call right now? Name only.",
			},
			Checks: []check{
				{Kind: checkContains, Value: "Marcus"},
				{Kind: checkNotContains, Value: "Priya"},
			},
		},
		{
			ID:       "C3",
			Category: "memory",
			Summary:  "filter a milestone list by date from earlier turns",
			Turns: []string{
				"Project Atlas milestones: M1 design freeze 2026-05-15; M2 beta 2026-06-10; M3 security review 2026-07-01; M4 GA launch 2026-08-20.",
				"Acknowledged, that is the plan.",
				"Is logging discussed in the plan? Say 'none' if it is not.",
				"List the milestones after 2026-06-01 with their names and dates.",
			},
			Checks: []check{
				{Kind: checkContains, Value: "2026-06-10"},
				{Kind: checkContains, Value: "2026-07-01"},
				{Kind: checkContains, Value: "2026-08-20"},
				// M1 may be named as excluded. It must not be listed as a row or bullet.
				{Kind: checkNotRegex, Value: `(?mi)^\s*(\|?\s*[-*]?\s*)?(M1\b|design freeze)`},
			},
		},
		{
			ID:       "C4",
			Category: "memory",
			Summary:  "apply a stated unit preference to a later question",
			Turns: []string{
				"Please answer all my questions in metric units.",
				"How far is a marathon?",
				"What is a good beginner 5k time?",
				"How far is a half marathon?",
			},
			Checks: []check{
				{Kind: checkRegex, Value: `(?i)\bkm\b|kilomet`},
				{Kind: checkNotRegex, Value: `(?i)\bmiles?\b`},
			},
		},
		{
			ID:       "C5",
			Category: "memory",
			Summary:  "track a list through an edit",
			Turns: []string{
				"Shopping list: eggs, milk, bread, coffee, apples, rice.",
				"What is a good breakfast idea?",
				"Remove the coffee from the list.",
				"What is the list now?",
			},
			Checks: []check{
				{Kind: checkContains, Value: "eggs", Fold: true},
				{Kind: checkContains, Value: "milk", Fold: true},
				{Kind: checkContains, Value: "bread", Fold: true},
				{Kind: checkContains, Value: "apples", Fold: true},
				{Kind: checkContains, Value: "rice", Fold: true},
				{Kind: checkNotContains, Value: "coffee", Fold: true},
			},
		},
	}
}
