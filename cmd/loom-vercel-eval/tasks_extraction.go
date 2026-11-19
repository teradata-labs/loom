package main

import (
	"fmt"
	"strings"
)

// extractionTasks are D1–D5: pulling fields out of text. D5 is scored by
// Jev as well as by its deterministic sub-checks, and only after the
// calibration gate has passed.
func extractionTasks() []task {
	return []task{
		{
			ID:       "D1",
			Category: "extraction",
			Summary:  "one email into a JSON object",
			Turns: []string{
				"Extract the order number, order date (as YYYY-MM-DD), total, and ship-to country from email.txt. Reply with one JSON object with keys order_number, order_date, total, country.",
			},
			Files: map[string]string{
				"email.txt": "From: orders@shop.example\nSubject: Order PO-48213 confirmed\nDate: 11 Mar 2026\nTotal charged: CAD 1,240.50\nShip to: Toronto, Canada\n",
			},
			Checks: []check{
				{Kind: checkContains, Value: "PO-48213"},
				{Kind: checkRegex, Value: `2026-03-11`},
				{Kind: checkRegex, Value: `1,?240\.50`},
				{Kind: checkContains, Value: "Canada"},
				{Kind: checkRegex, Value: `\{[\s\S]*\}`},
			},
		},
		{
			ID:       "D2",
			Category: "extraction",
			Summary:  "ten emails in varied formats, one email holds two orders",
			Turns: []string{
				"Extract every order number in emails/ into one JSON array, one object per order with keys order_number and country.",
			},
			Files: multiEmailFiles(),
			Checks: func() []check {
				var cs []check
				for _, n := range multiEmailOrders() {
					cs = append(cs, check{Kind: checkContains, Value: n})
				}
				return cs
			}(),
		},
		{
			ID:       "D3",
			Category: "extraction",
			Summary:  "classify 20 tickets with a rubric, 85% accuracy gate",
			Turns: []string{
				"Classify each ticket in tickets.txt as P1, P2 or P3 using rubric.md. Reply with one line per ticket in the form 'T-01: P1'.",
			},
			Files: ticketFiles(),
			Checks: []check{
				{Kind: checkLabelAccuracy, Value: ticketLabelSpec(), Path: "0.85"},
			},
		},
		{
			ID:       "D4",
			Category: "extraction",
			Summary:  "all dates in a contract and the termination date",
			Turns: []string{
				"List every date in contract.txt and say which one is the termination date.",
			},
			Files: map[string]string{
				"contract.txt": "Agreement signed 2025-12-20.\nEffective Date: 2026-01-15.\nRenewal notice due 2026-10-01.\nTermination effective 2026-12-31 unless renewed.\n",
			},
			Checks: []check{
				{Kind: checkContains, Value: "2025-12-20"},
				{Kind: checkContains, Value: "2026-01-15"},
				{Kind: checkContains, Value: "2026-10-01"},
				{Kind: checkContains, Value: "2026-12-31"},
				{Kind: checkRegex, Value: `(?i)terminat[^.\n]*2026-12-31|2026-12-31[^.\n]*terminat`},
			},
		},
		{
			ID:       "D5",
			Category: "extraction",
			Summary:  "action items from a meeting, Jev-scored after calibration",
			Turns: []string{
				"Turn transcript.txt into a list of action items with owners and due dates. Where the transcript gives no due date, say so.",
			},
			Files: map[string]string{
				"transcript.txt": "Gina: I'll draft the launch FAQ and share it by 2026-11-02.\n" +
					"Hal: Someone has to tell support about the launch. I'll take that; no date.\n" +
					"Iris: I'll check with the vendor on the SSO certificate by Friday, 2026-11-06.\n" +
					"Gina: Iris, please also add the SSO item to the launch checklist.\n",
			},
			Checks: []check{
				{Kind: checkContains, Value: "Gina"},
				{Kind: checkContains, Value: "Hal"},
				{Kind: checkContains, Value: "Iris"},
				{Kind: checkContains, Value: "2026-11-02"},
				{Kind: checkContains, Value: "2026-11-06"},
				// Items the transcript leaves undated must not gain a date. The
				// check reads only the lines about those items; "today is" lines
				// that mention a date are not items.
				{Kind: checkNotRegex, Value: `(?mi)^.*(support|checklist).*2026-11-(0[13-589]|[1-3]\d)`},
				{Kind: checkJevNotDefective, Value: "action_items", Path: "transcript.txt"},
			},
		},
	}
}

// multiEmailFiles returns ten emails in mixed formats. Email 4 holds two orders.
func multiEmailFiles() map[string]string {
	out := map[string]string{}
	countries := []string{"Canada", "Germany", "Japan", "Brazil", "France"}
	for i := 1; i <= 10; i++ {
		order := fmt.Sprintf("PO-5%04d", 100+i*7)
		country := countries[i%len(countries)]
		var body string
		switch i % 3 {
		case 0:
			body = fmt.Sprintf("Thanks for your purchase.\nOrder: %s\nShip to: %s\n", order, country)
		case 1:
			body = fmt.Sprintf("ORDER REF %s | destination %s | total 99.00\n", order, country)
		default:
			body = fmt.Sprintf("Hi,\n\nYour order %s is on its way to %s.\n", order, country)
		}
		if i == 4 {
			extra := fmt.Sprintf("PO-5%04d", 900+i)
			body += fmt.Sprintf("Also confirmed: a second order %s, shipping to %s.\n", extra, country)
		}
		out[fmt.Sprintf("emails/email%02d.txt", i)] = body
	}
	return out
}

// multiEmailOrders lists every order number that multiEmailFiles contains.
func multiEmailOrders() []string {
	var out []string
	for i := 1; i <= 10; i++ {
		out = append(out, fmt.Sprintf("PO-5%04d", 100+i*7))
		if i == 4 {
			out = append(out, fmt.Sprintf("PO-5%04d", 900+i))
		}
	}
	return out
}

// ticketFiles returns 20 tickets, the rubric, and nothing else. Each ticket
// is written to be clearly one priority, so the expected labels are unambiguous.
func ticketFiles() map[string]string {
	p1 := []string{
		"Production is down for all users since 9am.",
		"We lost customer data after last night's migration.",
		"Nobody can log in; the login page returns 500.",
	}
	p2 := []string{
		"Search is very slow for our team, pages take 40 seconds.",
		"CSV exports fail for some users; the JSON export works as a workaround.",
		"Notifications arrive about an hour late for a subset of accounts.",
	}
	p3 := []string{
		"Please change the footer colour to navy.",
		"Can you add a dark mode someday?",
		"What is the difference between the Pro and Team plans?",
	}
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		var text string
		switch ticketLabel(i) {
		case 1:
			text = p1[(i/3)%3]
		case 2:
			text = p2[(i/3)%3]
		default:
			text = p3[(i/3)%3]
		}
		fmt.Fprintf(&b, "T-%02d: %s\n", i, text)
	}
	rubric := "P1: service down, data loss, or no one can log in.\n" +
		"P2: degraded for some users, and a workaround exists.\n" +
		"P3: cosmetic change, question, or feature request.\n"
	return map[string]string{
		"tickets.txt": b.String(),
		"rubric.md":   rubric,
	}
}

// ticketLabel is the priority (1, 2 or 3) of ticket i. Both the ticket text
// and the expected labels come from this one function.
func ticketLabel(i int) int {
	switch i % 3 {
	case 1:
		return 1
	case 2:
		return 2
	default:
		return 3
	}
}

// ticketLabelSpec encodes the expected labels in the same order as ticketFiles.
func ticketLabelSpec() string {
	labels := []string{}
	for i := 1; i <= 20; i++ {
		labels = append(labels, fmt.Sprintf("T-%02d=P%d", i, ticketLabel(i)))
	}
	return strings.Join(labels, ",")
}
