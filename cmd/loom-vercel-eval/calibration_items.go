package main

import "fmt"

// Criteria are the exact text Jev is asked in both calibration and Phase 5
// scoring. They are data, so a change to a criterion is a visible diff.
const (
	criterionActionItems = "The source is a meeting transcript and the output is a list of action items. " +
		"The output is defective if it leaves out an action item stated in the transcript, assigns an item " +
		"to the wrong owner, or gives a due date the transcript does not state for that item. Items the " +
		"transcript gives no date for may be listed without a date. Is the output defective?"

	criterionSpecBullets = "The source is a specification and the output is a list of requirement bullets " +
		"with section citations. The output is defective if any bullet is not testable, states a requirement " +
		"the specification does not contain, or cites a section that does not exist or does not contain that " +
		"requirement. A line that only notes that part of the specification is not testable or is left out is not " +
		"a requirement bullet and does not make the output defective. Is the output defective?"
)

// calibrationItems returns the 26 known-answer items: 12 for action items
// (D5) and 14 for spec bullets (E4). Half of each set carries one planted
// defect. The label is the construction, so nobody judges these by hand.
func calibrationItems() []calItem {
	var out []calItem

	t1 := "Alice: I will send the Q3 forecast to finance by 2026-09-18.\n" +
		"Bob: I'll take the vendor contract review. No date yet.\n" +
		"Alice: Carol, please book the Thursday room for the review.\n" +
		"Carol: I'll confirm the room by Monday 2026-09-14.\n"
	t2 := "Dev: The login timeout bug is mine. I'll have a fix up by 2026-10-02.\n" +
		"Erin: Someone needs to update the on-call runbook. I'll do it, no date.\n" +
		"Frank: I'll ask legal about the data retention wording by 2026-10-09.\n"

	t1Good := []string{
		"- Alice: send Q3 forecast to finance, due 2026-09-18\n" +
			"- Bob: review vendor contract, no due date stated\n" +
			"- Carol: confirm the Thursday room, due 2026-09-14",
		"1. Carol: confirm Thursday review room, due 2026-09-14\n" +
			"2. Alice: send Q3 forecast to finance, due 2026-09-18\n" +
			"3. Bob: review vendor contract (no due date)",
		"| Owner | Action | Due |\n|---|---|---|\n" +
			"| Alice | Send Q3 forecast to finance | 2026-09-18 |\n" +
			"| Bob | Review vendor contract | not stated |\n" +
			"| Carol | Confirm Thursday room | 2026-09-14 |",
	}
	t1Bad := []calItem{
		{Defect: "drop_item", Output: "- Alice: send Q3 forecast to finance, due 2026-09-18\n" +
			"- Carol: confirm the Thursday room, due 2026-09-14"},
		{Defect: "owner_swap", Output: "- Bob: send Q3 forecast to finance, due 2026-09-18\n" +
			"- Bob: review vendor contract, no due date stated\n" +
			"- Carol: confirm the Thursday room, due 2026-09-14"},
		{Defect: "invented_date", Output: "- Alice: send Q3 forecast to finance, due 2026-09-18\n" +
			"- Bob: review vendor contract, due 2026-09-21\n" +
			"- Carol: confirm the Thursday room, due 2026-09-14"},
	}

	t2Good := []string{
		"- Dev: fix login timeout bug, due 2026-10-02\n" +
			"- Erin: update on-call runbook, no due date\n" +
			"- Frank: ask legal about data retention wording, due 2026-10-09",
		"Action items:\n1) Frank -> ask legal about data retention wording (2026-10-09)\n" +
			"2) Dev -> fix login timeout bug (2026-10-02)\n3) Erin -> update on-call runbook (none given)",
		"Owner: Dev | Task: fix login timeout bug | Due: 2026-10-02\n" +
			"Owner: Erin | Task: update on-call runbook | Due: not stated\n" +
			"Owner: Frank | Task: ask legal about data retention wording | Due: 2026-10-09",
	}
	t2Bad := []calItem{
		{Defect: "drop_item", Output: "- Dev: fix login timeout bug, due 2026-10-02\n" +
			"- Erin: update on-call runbook, no due date"},
		{Defect: "owner_swap", Output: "- Erin: fix login timeout bug, due 2026-10-02\n" +
			"- Erin: update on-call runbook, no due date\n" +
			"- Frank: ask legal about data retention wording, due 2026-10-09"},
		{Defect: "invented_date", Output: "- Dev: fix login timeout bug, due 2026-10-02\n" +
			"- Erin: update on-call runbook, due 2026-10-16\n" +
			"- Frank: ask legal about data retention wording, due 2026-10-09"},
	}

	for i, g := range t1Good {
		out = append(out, calItem{ID: fmt.Sprintf("D5-T1-good-%d", i+1), TaskType: "D5", Source: t1, Output: g, Criterion: criterionActionItems})
	}
	for i, b := range t1Bad {
		b.ID, b.TaskType, b.Defective, b.Source, b.Criterion = fmt.Sprintf("D5-T1-bad-%d", i+1), "D5", true, t1, criterionActionItems
		out = append(out, b)
	}
	for i, g := range t2Good {
		out = append(out, calItem{ID: fmt.Sprintf("D5-T2-good-%d", i+1), TaskType: "D5", Source: t2, Output: g, Criterion: criterionActionItems})
	}
	for i, b := range t2Bad {
		b.ID, b.TaskType, b.Defective, b.Source, b.Criterion = fmt.Sprintf("D5-T2-bad-%d", i+1), "D5", true, t2, criterionActionItems
		out = append(out, b)
	}

	s1 := "§2.1 After 5 failed login attempts within 10 minutes, the account is locked.\n" +
		"§2.2 Password reset links expire 30 minutes after they are issued.\n" +
		"§3.1 Every audit log entry records user ID, timestamp, and action.\n" +
		"§3.2 The system should feel fast to users.\n"
	s2 := "§1.1 Invoices over 10,000 USD require a second approver.\n" +
		"§1.2 Approvals are recorded with approver ID and time.\n" +
		"§2.1 Exports must be limited to 5,000 rows per request.\n" +
		"§2.2 Users see only their own department's invoices.\n"

	s1Good := []string{
		"- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Expire password reset links 30 minutes after issue (§2.2)\n" +
			"- Audit entries include user ID, timestamp, and action (§3.1)",
		"- Audit log entries must record user ID, timestamp and action (§3.1)\n" +
			"- Password reset links expire after 30 minutes (§2.2)",
		"- Account locks after 5 failed attempts in 10 minutes (§2.1)\n" +
			"- Reset links expire after 30 minutes (§2.2)\n" +
			"- Audit entries record user ID, timestamp and action (§3.1)",
		// A note about an untestable line is not a requirement (added after
		// the 2026-11-18 run, where Jev and the reviewer disagreed on it).
		"- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Expire password reset links 30 minutes after issue (§2.2)\n" +
			"- Audit entries include user ID, timestamp, and action (§3.1)\n" +
			"Note: §3.2 (\"should feel fast\") is not testable as written, so it is not listed.",
	}
	s1Bad := []calItem{
		{Defect: "untestable", Output: "- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Expire password reset links 30 minutes after issue (§2.2)\n" +
			"- The system should feel fast to users (§3.2)"},
		{Defect: "bad_citation", Output: "- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Passwords must be at least 12 characters (§4.1)\n" +
			"- Audit entries include user ID, timestamp, and action (§3.1)"},
		{Defect: "invented_requirement", Output: "- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Expire password reset links 30 minutes after issue (§2.2)\n" +
			"- Audit logs are kept for 7 years (§3.1)"},
		// The same note does not excuse a real defect elsewhere.
		{Defect: "invented_requirement_with_note", Output: "- Lock the account after 5 failed logins within 10 minutes (§2.1)\n" +
			"- Sessions expire after 15 minutes of inactivity (§2.2)\n" +
			"Note: §3.2 (\"should feel fast\") is not testable as written, so it is not listed."},
	}

	s2Good := []string{
		"- Invoices over 10,000 USD need a second approver (§1.1)\n" +
			"- Approvals record approver ID and time (§1.2)\n" +
			"- Exports are capped at 5,000 rows per request (§2.1)\n" +
			"- Users see only their department's invoices (§2.2)",
		"- Exports limited to 5,000 rows (§2.1)\n" +
			"- Second approver required above 10,000 USD (§1.1)",
		"- Approver ID and time are recorded for each approval (§1.2)\n" +
			"- Invoice visibility is scoped to the user's department (§2.2)",
	}
	s2Bad := []calItem{
		{Defect: "untestable", Output: "- Invoices over 10,000 USD need a second approver (§1.1)\n" +
			"- The approval workflow should be intuitive for approvers (§1.2)\n" +
			"- Exports are capped at 5,000 rows per request (§2.1)"},
		{Defect: "bad_citation", Output: "- Invoices over 10,000 USD need a second approver (§1.1)\n" +
			"- Exports are capped at 5,000 rows per request (§3.1)"},
		{Defect: "invented_requirement", Output: "- Invoices over 10,000 USD need a second approver (§1.1)\n" +
			"- Exports are kept in a shared folder for 90 days (§2.1)"},
	}

	for i, g := range s1Good {
		out = append(out, calItem{ID: fmt.Sprintf("E4-S1-good-%d", i+1), TaskType: "E4", Source: s1, Output: g, Criterion: criterionSpecBullets})
	}
	for i, b := range s1Bad {
		b.ID, b.TaskType, b.Defective, b.Source, b.Criterion = fmt.Sprintf("E4-S1-bad-%d", i+1), "E4", true, s1, criterionSpecBullets
		out = append(out, b)
	}
	for i, g := range s2Good {
		out = append(out, calItem{ID: fmt.Sprintf("E4-S2-good-%d", i+1), TaskType: "E4", Source: s2, Output: g, Criterion: criterionSpecBullets})
	}
	for i, b := range s2Bad {
		b.ID, b.TaskType, b.Defective, b.Source, b.Criterion = fmt.Sprintf("E4-S2-bad-%d", i+1), "E4", true, s2, criterionSpecBullets
		out = append(out, b)
	}
	return out
}
