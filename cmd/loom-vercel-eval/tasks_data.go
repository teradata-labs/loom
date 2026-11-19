package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// dataTasks are B1–B6: calculation and reconciliation over generated
// fixtures. Expected answers are computed from the same generated data.
func dataTasks() []task {
	return []task{
		salesTotals(),
		invoiceGaps(),
		businessDayCount(),
		orderAverages(),
		sensorMax(),
		reconcile(),
	}
}

// businessDayCount is B3: weekdays between two dates, less holidays.
func businessDayCount() task {
	holidays := map[string]bool{"2026-03-20": true, "2026-04-03": true}
	n := businessDays(mustDate("2026-03-02"), mustDate("2026-04-15"), holidays)
	return task{
		ID:       "B3",
		Category: "data",
		Summary:  "count business days excluding holidays",
		Turns:    []string{"How many business days (Mon–Fri, excluding the dates listed in holidays.txt) are there between 2026-03-02 and 2026-04-15 inclusive? Give the number."},
		Files:    map[string]string{"holidays.txt": "2026-03-20\n2026-04-03\n"},
		Checks:   []check{numberCheck(fmtInt(n))},
	}
}

// salesTotals is B1: totals per region from a CSV with two malformed rows.
func salesTotals() task {
	regions := []string{"North", "South", "West"}
	var b strings.Builder
	b.WriteString("id,region,amount\n")
	totals := map[string]int{}
	for i := 1; i <= 120; i++ {
		r := regions[i%3]
		amt := 10 + (i*37)%90
		totals[r] += amt
		fmt.Fprintf(&b, "%d,%s,%d\n", i, r, amt)
	}
	b.WriteString("999,South,abc\n")
	b.WriteString("1000,North\n")

	top, topVal := "", -1
	for _, r := range regions {
		if totals[r] > topVal {
			top, topVal = r, totals[r]
		}
	}
	return task{
		ID:       "B1",
		Category: "data",
		Summary:  "per-region totals from a CSV with malformed rows",
		Turns:    []string{"Total the amount column per region in sales.csv. Report each region's total, the top region, and how many rows were malformed."},
		Files:    map[string]string{"sales.csv": b.String()},
		Checks: []check{
			numberCheck(fmtInt(totals["North"])),
			numberCheck(fmtInt(totals["South"])),
			numberCheck(fmtInt(totals["West"])),
			{Kind: checkRegex, Value: `(?i)\b` + top + `\b`},
			{Kind: checkRegex, Value: `(?i)\b(two|2)\b`},
		},
	}
}

// invoiceGaps is B2: invoices with no matching payment.
func invoiceGaps() task {
	missing := map[int]bool{1003: true, 1017: true, 1031: true, 1044: true}
	type rec struct {
		ID       int    `json:"id"`
		Customer string `json:"customer"`
	}
	var inv, pay []rec
	for id := 1000; id < 1050; id++ {
		inv = append(inv, rec{ID: id, Customer: fmt.Sprintf("cust-%d", id%9)})
		if !missing[id] {
			pay = append(pay, rec{ID: id, Customer: fmt.Sprintf("cust-%d", id%9)})
		}
	}
	pay = append(pay, rec{ID: 2001, Customer: "cust-1"}, rec{ID: 2002, Customer: "cust-2"})
	invB, _ := json.MarshalIndent(inv, "", "  ")
	payB, _ := json.MarshalIndent(pay, "", "  ")

	checks := []check{}
	for _, id := range []int{1003, 1017, 1031, 1044} {
		checks = append(checks, numberCheck(fmtInt(id)))
	}
	// Payment-only ids (2001, 2002) may be mentioned as contrast. A paid id
	// must not be listed as a gap: a line naming it together with a gap word.
	checks = append(checks, check{Kind: checkNotRegex,
		Value: `(?mi)^.*\b(1000|1049)\b.*(missing|no payment|not in payments|without a payment|not paid)`})

	return task{
		ID:       "B2",
		Category: "data",
		Summary:  "ids present in one JSON file and absent from another",
		Turns:    []string{"Which invoice ids are in invoices.json but have no matching id in payments.json? Give the ids."},
		Files: map[string]string{
			"invoices.json": string(invB) + "\n",
			"payments.json": string(payB) + "\n",
		},
		Checks: checks,
	}
}

// orderAverages is B4: average order value per month, from a SQLite database
// the harness builds before the agent starts.
func orderAverages() task {
	var b strings.Builder
	b.WriteString("CREATE TABLE orders (id INTEGER PRIMARY KEY, order_date TEXT, amount REAL);\n")
	id := 1
	avgs := []int{}
	for m := 1; m <= 6; m++ {
		a, c := 100*m, 100*m+10
		fmt.Fprintf(&b, "INSERT INTO orders VALUES (%d, '2026-%02d-05', %d);\n", id, m, a)
		fmt.Fprintf(&b, "INSERT INTO orders VALUES (%d, '2026-%02d-20', %d);\n", id+1, m, c)
		id += 2
		avgs = append(avgs, (a+c)/2)
	}
	for m := 1; m <= 6; m++ {
		fmt.Fprintf(&b, "INSERT INTO orders VALUES (%d, '2025-%02d-05', 1000);\n", id, m)
		fmt.Fprintf(&b, "INSERT INTO orders VALUES (%d, '2025-%02d-20', 1000);\n", id+1, m)
		id += 2
	}

	checks := []check{}
	for _, v := range avgs {
		checks = append(checks, numberCheck(fmtInt(v)))
	}
	checks = append(checks, notNumberCheck("1000"))
	return task{
		ID:       "B4",
		Category: "data",
		Summary:  "monthly averages from SQLite, filtered to one year",
		Setup:    []string{"sqlite3 orders.db < orders.sql"},
		Turns:    []string{"Query orders.db for the average order value per month in 2026, highest first. Give each month and its average."},
		Files:    map[string]string{"orders.sql": b.String()},
		Checks:   checks,
	}
}

// sensorMax is B5: a maximum across mixed Fahrenheit and Celsius readings.
// The overall maximum is 104°F, which is 40°C; the Celsius readings are lower.
func sensorMax() task {
	log := strings.Join([]string{
		"2026-05-01T08:00 sensor=a temp=68F",
		"2026-05-01T09:00 sensor=b temp=28C",
		"2026-05-01T10:00 sensor=a temp=86F",
		"2026-05-01T11:00 sensor=c temp=31C",
		"2026-05-01T12:00 sensor=b temp=104F",
		"2026-05-01T13:00 sensor=c temp=77F",
		"",
	}, "\n")
	return task{
		ID:       "B5",
		Category: "data",
		Summary:  "maximum temperature in Celsius from mixed units",
		Turns:    []string{"Convert the temperatures in sensor.log to Celsius, then report the maximum in Celsius. Readings tagged C are already Celsius."},
		Files:    map[string]string{"sensor.log": log},
		Checks: []check{
			numberCheck("40"),
			// Mentioning 104 °F as the source is fine; claiming 104 °C is not.
			{Kind: checkNotRegex, Value: `(?i)\b104\s*°?\s*C\b`},
		},
	}
}

// reconcile is B6: three planted mismatches between a statement and a ledger.
func reconcile() task {
	type tx struct {
		id, date, amount, desc string
	}
	base := []tx{
		{"T01", "2026-04-01", "120.00", "office supplies"},
		{"T02", "2026-04-02", "45.50", "coffee"},
		{"T03", "2026-04-03", "980.00", "hosting"},
		{"T04", "2026-04-09", "310.00", "travel"},
		{"T05", "2026-04-05", "18.25", "parking"},
		{"T06", "2026-04-06", "742.10", "software licence"},
		{"T07", "2026-04-07", "250.00", "catering"},
		{"T08", "2026-04-08", "66.00", "printing"},
		{"T09", "2026-04-09", "12.00", "postage"},
		{"T10", "2026-04-10", "530.75", "contractor"},
		{"T11", "2026-04-11", "95.40", "training"},
		{"T12", "2026-04-12", "210.00", "insurance"},
	}
	var st, lg strings.Builder
	st.WriteString("id,date,amount,description\n")
	lg.WriteString("id,date,amount,description\n")
	for _, t := range base {
		fmt.Fprintf(&st, "%s,%s,%s,%s\n", t.id, t.date, t.amount, t.desc)
		switch t.id {
		case "T04": // date differs
			fmt.Fprintf(&lg, "%s,%s,%s,%s\n", t.id, "2026-04-08", t.amount, t.desc)
		case "T07": // amount differs
			fmt.Fprintf(&lg, "%s,%s,%s,%s\n", t.id, t.date, "205.00", t.desc)
		case "T10": // missing from ledger
		default:
			fmt.Fprintf(&lg, "%s,%s,%s,%s\n", t.id, t.date, t.amount, t.desc)
		}
	}
	return task{
		ID:       "B6",
		Category: "data",
		Summary:  "reconcile a statement against a ledger, three planted mismatches",
		Turns:    []string{"Reconcile statement.csv against ledger.csv. List every transaction that does not match, with the reason."},
		Files: map[string]string{
			"statement.csv": st.String(),
			"ledger.csv":    lg.String(),
		},
		Checks: []check{
			{Kind: checkRegex, Value: `\bT04\b`},
			{Kind: checkRegex, Value: `\bT07\b`},
			{Kind: checkRegex, Value: `\bT10\b`},
			// Matching transactions may be named as matches. A line that names one
			// of them as a mismatch is a false positive.
			{Kind: checkNotRegex, Value: `(?mi)^.*\bT(01|02|03|05|06|08|09|11|12)\b.*(differ|mismatch|missing|does not match|doesn't match|not match|no match)`},
			{Kind: checkRegex, Value: `(?i)date`},
			{Kind: checkRegex, Value: `(?i)amount`},
			{Kind: checkRegex, Value: `(?i)(missing|not in the ledger|not recorded|absent|not found|not present)`},
		},
	}
}
