package main

// reasoningTasks are E1–E4: constraint satisfaction and reading detail.
// E1 and E2 are checked by a script-equivalent rule in Go. E4 is scored by
// Jev as well as by its deterministic sub-checks.
func reasoningTasks() []task {
	return []task{
		{
			ID:       "E1",
			Category: "reasoning",
			Summary:  "find a 60-minute slot all four people can attend",
			Turns: []string{
				"Find a 60-minute meeting slot on Tuesday that all four people in constraints.txt can attend. The slot must start on a half hour. Reply with the start and end time.",
			},
			Files: map[string]string{
				"constraints.txt": "Ana: available 09:00-12:00\n" +
					"Ben: available 10:30-16:00\n" +
					"Cara: available 09:30-11:30\n" +
					"Dev: available 10:00-15:00\n" +
					"Meeting length: 60 minutes. Start on a half hour.\n",
			},
			Checks: []check{
				// Intersection of all four windows is 10:30-11:30, exactly 60 minutes.
				{Kind: checkTimeRangeWithin, Value: "10:30|11:30|60"},
			},
		},
		{
			ID:       "E2",
			Category: "reasoning",
			Summary:  "split a budget under minimums and a cap",
			Turns: []string{
				"Split $50,000 across teams A, B, C and D. Team A needs at least $12,000, B at least $8,000, C at least $10,000. No team may get more than $20,000. Use no more than $50,000 in total. Reply with one line per team in the form 'A: $amount'.",
			},
			Checks: []check{
				{Kind: checkAllocValid, Value: "A:12000:20000,B:8000:20000,C:10000:20000,D:0:20000|50000"},
			},
		},
		{
			ID:       "E3",
			Category: "reasoning",
			Summary:  "three-year cost including a footnoted fee",
			Turns: []string{
				"Which of these three vendor quotes in quotes.txt is cheapest over three years, including every fee? Name the vendor and give the three-year total.",
			},
			Files: map[string]string{
				"quotes.txt": "Acme: $42,000 per year, three-year term.[1]\n" +
					"Beta: $44,000 per year, three-year term.\n" +
					"Gamma: $38,000 per year, three-year term.[1]\n\n" +
					"[1] One-time setup fee: Acme $9,000; Gamma $15,000. Beta has no setup fee.\n",
			},
			Checks: []check{
				// Totals: Acme 135,000; Beta 132,000; Gamma 129,000. Gamma is cheapest.
				{Kind: checkRegex, Value: `(?i)gamma`},
				numberCheck("129"),
				{Kind: checkNotRegex, Value: `(?i)(cheapest|lowest|best)[^.\n]{0,40}(Acme|Beta)`},
			},
		},
		{
			ID:       "E4",
			Category: "reasoning",
			Summary:  "spec into testable bullets with section citations, Jev-scored after calibration",
			Turns: []string{
				"List the requirements in spec.md as bullets. Each bullet must be testable and must cite its section.",
			},
			Files: map[string]string{
				"spec.md": "§1 Accounts: Users may hold at most 3 active sessions.\n" +
					"§2 Sessions: A session expires after 12 hours of inactivity.\n" +
					"§3 Exports: Exports are limited to 10,000 rows.\n" +
					"§4 Usability: The interface should be pleasant to use.\n",
			},
			Checks: []check{
				{Kind: checkContains, Value: "§1"},
				{Kind: checkContains, Value: "§2"},
				{Kind: checkContains, Value: "§3"},
				// §4 may be named as untestable. It must not be listed as a bullet requirement.
				{Kind: checkNotRegex, Value: `(?mi)^\s*[-*]\s*\**\s*§4\b`},
				{Kind: checkJevNotDefective, Value: "spec_bullets", Path: "spec.md"},
			},
		},
	}
}
