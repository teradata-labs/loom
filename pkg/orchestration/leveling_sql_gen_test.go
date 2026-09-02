// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"hash/crc32"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver" // registers the "sqlite3" driver
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// This file generates the synthetic database and the question set for the
// SQL-generation leveling experiment (leveling_sql_live_test.go) and pins every
// property that experiment depends on. Nothing here touches the network, so
// these tests run in `-short` alongside everything else.
//
// Why SQL, after three earlier experiments on format, reasoning and knowledge.
// SQL generation is the first task in this series where a wrong answer can be
// SILENTLY wrong: a query that parses, executes and returns rows can still
// answer a different question than the one asked. The earlier tasks had only two
// failure modes (malformed, or numerically wrong), and both were visible to some
// free signal. Here there are three, and the middle one is the interesting one:
//
//	exec_error   — the SQL does not parse or does not execute. Visible to an
//	               execution signal, therefore escalatable.
//	silent_wrong — the SQL executes cleanly and returns the WRONG result. An
//	               execution signal cannot see this at all.
//	correct      — executes and matches the reference result.
//
// That split is the whole point. Arms 2 and 3 of the live experiment escalate on
// an execution signal, so they are STRUCTURALLY INCAPABLE of repairing a
// silent_wrong answer — the judge passes it on the first rung and no escalation
// ever fires. This mirrors H1 of the reasoning experiment ("a JSON schema cannot
// detect a wrong-but-well-formed answer"), one level up: an execution check
// cannot detect a wrong-but-executable query. The experiment measures how much
// of the weak model's error budget lives in the invisible bucket.
//
// Three properties are load-bearing and tested below:
//
//  1. The database is a pure function of nothing but the pinned word lists and
//     the draw order, so a re-analysis (in Go, or the parallel Python
//     calibration probe) sees byte-identical data.
//  2. Ground truth is a reference SQL statement executed against that database,
//     never a hardcoded expected value — so the truth cannot drift from the data.
//  3. No question is degenerate (empty/NULL/zero reference result), because a
//     degenerate question is answerable by an arbitrary wrong query.

// ─────────────────────────── pinned vocabulary ───────────────────────────

// The word lists are pinned and INDEX ORDER MATTERS: a row's city is
// sqCities[intn(0,7)], so reordering these slices silently regenerates the whole
// database. The parallel Python calibration probe carries the same lists in the
// same order.
var (
	sqCities     = []string{"Austin", "Boston", "Chicago", "Denver", "Fresno", "Madison", "Oakland", "Tucson"}
	sqCategories = []string{"electronics", "garden", "grocery", "office", "sports", "toys"}
	sqFirst      = []string{"Ava", "Ben", "Cara", "Dev", "Elena", "Felix", "Gina", "Hugo",
		"Iris", "Jonas", "Kira", "Liam", "Mona", "Noel", "Omar", "Priya"}
	sqLast = []string{"Adams", "Baker", "Chen", "Diaz", "Evans", "Fischer", "Gupta", "Hansen",
		"Ito", "Jones", "Kim", "Lopez", "Mehta", "Novak", "Ortiz", "Patel"}
	sqAdj   = []string{"compact", "deluxe", "eco", "folding", "heavy", "mini", "pro", "smart", "solar", "ultra"}
	sqNouns = []string{"backpack", "blender", "charger", "desk", "kettle", "lamp", "monitor", "notebook", "speaker", "tripod"}
	// sqStatuses is the status vocabulary questions draw from. The row generator
	// does NOT index into it — it maps a 0..9 roll to a skewed distribution (see
	// genSQOrder), so "delivered" is 4x as common as "cancelled".
	sqStatuses = []string{"placed", "shipped", "delivered", "cancelled"}
)

const (
	sqCustomerCount = 50
	sqProductCount  = 40
	sqOrderCount    = 300

	// sqQuestionFamilies is the number of question shapes; question q has family
	// q%5, so an N of 30 gives 6 questions per family.
	sqQuestionFamilies = 5

	// sqMaxSalt bounds the degeneracy redraw loop. A question that cannot be
	// made non-degenerate in this many redraws is a generator bug, not bad luck.
	sqMaxSalt = 200

	// sqSampleRowsPerTable is how many rows of each table are shown in the model
	// prompt. Three is enough to disambiguate column order and value shape
	// without turning the prompt into the dataset.
	sqSampleRowsPerTable = 3
)

// sqDDL is the schema, pinned verbatim. The same string is embedded in the model
// prompt, so the model sees exactly the schema the reference SQL was written
// against — no paraphrase, no drift.
const sqDDL = `CREATE TABLE customers (customer_id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT NOT NULL, signup_year INTEGER NOT NULL);
CREATE TABLE products (product_id INTEGER PRIMARY KEY, name TEXT NOT NULL, category TEXT NOT NULL, unit_price_cents INTEGER NOT NULL);
CREATE TABLE orders (order_id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(customer_id), order_date TEXT NOT NULL, status TEXT NOT NULL);
CREATE TABLE order_items (order_id INTEGER NOT NULL REFERENCES orders(order_id), product_id INTEGER NOT NULL REFERENCES products(product_id), quantity INTEGER NOT NULL, price_cents INTEGER NOT NULL, PRIMARY KEY (order_id, product_id));`

// ─────────────────────────── deterministic derivation ───────────────────────────

// sqStableSeed is the seed derivation: crc32(IEEE) over a label such as
// "sql|customers|0" or "sql|question|7|1". Same idiom as kgStableSeed and
// arithStableSeed.
func sqStableSeed(label string) uint32 {
	return crc32.ChecksumIEEE([]byte(label))
}

// sqRand is splitmix64, seeded from a row's crc32 seed. Hand-rolled for the same
// reason as kgRand: the dataset must be byte-reproducible from the index forever,
// including from a future Go release or a script in another language.
type sqRand struct{ state uint64 }

func newSQRand(seed uint32) *sqRand { return &sqRand{state: uint64(seed)} }

func (r *sqRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intn returns a value in [lo, hi], inclusive on both ends. Modulo bias is
// irrelevant here — the dataset needs determinism and range coverage, not a
// uniform distribution.
func (r *sqRand) intn(lo, hi int) int {
	return lo + int(r.next()%uint64(hi-lo+1)) //nolint:gosec // ranges are small positive constants
}

// ─────────────────────────── the rows ───────────────────────────

type sqCustomer struct {
	CustomerID int    `json:"customer_id"`
	Name       string `json:"name"`
	City       string `json:"city"`
	SignupYear int    `json:"signup_year"`
}

type sqProduct struct {
	ProductID      int    `json:"product_id"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	UnitPriceCents int    `json:"unit_price_cents"`
}

type sqOrder struct {
	OrderID    int    `json:"order_id"`
	CustomerID int    `json:"customer_id"`
	OrderDate  string `json:"order_date"`
	Status     string `json:"status"`
}

type sqOrderItem struct {
	OrderID    int `json:"order_id"`
	ProductID  int `json:"product_id"`
	Quantity   int `json:"quantity"`
	PriceCents int `json:"price_cents"`
}

// genSQCustomer generates customer row i (0-based). Draw order is fixed:
// first name, last name, city, signup year.
func genSQCustomer(i int) sqCustomer {
	r := newSQRand(sqStableSeed(fmt.Sprintf("sql|customers|%d", i)))
	firstIdx := r.intn(0, len(sqFirst)-1)
	lastIdx := r.intn(0, len(sqLast)-1)
	cityIdx := r.intn(0, len(sqCities)-1)
	signupYear := r.intn(2015, 2024)
	return sqCustomer{
		CustomerID: i + 1,
		Name:       sqFirst[firstIdx] + " " + sqLast[lastIdx],
		City:       sqCities[cityIdx],
		SignupYear: signupYear,
	}
}

// genSQProduct generates product row i (0-based). Draw order is fixed: category,
// adjective, noun, price. The trailing index in the name keeps every product
// name unique without a rejection loop.
func genSQProduct(i int) sqProduct {
	r := newSQRand(sqStableSeed(fmt.Sprintf("sql|products|%d", i)))
	catIdx := r.intn(0, len(sqCategories)-1)
	adjIdx := r.intn(0, len(sqAdj)-1)
	nounIdx := r.intn(0, len(sqNouns)-1)
	unitPrice := r.intn(199, 9999)
	return sqProduct{
		ProductID:      i + 1,
		Name:           sqAdj[adjIdx] + " " + sqNouns[nounIdx] + " " + strconv.Itoa(i+1),
		Category:       sqCategories[catIdx],
		UnitPriceCents: unitPrice,
	}
}

// genSQOrder generates order row i (0-based). Draw order is fixed: customer,
// year, month, day, status roll.
//
// The status roll is deliberately skewed (4/10 delivered, 3/10 shipped, 2/10
// placed, 1/10 cancelled) so a status filter is not a uniform 25% cut — that
// makes a question like F1's HAVING clause discriminate between statuses instead
// of behaving identically for all four. Day is capped at 28 so every
// (year, month, day) triple is a real date.
func genSQOrder(i int) sqOrder {
	r := newSQRand(sqStableSeed(fmt.Sprintf("sql|orders|%d", i)))
	customerID := r.intn(1, sqCustomerCount)
	year := r.intn(2023, 2025)
	month := r.intn(1, 12)
	day := r.intn(1, 28)
	roll := r.intn(0, 9)

	var status string
	switch {
	case roll <= 3:
		status = "delivered"
	case roll <= 6:
		status = "shipped"
	case roll <= 8:
		status = "placed"
	default:
		status = "cancelled"
	}

	return sqOrder{
		OrderID:    i + 1,
		CustomerID: customerID,
		OrderDate:  fmt.Sprintf("%04d-%02d-%02d", year, month, day),
		Status:     status,
	}
}

// genSQOrderItems generates the line items for order i (0-based), in insertion
// order.
//
// Product ids walk a stride of 7 from a random start, modulo the catalog size.
// With k <= 4 and a stride coprime to 40 the ids in one order are always
// distinct, so the (order_id, product_id) primary key never collides and no
// rejection loop is needed. price_cents copies the product's current
// unit_price_cents, which makes the F3 revenue question answerable from
// order_items alone OR through a join — a difference the weak model gets to
// discover for itself.
func genSQOrderItems(i int, products []sqProduct) []sqOrderItem {
	r := newSQRand(sqStableSeed(fmt.Sprintf("sql|order_items|%d", i)))
	k := r.intn(1, 4)
	first := r.intn(1, sqProductCount)

	items := make([]sqOrderItem, 0, k)
	for j := 0; j < k; j++ {
		quantity := r.intn(1, 5)
		productID := ((first - 1 + 7*j) % sqProductCount) + 1
		items = append(items, sqOrderItem{
			OrderID:    i + 1,
			ProductID:  productID,
			Quantity:   quantity,
			PriceCents: products[productID-1].UnitPriceCents,
		})
	}
	return items
}

// sqDataset is the whole generated database, in insertion order.
type sqDataset struct {
	Customers  []sqCustomer
	Products   []sqProduct
	Orders     []sqOrder
	OrderItems []sqOrderItem
}

// genSQDataset generates the complete dataset. Products are generated before
// order items because an item's price_cents is copied from its product.
func genSQDataset() sqDataset {
	ds := sqDataset{
		Customers:  make([]sqCustomer, 0, sqCustomerCount),
		Products:   make([]sqProduct, 0, sqProductCount),
		Orders:     make([]sqOrder, 0, sqOrderCount),
		OrderItems: make([]sqOrderItem, 0, sqOrderCount*2),
	}
	for i := 0; i < sqCustomerCount; i++ {
		ds.Customers = append(ds.Customers, genSQCustomer(i))
	}
	for i := 0; i < sqProductCount; i++ {
		ds.Products = append(ds.Products, genSQProduct(i))
	}
	for i := 0; i < sqOrderCount; i++ {
		ds.Orders = append(ds.Orders, genSQOrder(i))
	}
	for i := 0; i < sqOrderCount; i++ {
		ds.OrderItems = append(ds.OrderItems, genSQOrderItems(i, ds.Products)...)
	}
	return ds
}

// ─────────────────────────── rendering ───────────────────────────

// sqQuoteText renders a TEXT literal. The generated vocabulary contains no
// apostrophes, but the doubling rule is applied anyway so a future word list
// cannot produce invalid SQL silently.
func sqQuoteText(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sqInsertRow(table string, values []string) string {
	return fmt.Sprintf("INSERT INTO %s VALUES (%s);", table, strings.Join(values, ","))
}

func (c sqCustomer) insert() string {
	return sqInsertRow("customers", []string{
		strconv.Itoa(c.CustomerID), sqQuoteText(c.Name), sqQuoteText(c.City), strconv.Itoa(c.SignupYear)})
}

func (p sqProduct) insert() string {
	return sqInsertRow("products", []string{
		strconv.Itoa(p.ProductID), sqQuoteText(p.Name), sqQuoteText(p.Category), strconv.Itoa(p.UnitPriceCents)})
}

func (o sqOrder) insert() string {
	return sqInsertRow("orders", []string{
		strconv.Itoa(o.OrderID), strconv.Itoa(o.CustomerID), sqQuoteText(o.OrderDate), sqQuoteText(o.Status)})
}

func (it sqOrderItem) insert() string {
	return sqInsertRow("order_items", []string{
		strconv.Itoa(it.OrderID), strconv.Itoa(it.ProductID),
		strconv.Itoa(it.Quantity), strconv.Itoa(it.PriceCents)})
}

// sqInsertScript renders every row as an INSERT, in insertion order. It is both
// what builds the database and what the golden fingerprint is taken over, so a
// single crc32 pins the entire dataset.
func sqInsertScript(ds sqDataset) string {
	var b strings.Builder
	for _, c := range ds.Customers {
		b.WriteString(c.insert())
		b.WriteByte('\n')
	}
	for _, p := range ds.Products {
		b.WriteString(p.insert())
		b.WriteByte('\n')
	}
	for _, o := range ds.Orders {
		b.WriteString(o.insert())
		b.WriteByte('\n')
	}
	for _, it := range ds.OrderItems {
		b.WriteString(it.insert())
		b.WriteByte('\n')
	}
	return b.String()
}

// sqSampleRows renders the prompt's sample block: the first three rows of each
// table, in table order, as INSERT statements.
func sqSampleRows(ds sqDataset) string {
	lines := make([]string, 0, 4*sqSampleRowsPerTable)
	for i := 0; i < sqSampleRowsPerTable && i < len(ds.Customers); i++ {
		lines = append(lines, ds.Customers[i].insert())
	}
	for i := 0; i < sqSampleRowsPerTable && i < len(ds.Products); i++ {
		lines = append(lines, ds.Products[i].insert())
	}
	for i := 0; i < sqSampleRowsPerTable && i < len(ds.Orders); i++ {
		lines = append(lines, ds.Orders[i].insert())
	}
	for i := 0; i < sqSampleRowsPerTable && i < len(ds.OrderItems); i++ {
		lines = append(lines, ds.OrderItems[i].insert())
	}
	return strings.Join(lines, "\n")
}

// sqPromptTemplate is the task prompt, identical for both models and pinned
// verbatim. Task-oriented with no role framing, per project rules.
const sqPromptTemplate = `Generate one SQLite SELECT statement that answers the question below.

Schema:
%s

Sample rows (first 3 of each table):
%s

Question: %s

Reply with only the SQL statement in a ` + "```" + `sql fenced block. No explanation.`

// sqRenderPrompt builds the prompt for one question.
func sqRenderPrompt(ds sqDataset, question string) string {
	return fmt.Sprintf(sqPromptTemplate, sqDDL, sqSampleRows(ds), question)
}

// ─────────────────────────── the questions ───────────────────────────

// sqQuestion is one question plus the reference SQL that defines its truth.
//
// There is no expected-value field on purpose: the truth is whatever the
// reference SQL returns against the generated database, so truth and data cannot
// drift apart. Ordered records whether row order is part of the answer — it is
// exactly for the two families whose question text asks for an ordering.
type sqQuestion struct {
	Index   int    `json:"index"`
	Family  int    `json:"family"`
	Salt    int    `json:"salt"`
	Seed    uint32 `json:"seed"`
	Text    string `json:"question"`
	RefSQL  string `json:"reference_sql"`
	Ordered bool   `json:"ordered_comparison"`
	// K is the family parameter that bounds the expected row count (F1's HAVING
	// threshold, F4's LIMIT). Zero for families that have none.
	K int `json:"k,omitempty"`
}

// sqFamilyNames labels the families in reports.
var sqFamilyNames = []string{"filter+count", "group+having", "join2", "join3+agg", "top-n"}

// genSQQuestion builds candidate `index` at redraw `salt`. The salt is part of
// the seed label, so a redraw produces a genuinely different question rather
// than a perturbation of the same one.
//
// Draw order per family is fixed and must match the Python calibration probe.
func genSQQuestion(index, salt int) sqQuestion {
	family := index % sqQuestionFamilies
	seed := sqStableSeed(fmt.Sprintf("sql|question|%d|%d", index, salt))
	r := newSQRand(seed)
	q := sqQuestion{Index: index, Family: family, Salt: salt, Seed: seed}

	switch family {
	case 0: // filter + count
		city := sqCities[r.intn(0, len(sqCities)-1)]
		year := r.intn(2016, 2022)
		q.Text = fmt.Sprintf("How many customers from %s signed up in %d or later?", city, year)
		q.RefSQL = fmt.Sprintf(
			"SELECT COUNT(*) FROM customers WHERE city = '%s' AND signup_year >= %d;", city, year)

	case 1: // group by + HAVING, ordered
		status := sqStatuses[r.intn(0, len(sqStatuses)-1)]
		k := r.intn(2, 4)
		q.K = k
		q.Ordered = true
		q.Text = fmt.Sprintf("For orders with status '%s', how many orders has each customer placed? "+
			"Only include customers with at least %d such orders. "+
			"Return customer_id and the order count, ordered by customer_id ascending.", status, k)
		q.RefSQL = fmt.Sprintf("SELECT customer_id, COUNT(*) FROM orders WHERE status = '%s' "+
			"GROUP BY customer_id HAVING COUNT(*) >= %d ORDER BY customer_id;", status, k)

	case 2: // two-table join
		category := sqCategories[r.intn(0, len(sqCategories)-1)]
		q.Text = fmt.Sprintf("What is the total quantity of items ordered across all orders "+
			"for products in the '%s' category?", category)
		q.RefSQL = fmt.Sprintf("SELECT SUM(oi.quantity) FROM order_items oi "+
			"JOIN products p ON p.product_id = oi.product_id WHERE p.category = '%s';", category)

	case 3: // three-table join + aggregate
		city := sqCities[r.intn(0, len(sqCities)-1)]
		status := sqStatuses[r.intn(0, len(sqStatuses)-1)]
		q.Text = fmt.Sprintf("What is the total revenue in cents (sum of quantity times price_cents "+
			"over order_items) from orders with status '%s' placed by customers in %s?", status, city)
		q.RefSQL = fmt.Sprintf("SELECT SUM(oi.quantity * oi.price_cents) FROM customers c "+
			"JOIN orders o ON o.customer_id = c.customer_id "+
			"JOIN order_items oi ON oi.order_id = o.order_id "+
			"WHERE c.city = '%s' AND o.status = '%s';", city, status)

	case 4: // top-N, ordered
		k := r.intn(3, 5)
		q.K = k
		q.Ordered = true
		q.Text = fmt.Sprintf("Which %d products have the highest total quantity ordered? "+
			"Return product_id and total quantity, ordered by total quantity descending, "+
			"then by product_id ascending. Limit to %d rows.", k, k)
		// The product_id tie-break is in BOTH the SQL and the question wording.
		// Without it the top-N answer is not a function of the data (ties could
		// come back in any order) and an ordered comparison would be unsound.
		q.RefSQL = fmt.Sprintf("SELECT oi.product_id, SUM(oi.quantity) AS total_qty FROM order_items oi "+
			"GROUP BY oi.product_id ORDER BY total_qty DESC, oi.product_id ASC LIMIT %d;", k)
	}
	return q
}

// sqDegenerate reports whether a reference result makes the question useless.
//
// A degenerate question is one an arbitrary wrong query can satisfy: COUNT(*)=0
// is returned by any filter that matches nothing, a NULL SUM by any join that
// matches nothing, and an empty row set by any WHERE that excludes everything.
// Scoring against those would credit the model for failing in the right shape.
func sqDegenerate(q sqQuestion, res sqResult) bool {
	switch q.Family {
	case 0, 2, 3:
		if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
			return true
		}
		v := res.Rows[0][0]
		return v.Null || !v.IsNum || v.Num == 0
	case 1:
		return len(res.Rows) == 0
	case 4:
		return len(res.Rows) < q.K
	}
	return true
}

// sqBuildQuestions resolves questions 0..n-1 against a built database, redrawing
// with an incremented salt until each one is non-degenerate. It returns the
// questions and their reference results, so a caller never re-executes the
// reference SQL to learn the truth.
func sqBuildQuestions(ctx context.Context, dbPath string, n int) ([]sqQuestion, []sqResult, error) {
	questions := make([]sqQuestion, 0, n)
	refs := make([]sqResult, 0, n)
	for i := 0; i < n; i++ {
		var resolved bool
		for salt := 0; salt < sqMaxSalt; salt++ {
			q := genSQQuestion(i, salt)
			res, err := sqExecuteSQL(ctx, dbPath, q.RefSQL, sqRefQueryTimeout)
			if err != nil {
				return nil, nil, fmt.Errorf("question %d salt %d: reference SQL failed: %w", i, salt, err)
			}
			if sqDegenerate(q, res) {
				continue
			}
			questions = append(questions, q)
			refs = append(refs, res)
			resolved = true
			break
		}
		if !resolved {
			return nil, nil, fmt.Errorf("question %d: no non-degenerate draw within %d salts", i, sqMaxSalt)
		}
	}
	return questions, refs, nil
}

// ─────────────────────────── the database ───────────────────────────

const (
	// sqRefQueryTimeout bounds a reference query. Reference SQL is known-good, so
	// this only catches a pathological build.
	sqRefQueryTimeout = 30 * time.Second

	// sqModelQueryTimeout bounds a MODEL-generated query. This one is load-bearing:
	// a generated query can cross-join its way into a query that never finishes,
	// and an unbounded execution would hang the whole experiment. A timeout counts
	// as an execution failure, which is exactly how a real execution signal would
	// see it.
	sqModelQueryTimeout = 5 * time.Second

	// sqMaxResultRows caps how many rows a scored query may return. The largest
	// reference result is a per-customer group-by over 50 customers, so this is
	// two orders of magnitude of headroom; a runaway cross join hits it instead of
	// buffering millions of rows into the test process.
	sqMaxResultRows = 5000
)

// sqBuildDB creates the database at dbPath and populates it. It is called once
// per test run on a read-write connection; every scored query afterwards gets its
// own fresh READ-ONLY connection, so no model-generated statement can mutate the
// data another trial is scored against.
func sqBuildDB(ctx context.Context, dbPath string, ds sqDataset) error {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=rwc")
	if err != nil {
		return fmt.Errorf("opening %s for write: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	for _, stmt := range strings.Split(sqDDL, "\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating schema (%s): %w", stmt, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning load transaction: %w", err)
	}
	for _, stmt := range strings.Split(sqInsertScript(ds), "\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("loading row (%s): %w", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing load: %w", err)
	}
	return nil
}

// sqExecuteSQL runs one statement on a FRESH read-only connection and returns its
// result set.
//
// Read-only is the safety property that lets untrusted model output run at all:
// mode=ro makes any INSERT/UPDATE/DROP the model emits fail rather than corrupt
// the fixture. The context timeout is the liveness property. Only a single
// statement is executed — the parser has already reduced the model's reply to one.
func sqExecuteSQL(ctx context.Context, dbPath, query string, timeout time.Duration) (sqResult, error) {
	stmt := strings.TrimSuffix(strings.TrimSpace(query), ";")
	if stmt == "" {
		return sqResult{}, fmt.Errorf("empty statement")
	}

	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return sqResult{}, fmt.Errorf("opening %s read-only: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()

	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rows, err := db.QueryContext(qctx, stmt)
	if err != nil {
		return sqResult{}, err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return sqResult{}, err
	}
	res := sqResult{Cols: len(cols)}
	for rows.Next() {
		if len(res.Rows) >= sqMaxResultRows {
			return sqResult{}, fmt.Errorf("result exceeds %d rows", sqMaxResultRows)
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return sqResult{}, err
		}
		row := make([]sqValue, len(cols))
		for i, v := range raw {
			row[i] = sqNormalizeValue(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return sqResult{}, err
	}
	return res, nil
}

// ─────────────────────────── result comparison ───────────────────────────

// sqValue is one normalized cell: NULL, numeric, or text. Normalizing at scan
// time means the comparison never has to know which driver type a column came
// back as.
type sqValue struct {
	Null  bool    `json:"null,omitempty"`
	IsNum bool    `json:"is_num,omitempty"`
	Num   float64 `json:"num,omitempty"`
	Text  string  `json:"text,omitempty"`
}

func sqNormalizeValue(v any) sqValue {
	switch t := v.(type) {
	case nil:
		return sqValue{Null: true}
	case int64:
		return sqValue{IsNum: true, Num: float64(t)}
	case float64:
		return sqValue{IsNum: true, Num: t}
	case bool:
		if t {
			return sqValue{IsNum: true, Num: 1}
		}
		return sqValue{IsNum: true, Num: 0}
	case []byte:
		return sqValue{Text: string(t)}
	case string:
		return sqValue{Text: t}
	case time.Time:
		return sqValue{Text: t.UTC().Format(time.RFC3339Nano)}
	default:
		return sqValue{Text: fmt.Sprint(t)}
	}
}

// sqValueEqual compares two cells: NULL equals NULL, numbers compare with a
// tolerance, text compares exactly, and different kinds are never equal.
//
// The tolerance is relative-with-a-floor rather than absolute so a cents total in
// the millions is not held to absolute 1e-9. Every value in this dataset is an
// integer, so the tolerance only ever absorbs a float round-trip (SUM returning
// 1234.0 where the reference returned 1234).
func sqValueEqual(a, b sqValue) bool {
	switch {
	case a.Null || b.Null:
		return a.Null && b.Null
	case a.IsNum != b.IsNum:
		return false
	case a.IsNum:
		scale := math.Max(1, math.Max(math.Abs(a.Num), math.Abs(b.Num)))
		return math.Abs(a.Num-b.Num) <= 1e-9*scale
	default:
		return a.Text == b.Text
	}
}

// sqResult is a normalized result set: a column count plus rows of cells.
type sqResult struct {
	Cols int         `json:"cols"`
	Rows [][]sqValue `json:"rows"`
}

// sqRowKey is the canonical sort key for a row, used to compare unordered result
// sets as multisets. The kind tag keeps NULL, 0 and "0" from collapsing onto each
// other, and 'g'/17 is exact for float64.
func sqRowKey(row []sqValue) string {
	parts := make([]string, 0, len(row))
	for _, v := range row {
		switch {
		case v.Null:
			parts = append(parts, "n:")
		case v.IsNum:
			parts = append(parts, "d:"+strconv.FormatFloat(v.Num, 'g', 17, 64))
		default:
			parts = append(parts, "t:"+v.Text)
		}
	}
	return strings.Join(parts, "\x1f")
}

// sqResultEqual compares a candidate result against the reference.
//
// Column count must match, and then either row order matters (the two families
// whose question text asks for an ordering) or the rows are compared as a
// multiset by canonical key. Multiset — not set — because a duplicate row is a
// different answer from a single row.
func sqResultEqual(ref, got sqResult, ordered bool) bool {
	if ref.Cols != got.Cols || len(ref.Rows) != len(got.Rows) {
		return false
	}
	refRows, gotRows := ref.Rows, got.Rows
	if !ordered {
		refRows = sqSortedRows(refRows)
		gotRows = sqSortedRows(gotRows)
	}
	for i := range refRows {
		if len(refRows[i]) != len(gotRows[i]) {
			return false
		}
		for j := range refRows[i] {
			if !sqValueEqual(refRows[i][j], gotRows[i][j]) {
				return false
			}
		}
	}
	return true
}

// sqSortedRows returns a copy of rows sorted by canonical key. The input slice is
// never reordered, so a reference result can be compared many times.
func sqSortedRows(rows [][]sqValue) [][]sqValue {
	out := make([][]sqValue, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool { return sqRowKey(out[i]) < sqRowKey(out[j]) })
	return out
}

// sqRenderResult renders a result compactly for logs and for the per-question
// grid. Long results are elided — the JSONL keeps everything.
func sqRenderResult(res sqResult) string {
	const maxRows = 6
	parts := make([]string, 0, maxRows+1)
	for i, row := range res.Rows {
		if i == maxRows {
			parts = append(parts, fmt.Sprintf("…+%d", len(res.Rows)-maxRows))
			break
		}
		cells := make([]string, 0, len(row))
		for _, v := range row {
			switch {
			case v.Null:
				cells = append(cells, "NULL")
			case v.IsNum:
				cells = append(cells, strconv.FormatFloat(v.Num, 'g', -1, 64))
			default:
				cells = append(cells, v.Text)
			}
		}
		parts = append(parts, "("+strings.Join(cells, ",")+")")
	}
	if len(parts) == 0 {
		return "<0 rows>"
	}
	return strings.Join(parts, " ")
}

// ─────────────────────────── SQL extraction ───────────────────────────

// sqFenceRE matches a fenced block, with or without a language tag. Non-greedy so
// a reply with several blocks yields several matches rather than one giant one.
var sqFenceRE = regexp.MustCompile("(?s)```[a-zA-Z]*[ \t]*\r?\n?(.*?)```")

// sqParseSQL extracts the single SELECT statement to execute from a model reply.
//
// Order, and why each step exists:
//
//  1. Strip <think>…</think>. A reasoning model's scratchpad routinely contains
//     several draft queries; scoring one of those instead of the final answer
//     would measure the wrong thing. thinkTagRE is shared with the arithmetic
//     harness.
//  2. Prefer fenced blocks. The prompt asks for one, and prose outside the fence
//     ("Here is the query…") is not SQL.
//  3. Split on ";" and keep the fragments that mention SELECT, then take the
//     LAST. Weak models emit the schema DDL again, or a scratch query followed by
//     the real one; the last SELECT is the model's final commitment. This also
//     guarantees the executed string is a SINGLE statement — the semicolons are
//     gone by construction, so a multi-statement reply cannot smuggle a second
//     statement past the read-only connection.
//  4. No SELECT anywhere → parse failure, scored exec_error.
func sqParseSQL(output string) (string, bool) {
	text := thinkTagRE.ReplaceAllString(output, "")

	candidate := text
	if blocks := sqFenceRE.FindAllStringSubmatch(text, -1); len(blocks) > 0 {
		contents := make([]string, 0, len(blocks))
		for _, b := range blocks {
			contents = append(contents, b[1])
		}
		candidate = strings.Join(contents, "\n")
	}

	best := ""
	for _, frag := range strings.Split(candidate, ";") {
		if strings.Contains(strings.ToLower(frag), "select") {
			best = strings.TrimSpace(frag)
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// ─────────────────────────── scoring ───────────────────────────

// The three outcome categories. The middle one is the reason this experiment
// exists: it is invisible to an execution signal, so no amount of escalation
// driven by one can repair it.
const (
	sqCatExecError   = "exec_error"
	sqCatSilentWrong = "silent_wrong"
	sqCatCorrect     = "correct"
)

// sqOutcome is the scored result of one model reply.
type sqOutcome struct {
	SQL      string `json:"sql"`
	Parsed   bool   `json:"parsed"`
	ExecOK   bool   `json:"exec_ok"`
	ExecErr  string `json:"exec_error,omitempty"`
	Correct  bool   `json:"correct"`
	Category string `json:"category"`
	Rows     int    `json:"rows"`
	Result   string `json:"result,omitempty"`
}

// sqScore scores one model reply against a question's reference result. It runs
// OUTSIDE the executor and outside the judge, so every arm — leveled or not — is
// measured by exactly the same yardstick.
func sqScore(ctx context.Context, dbPath string, q sqQuestion, ref sqResult, output string) sqOutcome {
	stmt, ok := sqParseSQL(output)
	if !ok {
		return sqOutcome{Category: sqCatExecError, ExecErr: sqNoSQLFound}
	}
	res, err := sqExecuteSQL(ctx, dbPath, stmt, sqModelQueryTimeout)
	if err != nil {
		return sqOutcome{SQL: stmt, Parsed: true, Category: sqCatExecError, ExecErr: err.Error()}
	}
	out := sqOutcome{
		SQL: stmt, Parsed: true, ExecOK: true,
		Rows: len(res.Rows), Result: sqRenderResult(res),
	}
	if sqResultEqual(ref, res, q.Ordered) {
		out.Correct = true
		out.Category = sqCatCorrect
	} else {
		out.Category = sqCatSilentWrong
	}
	return out
}

// sqNoSQLFound is the parse-failure reason. It is also what the execution judge
// reports, and like every other judge reason it names no reference answer.
const sqNoSQLFound = "no SQL statement found"

// ─────────────────────────── golden fingerprints ───────────────────────────

// These constants were computed once from the generator above and hardcoded, so
// any change to a word list, a draw order or a row count fails
// TestSQDatasetGoldenChecksums instead of silently regenerating the database
// under an experiment whose results were recorded against the old one. They are
// also the cross-check against the parallel Python calibration probe: if its
// numbers differ, the two are not measuring the same data.
const (
	sqGoldenInsertScriptCRC32 = 0x916a97d5

	sqGoldenCustomerRows  = 50
	sqGoldenProductRows   = 40
	sqGoldenOrderRows     = 300
	sqGoldenOrderItemRows = 728

	sqGoldenSignupYearSum     = 100913
	sqGoldenUnitPriceCentsSum = 190963
	sqGoldenQuantitySum       = 2162
	sqGoldenPriceCentsSum     = 3496505

	sqGoldenStatusPlaced    = 43
	sqGoldenStatusShipped   = 87
	sqGoldenStatusDelivered = 131
	sqGoldenStatusCancelled = 39
)

// ─────────────────────────── tests (no network) ───────────────────────────

// sqTestDB builds the fixture once for a test and returns its path.
func sqTestDB(t *testing.T) (string, sqDataset) {
	t.Helper()
	ds := genSQDataset()
	path := t.TempDir() + "/sqlgen.db"
	if err := sqBuildDB(context.Background(), path, ds); err != nil {
		t.Fatalf("building fixture database: %v", err)
	}
	return path, ds
}

// TestSQDatasetDeterminism pins the pure-function property the whole experiment
// rests on: generating twice yields identical rows, and the seed derivation
// itself is pinned so a dataset rebuilt from a committed JSONL row can be checked
// against it.
func TestSQDatasetDeterminism(t *testing.T) {
	a, b := genSQDataset(), genSQDataset()

	if len(a.Customers) != len(b.Customers) || len(a.Products) != len(b.Products) ||
		len(a.Orders) != len(b.Orders) || len(a.OrderItems) != len(b.OrderItems) {
		t.Fatalf("row counts differ between generations")
	}
	for i := range a.Customers {
		if a.Customers[i] != b.Customers[i] {
			t.Fatalf("customer %d is not deterministic: %+v vs %+v", i, a.Customers[i], b.Customers[i])
		}
	}
	for i := range a.Products {
		if a.Products[i] != b.Products[i] {
			t.Fatalf("product %d is not deterministic: %+v vs %+v", i, a.Products[i], b.Products[i])
		}
	}
	for i := range a.Orders {
		if a.Orders[i] != b.Orders[i] {
			t.Fatalf("order %d is not deterministic: %+v vs %+v", i, a.Orders[i], b.Orders[i])
		}
	}
	for i := range a.OrderItems {
		if a.OrderItems[i] != b.OrderItems[i] {
			t.Fatalf("order_item %d is not deterministic: %+v vs %+v", i, a.OrderItems[i], b.OrderItems[i])
		}
	}
	if sqInsertScript(a) != sqInsertScript(b) {
		t.Fatalf("rendered INSERT script is not deterministic")
	}

	if got, want := sqStableSeed("sql|customers|0"), crc32.ChecksumIEEE([]byte("sql|customers|0")); got != want {
		t.Fatalf("sqStableSeed(%q) = %d, want %d", "sql|customers|0", got, want)
	}

	// Questions are deterministic per (index, salt) too — the salt is part of the
	// seed label, so a redraw is a different question and not a perturbation.
	for _, idx := range []int{0, 1, 2, 3, 4, 17, 29} {
		for _, salt := range []int{0, 1, 3} {
			if genSQQuestion(idx, salt) != genSQQuestion(idx, salt) {
				t.Fatalf("question (%d, salt %d) is not deterministic", idx, salt)
			}
		}
		if genSQQuestion(idx, 0) == genSQQuestion(idx, 1) {
			t.Errorf("question %d salt 0 and salt 1 are identical; the redraw does nothing", idx)
		}
	}
}

// TestSQDatasetGoldenChecksums pins the dataset itself. Every constant here was
// computed from the generator and hardcoded; a diff means the data changed, which
// invalidates any recorded experiment run and any Python probe calibrated
// against it.
func TestSQDatasetGoldenChecksums(t *testing.T) {
	ds := genSQDataset()

	if got := crc32.ChecksumIEEE([]byte(sqInsertScript(ds))); got != sqGoldenInsertScriptCRC32 {
		t.Errorf("INSERT script crc32 = 0x%08x, want 0x%08x — the dataset changed", got, sqGoldenInsertScriptCRC32)
	}

	type check struct {
		name      string
		got, want int
	}

	var signupSum int
	for _, c := range ds.Customers {
		signupSum += c.SignupYear
	}
	var priceSum int
	for _, p := range ds.Products {
		priceSum += p.UnitPriceCents
	}
	var qtySum, centsSum int
	for _, it := range ds.OrderItems {
		qtySum += it.Quantity
		centsSum += it.PriceCents
	}
	byStatus := map[string]int{}
	for _, o := range ds.Orders {
		byStatus[o.Status]++
	}
	checks := []check{
		{"customers rows", len(ds.Customers), sqGoldenCustomerRows},
		{"products rows", len(ds.Products), sqGoldenProductRows},
		{"orders rows", len(ds.Orders), sqGoldenOrderRows},
		{"order_items rows", len(ds.OrderItems), sqGoldenOrderItemRows},
		{"SUM(signup_year)", signupSum, sqGoldenSignupYearSum},
		{"SUM(unit_price_cents)", priceSum, sqGoldenUnitPriceCentsSum},
		{"SUM(quantity)", qtySum, sqGoldenQuantitySum},
		{"SUM(price_cents)", centsSum, sqGoldenPriceCentsSum},
		{"orders status=placed", byStatus["placed"], sqGoldenStatusPlaced},
		{"orders status=shipped", byStatus["shipped"], sqGoldenStatusShipped},
		{"orders status=delivered", byStatus["delivered"], sqGoldenStatusDelivered},
		{"orders status=cancelled", byStatus["cancelled"], sqGoldenStatusCancelled},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	// Every order has between 1 and 4 items, all with distinct product ids, and
	// every item's price matches its product's unit price. These are structural
	// guarantees the generator's stride trick provides; if the stride or the
	// catalog size ever changes, the (order_id, product_id) primary key starts
	// colliding and the load fails instead of the data being subtly wrong.
	perOrder := map[int]map[int]bool{}
	for _, it := range ds.OrderItems {
		if perOrder[it.OrderID] == nil {
			perOrder[it.OrderID] = map[int]bool{}
		}
		if perOrder[it.OrderID][it.ProductID] {
			t.Fatalf("order %d has duplicate product %d", it.OrderID, it.ProductID)
		}
		perOrder[it.OrderID][it.ProductID] = true
		if want := ds.Products[it.ProductID-1].UnitPriceCents; it.PriceCents != want {
			t.Errorf("order %d item %d: price_cents=%d, want the product's %d",
				it.OrderID, it.ProductID, it.PriceCents, want)
		}
	}
	if len(perOrder) != sqOrderCount {
		t.Errorf("%d orders have items, want all %d", len(perOrder), sqOrderCount)
	}
	for id, items := range perOrder {
		if len(items) < 1 || len(items) > 4 {
			t.Errorf("order %d has %d items, want 1..4", id, len(items))
		}
	}

	t.Logf("dataset: %d customers, %d products, %d orders, %d order_items, crc32=0x%08x",
		len(ds.Customers), len(ds.Products), len(ds.Orders), len(ds.OrderItems),
		crc32.ChecksumIEEE([]byte(sqInsertScript(ds))))
	t.Logf("orders by status: placed=%d shipped=%d delivered=%d cancelled=%d",
		byStatus["placed"], byStatus["shipped"], byStatus["delivered"], byStatus["cancelled"])
}

// TestSQQuestionsNonDegenerate is the question-set guarantee: every reference
// result is non-empty, non-NULL and non-zero, and a top-N question really has k
// rows. A degenerate question would be satisfiable by a query that matches
// nothing, which would credit the model for the right kind of failure.
func TestSQQuestionsNonDegenerate(t *testing.T) {
	dbPath, _ := sqTestDB(t)
	ctx := context.Background()

	questions, refs, err := sqBuildQuestions(ctx, dbPath, 30)
	if err != nil {
		t.Fatalf("building questions: %v", err)
	}
	if len(questions) != 30 {
		t.Fatalf("got %d questions, want 30", len(questions))
	}

	byFamily := map[int]int{}
	for i, q := range questions {
		byFamily[q.Family]++
		if sqDegenerate(q, refs[i]) {
			t.Errorf("question %d (family %d, salt %d) is degenerate: %s",
				q.Index, q.Family, q.Salt, sqRenderResult(refs[i]))
		}
		if q.Text == "" || q.RefSQL == "" {
			t.Fatalf("question %d has empty text or reference SQL", q.Index)
		}
		if !strings.Contains(strings.ToUpper(q.RefSQL), "SELECT") {
			t.Errorf("question %d reference SQL is not a SELECT: %s", q.Index, q.RefSQL)
		}
		if q.Family == 4 && len(refs[i].Rows) != q.K {
			t.Errorf("question %d (top-%d) returned %d rows, want exactly %d",
				q.Index, q.K, len(refs[i].Rows), q.K)
		}
		if q.Family == 1 {
			// Every returned group must actually clear the HAVING threshold, and
			// there must be at least one — otherwise the HAVING clause is not
			// discriminating and the question is easier than it looks.
			for _, row := range refs[i].Rows {
				if len(row) != 2 || !row[1].IsNum || int(row[1].Num) < q.K {
					t.Errorf("question %d: row %s violates HAVING >= %d",
						q.Index, sqRowKey(row), q.K)
				}
			}
		}
		if i < 5 {
			t.Logf("q%d family=%d(%s) salt=%d ordered=%t\n  Q: %s\n  SQL: %s\n  REF: %s",
				q.Index, q.Family, sqFamilyNames[q.Family], q.Salt, q.Ordered,
				q.Text, q.RefSQL, sqRenderResult(refs[i]))
		}
	}
	for f := 0; f < sqQuestionFamilies; f++ {
		if byFamily[f] != 6 {
			t.Errorf("family %d appears %d times over 30 questions, want 6", f, byFamily[f])
		}
	}
}

// TestSQOrderedQuestionsDeterministic pins the ordering property the two ordered
// families depend on: the reference result is stable across executions, and for
// top-N the (total_qty DESC, product_id ASC) sort is strict — no two rows can
// legally swap. Without the product_id tie-break a top-N answer would not be a
// function of the data and an ordered comparison would fail at random.
func TestSQOrderedQuestionsDeterministic(t *testing.T) {
	dbPath, _ := sqTestDB(t)
	ctx := context.Background()

	questions, refs, err := sqBuildQuestions(ctx, dbPath, 10)
	if err != nil {
		t.Fatalf("building questions: %v", err)
	}

	for i, q := range questions {
		if !q.Ordered {
			continue
		}
		for rep := 0; rep < 5; rep++ {
			again, err := sqExecuteSQL(ctx, dbPath, q.RefSQL, sqRefQueryTimeout)
			if err != nil {
				t.Fatalf("question %d re-execution %d failed: %v", q.Index, rep, err)
			}
			if !sqResultEqual(refs[i], again, true) {
				t.Fatalf("question %d reference result is not order-stable: %s vs %s",
					q.Index, sqRenderResult(refs[i]), sqRenderResult(again))
			}
		}

		switch q.Family {
		case 1:
			// customer_id ascending, strictly — customer_id is the group key so
			// duplicates are impossible.
			for r := 1; r < len(refs[i].Rows); r++ {
				if refs[i].Rows[r-1][0].Num >= refs[i].Rows[r][0].Num {
					t.Errorf("question %d rows %d/%d are not strictly ascending by customer_id",
						q.Index, r-1, r)
				}
			}
		case 4:
			for r := 1; r < len(refs[i].Rows); r++ {
				prev, cur := refs[i].Rows[r-1], refs[i].Rows[r]
				prevQty, curQty := prev[1].Num, cur[1].Num
				prevID, curID := prev[0].Num, cur[0].Num
				if prevQty < curQty || (prevQty == curQty && prevID >= curID) {
					t.Errorf("question %d rows %d/%d violate (qty DESC, product_id ASC): %v/%v vs %v/%v",
						q.Index, r-1, r, prevQty, prevID, curQty, curID)
				}
			}
		}
	}
}

// TestSQParseSQL pins the extraction rules. Each case is a real failure mode
// observed from a local model, not a hypothetical.
func TestSQParseSQL(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{
			name:   "sql fenced block",
			input:  "Here you go:\n```sql\nSELECT COUNT(*) FROM customers;\n```\n",
			want:   "SELECT COUNT(*) FROM customers",
			wantOK: true,
		},
		{
			name:   "bare fenced block without a language tag",
			input:  "```\nSELECT 1 FROM orders\n```",
			want:   "SELECT 1 FROM orders",
			wantOK: true,
		},
		{
			name:   "no fence at all, prose around the statement",
			input:  "The query is SELECT SUM(quantity) FROM order_items; that should do it.",
			want:   "The query is SELECT SUM(quantity) FROM order_items",
			wantOK: true,
		},
		{
			name:   "think block is stripped before parsing",
			input:  "<think>Maybe SELECT * FROM products; no, wrong table</think>\n```sql\nSELECT COUNT(*) FROM orders;\n```",
			want:   "SELECT COUNT(*) FROM orders",
			wantOK: true,
		},
		{
			name: "multi-statement reply yields exactly one statement (the last SELECT)",
			input: "```sql\nSELECT 1 FROM customers;\nSELECT 2 FROM products;\n```" +
				"\nand a stray DROP TABLE customers;",
			want:   "SELECT 2 FROM products",
			wantOK: true,
		},
		{
			name:   "prose before the fence is ignored in favour of the fence",
			input:  "I would run SELECT * FROM nothing;\n```sql\nSELECT city FROM customers;\n```",
			want:   "SELECT city FROM customers",
			wantOK: true,
		},
		{
			name:   "several fenced blocks: the last SELECT wins",
			input:  "```sql\nSELECT a FROM t;\n```\ntext\n```sql\nSELECT b FROM u;\n```",
			want:   "SELECT b FROM u",
			wantOK: true,
		},
		{
			name:   "lowercase select is still a select",
			input:  "```sql\nselect count(*) from orders\n```",
			want:   "select count(*) from orders",
			wantOK: true,
		},
		{
			name:   "no SELECT anywhere is a parse failure",
			input:  "```sql\nDROP TABLE customers;\n```",
			wantOK: false,
		},
		{
			name:   "empty reply is a parse failure",
			input:  "",
			wantOK: false,
		},
		{
			name:   "only a think block is a parse failure",
			input:  "<think>SELECT 1 FROM customers</think>",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sqParseSQL(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("sqParseSQL() ok = %t, want %t (got %q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("sqParseSQL() = %q, want %q", got, tc.want)
			}
			// Whatever comes out must be executable as a SINGLE statement.
			if ok && strings.Contains(got, ";") {
				t.Errorf("sqParseSQL() returned more than one statement: %q", got)
			}
		})
	}
}

// TestSQResultEqual pins the comparison rules that decide correct vs
// silent_wrong. A bug here would either credit wrong queries or convict right
// ones, and both would invalidate the whole experiment.
func TestSQResultEqual(t *testing.T) {
	num := func(vs ...float64) []sqValue {
		row := make([]sqValue, 0, len(vs))
		for _, v := range vs {
			row = append(row, sqValue{IsNum: true, Num: v})
		}
		return row
	}
	text := func(s string) sqValue { return sqValue{Text: s} }
	null := sqValue{Null: true}

	tests := []struct {
		name    string
		ref     sqResult
		got     sqResult
		ordered bool
		want    bool
	}{
		{
			name: "identical scalars",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(7)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(7)}},
			want: true,
		},
		{
			name: "int and its float round-trip are equal",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(1234)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(1234.0000000001)}},
			want: true,
		},
		{
			name: "different scalars are not equal",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(7)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(8)}},
			want: false,
		},
		{
			name: "column count mismatch",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(7)}},
			got:  sqResult{Cols: 2, Rows: [][]sqValue{num(7, 7)}},
			want: false,
		},
		{
			name: "row count mismatch",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(1), num(2)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(1)}},
			want: false,
		},
		{
			name: "unordered compare ignores row order",
			ref:  sqResult{Cols: 2, Rows: [][]sqValue{num(1, 10), num(2, 20)}},
			got:  sqResult{Cols: 2, Rows: [][]sqValue{num(2, 20), num(1, 10)}},
			want: true,
		},
		{
			name:    "ordered compare does not ignore row order",
			ref:     sqResult{Cols: 2, Rows: [][]sqValue{num(1, 10), num(2, 20)}},
			got:     sqResult{Cols: 2, Rows: [][]sqValue{num(2, 20), num(1, 10)}},
			ordered: true,
			want:    false,
		},
		{
			name: "multiset, not set: duplicates count",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(5), num(5)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(5), num(6)}},
			want: false,
		},
		{
			name: "NULL equals NULL",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{{null}}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{{null}}},
			want: true,
		},
		{
			name: "NULL does not equal zero",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{{null}}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{num(0)}},
			want: false,
		},
		{
			name: "text compares exactly",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{{text("Austin")}}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{{text("Austin")}}},
			want: true,
		},
		{
			name: "text is case sensitive",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{{text("Austin")}}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{{text("austin")}}},
			want: false,
		},
		{
			name: "a number is not the text of that number",
			ref:  sqResult{Cols: 1, Rows: [][]sqValue{num(7)}},
			got:  sqResult{Cols: 1, Rows: [][]sqValue{{text("7")}}},
			want: false,
		},
		{
			name: "empty results are equal",
			ref:  sqResult{Cols: 2},
			got:  sqResult{Cols: 2},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqResultEqual(tc.ref, tc.got, tc.ordered); got != tc.want {
				t.Errorf("sqResultEqual(ordered=%t) = %t, want %t", tc.ordered, got, tc.want)
			}
			// Comparison must not reorder its inputs: a reference result is
			// compared once per arm per rung.
			if !tc.ordered && len(tc.ref.Rows) > 1 {
				before := sqRowKey(tc.ref.Rows[0])
				_ = sqResultEqual(tc.ref, tc.got, false)
				if after := sqRowKey(tc.ref.Rows[0]); before != after {
					t.Errorf("sqResultEqual mutated the reference row order")
				}
			}
		})
	}
}

// TestSQScoreCategories checks the three-way scoring against the real fixture:
// a parse failure and a broken query are exec_error, the reference SQL itself is
// correct, and a query that runs but answers a different question is
// silent_wrong. That last case is the one arms 2 and 3 cannot see.
func TestSQScoreCategories(t *testing.T) {
	dbPath, _ := sqTestDB(t)
	ctx := context.Background()

	questions, refs, err := sqBuildQuestions(ctx, dbPath, 5)
	if err != nil {
		t.Fatalf("building questions: %v", err)
	}
	q0, ref0 := questions[0], refs[0]

	cases := []struct {
		name     string
		output   string
		wantCat  string
		wantExec bool
	}{
		{"reference SQL scores correct", "```sql\n" + q0.RefSQL + "\n```", sqCatCorrect, true},
		{"no SQL at all is exec_error", "I cannot answer that.", sqCatExecError, false},
		{"nonexistent table is exec_error", "```sql\nSELECT * FROM nope;\n```", sqCatExecError, false},
		{"syntax error is exec_error", "```sql\nSELECT FROM WHERE customers;\n```", sqCatExecError, false},
		{"a trailing write statement is dropped by the parser, not executed",
			"```sql\nSELECT 1; DELETE FROM customers;\n```", sqCatSilentWrong, true},
		{"runs but answers a different question is silent_wrong",
			"```sql\nSELECT COUNT(*) FROM customers;\n```", sqCatSilentWrong, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := sqScore(ctx, dbPath, q0, ref0, tc.output)
			if out.Category != tc.wantCat {
				t.Errorf("category = %q, want %q (err=%q sql=%q)",
					out.Category, tc.wantCat, out.ExecErr, out.SQL)
			}
			if out.ExecOK != tc.wantExec {
				t.Errorf("exec_ok = %t, want %t (err=%q)", out.ExecOK, tc.wantExec, out.ExecErr)
			}
			if out.Category != sqCatCorrect && out.Correct {
				t.Errorf("non-correct category %q must not set Correct", out.Category)
			}
		})
	}

	// A judge reason must never leak the reference answer — the escalation prompt
	// is built from it, and a leaked answer would turn the escalation rung into a
	// copying exercise. The only reasons this harness produces are sqlite error
	// text and sqNoSQLFound.
	bad := sqScore(ctx, dbPath, q0, ref0, "no sql here")
	if bad.ExecErr != sqNoSQLFound {
		t.Errorf("parse-failure reason = %q, want %q", bad.ExecErr, sqNoSQLFound)
	}
	refRendered := sqRenderResult(ref0)
	for _, tc := range cases {
		out := sqScore(ctx, dbPath, q0, ref0, tc.output)
		if out.ExecErr != "" && strings.Contains(out.ExecErr, refRendered) {
			t.Errorf("execution error leaks the reference result: %q", out.ExecErr)
		}
	}
}

// TestSQReadOnlyConnectionRefusesWrites is the second half of the containment
// story. The parser already reduces a reply to one SELECT fragment, so a write
// normally never reaches the database; this test checks the belt as well as the
// braces, by handing a write DIRECTLY to the executor. mode=ro must refuse it, so
// even a parser bug cannot let model output mutate the fixture that every other
// trial is scored against.
func TestSQReadOnlyConnectionRefusesWrites(t *testing.T) {
	dbPath, _ := sqTestDB(t)
	ctx := context.Background()

	for _, stmt := range []string{
		"DELETE FROM customers",
		"UPDATE customers SET city = 'Nowhere'",
		"INSERT INTO customers VALUES (999,'X','Y',2000)",
		"DROP TABLE customers",
	} {
		if _, err := sqExecuteSQL(ctx, dbPath, stmt, sqModelQueryTimeout); err == nil {
			t.Errorf("%q succeeded on a read-only connection, want an error", stmt)
		}
	}

	// And the fixture is untouched.
	res, err := sqExecuteSQL(ctx, dbPath, "SELECT COUNT(*) FROM customers", sqRefQueryTimeout)
	if err != nil {
		t.Fatalf("counting customers: %v", err)
	}
	if len(res.Rows) != 1 || int(res.Rows[0][0].Num) != sqCustomerCount {
		t.Errorf("customers count = %s, want %d", sqRenderResult(res), sqCustomerCount)
	}

	// The parser is the first line of defence: a reply that appends a write after
	// a SELECT yields only the SELECT.
	got, ok := sqParseSQL("```sql\nSELECT 1; DELETE FROM customers;\n```")
	if !ok || got != "SELECT 1" {
		t.Errorf("sqParseSQL() = %q (ok=%t), want %q — the write must be dropped", got, ok, "SELECT 1")
	}
}

// TestSQLevelingArmSemantics pins the executor mechanics arms 2 and 3 of the live
// experiment are built on, WITHOUT a model: stub rungs stand in for llama3.2, and
// the real execution judge runs against the real fixture.
//
// It is the non-live proof of H1's mechanism. The interesting case is the second
// one: a first attempt that executes but answers the wrong question passes the
// judge, so MaxEscalations=2 buys exactly zero escalations and the wrong answer is
// returned as a leveling PASS. No model behaviour is involved — it follows from
// the executor consulting the judge and the judge being an execution check.
//
// It also pins the two properties the escalation prompt must have: it carries the
// sqlite error and the failed query, and it carries no part of the reference
// answer.
func TestSQLevelingArmSemantics(t *testing.T) {
	dbPath, ds := sqTestDB(t)
	ctx := context.Background()

	questions, refs, err := sqBuildQuestions(ctx, dbPath, 1)
	if err != nil {
		t.Fatalf("building questions: %v", err)
	}
	q, ref := questions[0], refs[0]

	// The rungs' provider/model must classify as a non-short-circuiting tier or
	// the executor skips the judge entirely — the same precondition the live
	// harness asserts against the discovered Ollama catalog.
	const provider, model = "ollama", "llama3.2"
	if got := catalog.TierOf(provider, model); got != catalog.TierLocal {
		t.Fatalf("precondition failed: TierOf(%s, %s) = %v, want %v", provider, model, got, catalog.TierLocal)
	}

	// A schema-less policy with a feedback template: exactly the live harness's
	// contract. Without the template the escalation prompt would drop the previous
	// output; without the missing schema the judge would never be consulted.
	policy := &loomv1.OutputPolicy{
		RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 0, FeedbackTemplate: sqCritiqueTemplate},
	}

	// stubLadder builds len(outputs) rungs, the i-th returning outputs[i] and
	// recording the prompt it was called with.
	type stub struct {
		prompts []string
	}
	newStubLadder := func(rec *stub, outputs ...string) []LevelingRung {
		rungs := make([]LevelingRung, 0, len(outputs))
		for i := range outputs {
			out := outputs[i]
			rungs = append(rungs, LevelingRung{
				Provider: provider, Model: model,
				Execute: func(_ context.Context, _ string, prompt string) (*loomv1.AgentResult, error) {
					rec.prompts = append(rec.prompts, prompt)
					return &loomv1.AgentResult{AgentId: "sq-stub", Output: out}, nil
				},
			})
		}
		return rungs
	}

	fenced := func(sqlText string) string { return "```sql\n" + sqlText + "\n```" }
	brokenSQL := fenced("SELECT * FROM no_such_table;")
	wrongSQL := fenced("SELECT COUNT(*) FROM products;")
	rightSQL := fenced(q.RefSQL)
	basePrompt := sqRenderPrompt(ds, q.Text)

	t.Run("exec_error escalates and a later rung's output is taken", func(t *testing.T) {
		arm := &sqArm{name: "stub", dbPath: dbPath, question: q, ref: ref}
		exec := NewLevelingExecutor(nil, &LevelingPolicy{
			Enabled: true, ShortCircuitMid: true, MaxEscalations: 2, Judge: arm.executionJudge(),
		}, nil, nil)
		rec := &stub{}

		result, report, err := exec.Execute(ctx, policy,
			newStubLadder(rec, brokenSQL, brokenSQL, rightSQL), basePrompt, "wf-sq-esc")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if report == nil {
			t.Fatal("leveling report is nil; leveling must be enabled for this arm")
		}
		if report.ShortCircuited {
			t.Fatalf("primary rung short-circuited; the judge never ran")
		}
		if report.Escalations != 2 {
			t.Errorf("escalations = %d, want 2", report.Escalations)
		}
		if !report.Passed {
			t.Errorf("report.Passed = false, want true (rung 2's query executes)")
		}
		// The judge saw the primary plus both escalation outputs.
		if arm.judge.calls != 3 {
			t.Errorf("judge invocations = %d, want 3", arm.judge.calls)
		}
		if len(rec.prompts) != 3 {
			t.Fatalf("rungs called %d times, want 3", len(rec.prompts))
		}
		if out := sqScore(ctx, dbPath, q, ref, result.GetOutput()); out.Category != sqCatCorrect {
			t.Errorf("final category = %q, want %q", out.Category, sqCatCorrect)
		}

		// The escalation prompt carries the task, the sqlite error and the failed
		// query — and nothing about the expected answer.
		esc := rec.prompts[1]
		for _, want := range []string{basePrompt, "no_such_table", "Database error:"} {
			if !strings.Contains(esc, want) {
				t.Errorf("escalation prompt is missing %q:\n%s", want, esc)
			}
		}
		if strings.Contains(esc, q.RefSQL) {
			t.Errorf("escalation prompt leaks the reference SQL:\n%s", esc)
		}
		if refRendered := sqRenderResult(ref); strings.Contains(esc, refRendered) {
			t.Errorf("escalation prompt leaks the reference result %q:\n%s", refRendered, esc)
		}
	})

	t.Run("silent_wrong is invisible: the judge passes it and nothing escalates", func(t *testing.T) {
		arm := &sqArm{name: "stub", dbPath: dbPath, question: q, ref: ref}
		exec := NewLevelingExecutor(nil, &LevelingPolicy{
			Enabled: true, ShortCircuitMid: true, MaxEscalations: 2, Judge: arm.executionJudge(),
		}, nil, nil)
		rec := &stub{}

		result, report, err := exec.Execute(ctx, policy,
			newStubLadder(rec, wrongSQL, rightSQL, rightSQL), basePrompt, "wf-sq-silent")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if report == nil {
			t.Fatal("leveling report is nil")
		}
		if report.Escalations != 0 {
			t.Errorf("escalations = %d, want 0 — an executable query must not escalate", report.Escalations)
		}
		if !report.Passed {
			t.Errorf("report.Passed = false; the execution judge passes any query that runs")
		}
		if len(rec.prompts) != 1 {
			t.Errorf("rungs called %d times, want 1 — rungs 1 and 2 must never run", len(rec.prompts))
		}
		if arm.judge.calls != 1 || arm.judge.passesOnWrong != 1 {
			t.Errorf("judge calls = %d (passes on wrong = %d), want 1 and 1",
				arm.judge.calls, arm.judge.passesOnWrong)
		}
		// The leveling PASS is on a wrong answer. That is the finding, not a bug.
		if out := sqScore(ctx, dbPath, q, ref, result.GetOutput()); out.Category != sqCatSilentWrong {
			t.Errorf("final category = %q, want %q", out.Category, sqCatSilentWrong)
		}
	})

	t.Run("leveling off runs the primary once and reports nothing", func(t *testing.T) {
		rec := &stub{}
		result, report, err := NewLevelingExecutor(nil, nil, nil, nil).Execute(
			ctx, policy, newStubLadder(rec, wrongSQL, rightSQL), basePrompt, "wf-sq-off")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if report != nil {
			t.Errorf("disabled leveling must report nothing, got %+v", report)
		}
		if len(rec.prompts) != 1 {
			t.Errorf("rungs called %d times, want 1", len(rec.prompts))
		}
		if got := result.GetOutput(); got != wrongSQL {
			t.Errorf("output = %q, want the primary rung's %q", got, wrongSQL)
		}
	})
}

// TestSQPromptRendering pins the prompt: it carries the schema verbatim, three
// sample rows per table, the question, and no role framing.
func TestSQPromptRendering(t *testing.T) {
	ds := genSQDataset()
	q := genSQQuestion(0, 0)
	prompt := sqRenderPrompt(ds, q.Text)

	for _, want := range []string{sqDDL, q.Text, "```sql"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	// Project rule: no role prompting anywhere in prompt text.
	for _, banned := range []string{"You are", "you are a", "As a ", "Act as"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("prompt contains role framing %q", banned)
		}
	}

	samples := sqSampleRows(ds)
	lines := strings.Split(samples, "\n")
	if len(lines) != 4*sqSampleRowsPerTable {
		t.Fatalf("sample block has %d lines, want %d", len(lines), 4*sqSampleRowsPerTable)
	}
	wantTables := []string{"customers", "customers", "customers", "products", "products", "products",
		"orders", "orders", "orders", "order_items", "order_items", "order_items"}
	for i, line := range lines {
		if !strings.HasPrefix(line, "INSERT INTO "+wantTables[i]+" VALUES (") {
			t.Errorf("sample line %d = %q, want an INSERT INTO %s", i, line, wantTables[i])
		}
		if !strings.HasSuffix(line, ");") {
			t.Errorf("sample line %d does not end with %q: %q", i, ");", line)
		}
	}
	// The first sample row must be the first generated row, rendered with bare
	// integers and single-quoted text.
	if lines[0] != ds.Customers[0].insert() {
		t.Errorf("first sample line = %q, want %q", lines[0], ds.Customers[0].insert())
	}
	t.Logf("prompt for q0 (%d bytes):\n%s", len(prompt), prompt)
}
