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

package index

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// ---------------------------------------------------------------------------
// A corpus big enough to be worth routing: eight domains, thirty-two skills,
// a tree two levels deep, and two deliberately fat leaves so the fat-leaf
// path is exercised rather than assumed.
// ---------------------------------------------------------------------------

type corpusSkill struct {
	domain string
	name   string
	title  string
	desc   string
}

func corpus() []corpusSkill {
	return []corpusSkill{
		// sql — fat leaf (6)
		{"sql", "sql-optimization", "SQL optimization", "Rewrite slow queries, read EXPLAIN output, choose join strategies and indexes."},
		{"sql", "sql-syntax-help", "SQL syntax help", "Explain dialect-specific syntax and correct statements a database rejected."},
		{"sql", "sql-window-functions", "Window functions", "Write ranking, running total and lag/lead expressions over partitions."},
		{"sql", "sql-migration", "Schema migration", "Plan and sequence DDL changes, backfills and column type changes."},
		{"sql", "sql-cost-analysis", "Query cost analysis", "Estimate and compare the cost of candidate query plans before running them."},
		{"sql", "sql-stored-procs", "Stored procedures", "Author and debug stored procedures, cursors and transaction blocks."},

		// data-prep — fat leaf (5)
		{"data-prep", "feature-engineering", "Feature engineering", "Derive model features: binning, one-hot encoding, scaling and imputation."},
		{"data-prep", "data-profiling", "Data profiling", "Summarise a table: null rates, cardinality, distributions and outliers."},
		{"data-prep", "csv-import", "CSV import", "Load delimited files, infer column types and handle malformed rows."},
		{"data-prep", "dedupe", "Deduplication", "Find and merge duplicate records using deterministic and fuzzy keys."},
		{"data-prep", "pii-redaction", "PII redaction", "Detect and mask personal data before it leaves a controlled environment."},

		// ml (4)
		{"ml", "model-training", "Model training", "Train classifiers and regressors, choose hyperparameters, evaluate fit."},
		{"ml", "model-eval", "Model evaluation", "Compute precision, recall, ROC and calibration for a trained model."},
		{"ml", "clustering", "Clustering", "Segment records with k-means or hierarchical clustering and read the result."},
		{"ml", "forecasting", "Time series forecasting", "Fit seasonal models and produce forward projections with intervals."},

		// infra (4)
		{"infra", "kubernetes-debug", "Kubernetes debugging", "Diagnose crashlooping pods, pending scheduling and failing probes."},
		{"infra", "terraform-plan", "Terraform review", "Read a plan diff and flag destructive or drift-inducing changes."},
		{"infra", "docker-build", "Container builds", "Write and slim Dockerfiles, fix layer caching and build failures."},
		{"infra", "incident-response", "Incident response", "Trace an outage from symptom to cause and write the timeline."},

		// code (4)
		{"code", "code-review", "Code review", "Review a diff for correctness, edge cases and missing tests."},
		{"code", "refactoring", "Refactoring", "Restructure code without behaviour change and keep the tests green."},
		{"code", "test-authoring", "Test authoring", "Write table-driven tests that pin behaviour rather than restate constants."},
		{"code", "dependency-audit", "Dependency audit", "Check dependencies for known vulnerabilities and license problems."},

		// docs (3)
		{"docs", "api-docs", "API documentation", "Document endpoints, parameters and error codes from the source of truth."},
		{"docs", "runbook", "Runbook authoring", "Write operational runbooks an on-call engineer can follow at 3am."},
		{"docs", "release-notes", "Release notes", "Summarise a release for users without marketing language."},

		// security (3)
		{"security", "threat-model", "Threat modelling", "Enumerate trust boundaries, assets and plausible attacker paths."},
		{"security", "secret-scanning", "Secret scanning", "Find committed credentials and plan their rotation."},
		{"security", "access-review", "Access review", "Audit who can reach which resource and flag excess privilege."},

		// finance (3)
		{"finance", "cost-attribution", "Cloud cost attribution", "Attribute spend to teams and find the drivers of a cost increase."},
		{"finance", "budget-forecast", "Budget forecasting", "Project spend from usage trends and committed discounts."},
		{"finance", "invoice-recon", "Invoice reconciliation", "Match provider invoices against metered usage and explain the gap."},
	}
}

var domainTitles = map[string]string{
	"sql":       "SQL and databases",
	"data-prep": "Data preparation",
	"ml":        "Machine learning",
	"infra":     "Infrastructure",
	"code":      "Software engineering",
	"docs":      "Documentation",
	"security":  "Security",
	"finance":   "Cloud finance",
}

var domainSummaries = map[string]string{
	"sql":       "Writing, correcting and tuning database queries and schemas.",
	"data-prep": "Shaping raw data into something usable: cleaning, profiling, loading.",
	"ml":        "Training, evaluating and applying statistical models.",
	"infra":     "Running systems: containers, clusters, provisioning and outages.",
	"code":      "Reading and changing source code safely.",
	"docs":      "Writing for other engineers and for users.",
	"security":  "Finding and closing exposure.",
	"finance":   "Understanding and predicting what infrastructure costs.",
}

// corpusTree builds the two-level index and a resolver over it.
func corpusTree() (*Tree, *fakeResolver, map[string]string) {
	byDomain := map[string][]string{}
	skillMap := map[string]*skills.Skill{}
	skillDomain := map[string]string{}
	for _, c := range corpus() {
		byDomain[c.domain] = append(byDomain[c.domain], c.name)
		skillMap[c.name] = &skills.Skill{Name: c.name, Title: c.title, Description: c.desc, Domain: c.domain}
		skillDomain[c.name] = c.domain
	}
	root := &skills.SkillIndexNode{ID: NodeID(Root), Title: "root"}
	nodes := []*skills.SkillIndexNode{root}
	for domain, names := range byDomain {
		id := NodeID("ent/" + domain)
		nodes = append(nodes, &skills.SkillIndexNode{
			ID: id, Title: domainTitles[domain], Summary: domainSummaries[domain],
			Depth: 1, SkillRefs: names,
		})
		root.Children = append(root.Children, id)
	}
	idx := &skills.SkillIndex{ID: "corpus", RootID: root.ID, Nodes: nodes}
	return NewTree(idx), &fakeResolver{skills: skillMap}, skillDomain
}

// ---------------------------------------------------------------------------
// The decider under test reads the request the way a real one would: it is
// handed only the state, so if the request fails to carry the message or the
// option text, the oracle cannot answer and the test fails.
// ---------------------------------------------------------------------------

type optionView struct{ ID, Kind, Title, Text string }

// oracleDecider answers each option by consulting score(), which sees only
// what the request carried.
type oracleDecider struct {
	score func(message string, opt optionView) float64
	calls atomic.Int32
	seen  atomic.Int32 // options answered
}

func (o *oracleDecider) Name() string  { return "oracle" }
func (o *oracleDecider) Model() string { return "oracle-1" }

func (o *oracleDecider) Decide(_ context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	if err := decision.Validate(req); err != nil {
		return nil, err
	}
	o.calls.Add(1)
	state := req.GetState().GetStructValue().AsMap()
	message, _ := state["message"].(string)
	if strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("request carried no message")
	}
	items, _ := state[sites.SkillRouteFanOutKey].([]any)
	if len(items) != len(req.Questions) {
		return nil, fmt.Errorf("carried %d options for %d questions", len(items), len(req.Questions))
	}
	answers := make(map[string]*loomv1.DecisionAnswer, len(items))
	for _, raw := range items {
		m, _ := raw.(map[string]any)
		id, _ := m["id"].(string)
		title, _ := m["title"].(string)
		text, _ := m["text"].(string)
		kind, _ := m["kind"].(string)
		if id == "" || title == "" {
			return nil, fmt.Errorf("option missing id or title: %v", m)
		}
		p := o.score(message, optionView{ID: id, Kind: kind, Title: title, Text: text})
		answers[id] = &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{
			Noul: &loomv1.NoulAnswer{Probability: p},
		}}
		o.seen.Add(1)
	}
	return &loomv1.DecisionResponse{Model: o.Model(), Answers: answers, Usage: &loomv1.DecisionUsage{}}, nil
}

// ---------------------------------------------------------------------------
// The labelled set. Each message names the skill a correct router must
// surface and the domain it lives under. Three shapes on purpose:
// literal (shares vocabulary), paraphrase (shares meaning only), and
// distractor (mentions one domain's vocabulary but needs another's skill).
// ---------------------------------------------------------------------------

type routeCase struct {
	message string
	want    string // skill name
	shape   string // literal | paraphrase | distractor
}

func routeCases() []routeCase {
	return []routeCase{
		{"this query takes 40 seconds, can you read the EXPLAIN and speed it up", "sql-optimization", "literal"},
		{"the database rejected my statement with error 3706, what is the right syntax", "sql-syntax-help", "literal"},
		{"I need a running total per customer ordered by date", "sql-window-functions", "paraphrase"},
		{"we're changing a column from int to bigint on a live table, how do we sequence it", "sql-migration", "paraphrase"},
		{"which of these two plans will be cheaper before I run either", "sql-cost-analysis", "paraphrase"},

		{"one-hot encode the occupation column and scale the income", "feature-engineering", "literal"},
		{"what fraction of rows are null and how many distinct values per column", "data-profiling", "paraphrase"},
		{"load this pipe-delimited export, some rows have the wrong field count", "csv-import", "paraphrase"},
		{"the same customer appears three times with slightly different spellings", "dedupe", "paraphrase"},
		{"strip names and account numbers before this leaves our VPC", "pii-redaction", "paraphrase"},

		{"train a classifier on this and tell me the hyperparameters you chose", "model-training", "literal"},
		{"how good is the model, give me precision and recall", "model-eval", "literal"},
		{"segment these customers into groups without predefined labels", "clustering", "paraphrase"},
		{"project the next four quarters from this monthly series", "forecasting", "paraphrase"},

		{"the pod keeps restarting and the readiness probe fails", "kubernetes-debug", "literal"},
		{"read this plan diff and tell me if anything gets destroyed", "terraform-plan", "paraphrase"},
		{"the image is 2GB and the build re-downloads everything each time", "docker-build", "paraphrase"},
		{"the site was down for 20 minutes, write up what happened and why", "incident-response", "paraphrase"},

		{"look over this diff for edge cases and missing tests", "code-review", "literal"},
		{"split this 400-line function up without changing what it does", "refactoring", "paraphrase"},
		{"write table-driven tests that would actually fail if this regressed", "test-authoring", "literal"},
		{"do any of our dependencies have known CVEs or bad licenses", "dependency-audit", "paraphrase"},

		{"document these endpoints and their error codes", "api-docs", "literal"},
		{"write something on-call can follow at 3am when this alert fires", "runbook", "literal"},
		{"summarise this release for users, no marketing language", "release-notes", "literal"},

		{"enumerate the trust boundaries and how an attacker would get in", "threat-model", "literal"},
		{"someone committed an API key, find it and plan the rotation", "secret-scanning", "paraphrase"},
		{"who can reach the production database and does anyone have more access than they need", "access-review", "paraphrase"},

		{"our cloud bill jumped 30% last month, which team caused it", "cost-attribution", "paraphrase"},
		{"project next year's spend given current usage and our committed discount", "budget-forecast", "paraphrase"},
		{"the invoice says more than our metered usage does, explain the gap", "invoice-recon", "paraphrase"},

		// Second and third phrasings for the same skills: the corpus has to
		// be wider than one sentence per skill or a single unlucky wording
		// decides the score.
		{"the optimizer is picking a nested loop join and it's killing us", "sql-optimization", "paraphrase"},
		{"add an index or rewrite the predicate, whichever is faster here", "sql-optimization", "paraphrase"},
		{"is QUALIFY supported here or do I need a subquery", "sql-syntax-help", "paraphrase"},
		{"rank each row inside its group and keep the top three", "sql-window-functions", "paraphrase"},
		{"we need to backfill the new column without locking the table", "sql-migration", "paraphrase"},
		{"declare a cursor and loop over it inside a procedure", "sql-stored-procs", "literal"},
		{"write the transaction block so a failure rolls everything back", "sql-stored-procs", "paraphrase"},

		{"bucket ages into ranges and fill the blanks with the median", "feature-engineering", "paraphrase"},
		{"give me min, max and the top ten values for every column", "data-profiling", "paraphrase"},
		{"the header row repeats halfway through the file", "csv-import", "paraphrase"},
		{"merge records that are the same person spelled differently", "dedupe", "paraphrase"},
		{"mask the email addresses before we share this extract", "pii-redaction", "paraphrase"},

		{"tune the learning rate and depth, then tell me what you picked", "model-training", "paraphrase"},
		{"plot the ROC and tell me if the probabilities are trustworthy", "model-eval", "paraphrase"},
		{"group these into five buckets and describe what each one is", "clustering", "paraphrase"},
		{"we need next quarter's demand with an uncertainty range", "forecasting", "paraphrase"},

		{"nothing is scheduling, the nodes say insufficient memory", "kubernetes-debug", "paraphrase"},
		{"this apply wants to replace the database, is that expected", "terraform-plan", "paraphrase"},
		{"every build reinstalls the same packages from scratch", "docker-build", "paraphrase"},
		{"write the postmortem for last night with a timeline", "incident-response", "paraphrase"},

		{"check this change for races before it goes in", "code-review", "paraphrase"},
		{"pull the duplicated logic out into something shared", "refactoring", "paraphrase"},
		{"these tests pass but they'd pass if the code were deleted", "test-authoring", "paraphrase"},
		{"anything in go.mod with a published advisory", "dependency-audit", "paraphrase"},

		{"describe the request body and every status code we return", "api-docs", "paraphrase"},
		{"steps to fail over the primary, assume the reader is tired", "runbook", "paraphrase"},
		{"what changed since the last tag, in plain language", "release-notes", "paraphrase"},

		{"where could an attacker get in if they had a stolen token", "threat-model", "paraphrase"},
		{"scan the history for anything that looks like a password", "secret-scanning", "paraphrase"},
		{"list everyone with write access to this bucket and why", "access-review", "paraphrase"},

		{"which service accounts for the spike in last week's spend", "cost-attribution", "paraphrase"},
		{"if usage keeps growing at this rate, what do we pay in June", "budget-forecast", "paraphrase"},
		{"reconcile these line items against what we actually used", "invoice-recon", "paraphrase"},

		// Distractors: the vocabulary points one way, the need points another.
		{"the SQL is fine, I need tests around the function that builds it", "test-authoring", "distractor"},
		{"the model is trained; document how to call the scoring endpoint", "api-docs", "distractor"},
		{"a query is slow because the pod it runs in is being throttled", "kubernetes-debug", "distractor"},
		{"the training job costs more than the model is worth, where is the spend going", "cost-attribution", "distractor"},
		{"the runbook says rotate the key but does not say which key", "secret-scanning", "distractor"},
		{"our dependency scan output needs to go in the release notes", "release-notes", "distractor"},
		{"the migration script has no tests", "test-authoring", "distractor"},
		{"encode the categorical column before the clustering step", "feature-engineering", "distractor"},
		{"document the threat model for the new endpoint", "threat-model", "distractor"},
	}
}

// outOfScopeMessages have no right answer in this corpus. A router that
// surfaces a skill for them is loading prompt budget for nothing, so they
// measure the opposite failure from a miss.
func outOfScopeMessages() []string {
	return []string{
		"what time is the standup tomorrow",
		"can you book me a flight to Dublin on the 14th",
		"remind me what the office wifi password is",
		"translate this paragraph into German",
		"what is the capital of Portugal",
		"my laptop fan is loud, should I be worried",
		"write a haiku about the weather",
		"who won the match last night",
	}
}

// keywordOracle is a deliberately weak semantic model: it scores an option by
// word overlap with the message. It is not meant to be good — it is meant to
// be independent of the routing machinery, so the test measures the walk
// rather than a scripted answer key.
func keywordOracle(skillDomain map[string]string) func(string, optionView) float64 {
	stop := map[string]bool{"the": true, "and": true, "for": true, "this": true, "that": true, "with": true,
		"are": true, "was": true, "from": true, "into": true, "what": true, "how": true, "our": true,
		"can": true, "you": true, "them": true, "their": true, "they": true, "per": true, "not": true,
		"need": true, "want": true, "does": true, "did": true, "has": true, "have": true, "will": true}
	alnum := func(r rune) bool { return ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') }
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !alnum(r) }) {
			if len(w) > 2 && !stop[w] {
				out[w] = true
			}
		}
		return out
	}
	stem := func(w string) string {
		if len(w) > 5 {
			return w[:5]
		}
		return w
	}
	return func(message string, opt optionView) float64 {
		mw := map[string]bool{}
		for w := range words(message) {
			mw[stem(w)] = true
		}
		hits := 0
		for w := range words(opt.Title + " " + opt.Text) {
			if mw[stem(w)] {
				hits++
			}
		}
		switch {
		case hits >= 3:
			return 0.95
		case hits == 2:
			return 0.85
		case hits == 1:
			return 0.45 // uncertain: descends a subtree, does not select a skill
		default:
			return 0.03
		}
	}
}

// The machinery test: with a decider that can only see what the request
// carries, the walk must reach the right leaf, never surface a skill from a
// pruned domain, and respect the candidate cap — across the whole corpus.
func TestRouteCorpusWalksTheTreeCorrectly(t *testing.T) {
	t.Parallel()
	tree, res, skillDomain := corpusTree()

	// A perfect oracle: it knows the labelled answer and scores the option
	// that leads there. This isolates the walk from the model's judgement.
	cases := routeCases()
	wantByMessage := map[string]string{}
	for _, c := range cases {
		wantByMessage[c.message] = c.want
	}
	titleDomain := map[string]string{}
	for d, title := range domainTitles {
		titleDomain[title] = d
	}
	// The request carries a positional question id, a title and the text —
	// never the skill's name. A decider judges on what it is shown, so the
	// oracle matches on the title exactly as a real model would have to.
	titleSkill := map[string]string{}
	for _, c := range corpus() {
		titleSkill[c.title] = c.name
	}
	oracle := &oracleDecider{score: func(message string, opt optionView) float64 {
		want := wantByMessage[message]
		if opt.Kind == sites.OptionKindSubtree {
			if titleDomain[opt.Title] == skillDomain[want] {
				return 0.97
			}
			return 0.02
		}
		if titleSkill[opt.Title] == want {
			return 0.97
		}
		return 0.02
	}}

	r := NewRouter(res,
		WithRouterLLM(newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":"must not be called"}`})),
		WithRouterDecision(decision.NewRouter(oracle, decision.WithBands(liveBand())), &memShadowStore{}))
	r.SetTree(tree)

	llm := r.llm.(*scriptedLLM)
	for _, c := range cases {
		got, err := r.Route(context.Background(), "corpus", c.message, nil, "h")
		require.NoError(t, err, c.message)
		names := make([]string, 0, len(got))
		for _, s := range got {
			names = append(names, s.Name)
		}
		assert.Contains(t, names, c.want, "message: %s", c.message)
		assert.LessOrEqual(t, len(names), r.maxCandidates, "cap respected for: %s", c.message)
		for _, n := range names {
			assert.Equal(t, skillDomain[c.want], skillDomain[n],
				"a skill from a pruned domain surfaced for: %s", c.message)
		}
	}
	assert.Equal(t, 0, llm.calls, "the whole corpus routed without a single generative call")
	// One decider call at the root plus one per fat leaf entered. Nothing
	// pathological: bounded by cases, not by corpus size.
	assert.Less(t, int(oracle.calls.Load()), len(cases)*3,
		"the walk stayed bounded: %d calls for %d messages", oracle.calls.Load(), len(cases))
}

// The realistic test: a weak, independent semantic model that has never seen
// the answer key. This measures how the *site* behaves when the decider is
// imperfect — which is the situation in production — and pins the properties
// that must hold regardless of how good the model is.
func TestRouteCorpusWithAnImperfectDecider(t *testing.T) {
	t.Parallel()
	tree, res, skillDomain := corpusTree()
	oracle := &oracleDecider{score: keywordOracle(skillDomain)}

	// The LLM stands behind the decider, as in production. Here it returns
	// nothing useful, so any correct answer came from the decision layer.
	llm := newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":"no idea"}`})
	r := NewRouter(res,
		WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(oracle, decision.WithBands(liveBand())), &memShadowStore{}))
	r.SetTree(tree)

	cases := routeCases()
	var hits, wrongDomain, empty int
	byShape := map[string][2]int{}
	for _, c := range cases {
		got, err := r.Route(context.Background(), "corpus", c.message, nil, "h")
		require.NoError(t, err, c.message)
		names := make([]string, 0, len(got))
		for _, s := range got {
			names = append(names, s.Name)
		}
		hit := false
		for _, n := range names {
			if n == c.want {
				hit = true
			} else if skillDomain[n] != skillDomain[c.want] {
				wrongDomain++
			}
		}
		if len(names) == 0 {
			empty++
		}
		if hit {
			hits++
		}
		s := byShape[c.shape]
		byShape[c.shape] = [2]int{s[0] + boolToInt(hit), s[1] + 1}
		assert.LessOrEqual(t, len(names), r.maxCandidates, "cap holds even when the decider is weak: %s", c.message)
	}
	t.Logf("imperfect decider: %d/%d correct skill surfaced, %d wrong-domain loads, %d empty",
		hits, len(cases), wrongDomain, empty)
	for shape, v := range byShape {
		t.Logf("  %-11s %d/%d", shape, v[0], v[1])
	}

	// These are properties that must hold for ANY decider, not a score for
	// this one. A weak model is allowed to route badly; it is not allowed to
	// route somewhere harmful, to exceed the cap, or to silently take the
	// decision away from the LLM when it has nothing to say.
	// A weak model is entitled to route wrongly — the distractor cases exist
	// to make it do so. What must not happen is flooding: the site handing
	// the agent a pile of unrelated skills. This is a regression guard on
	// that, not a quality bar for the model.
	assert.LessOrEqual(t, wrongDomain, len(cases)/4,
		"wrong-domain loads should stay rare even with a weak decider; got %d over %d messages",
		wrongDomain, len(cases))
	assert.Positive(t, llm.calls,
		"where the decider produced nothing, the walk must have asked the LLM instead")
	assert.LessOrEqual(t, empty, len(cases),
		"returning nothing is a valid outcome: Route's contract is that the caller falls back to FTS5")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// When the decider is confidently wrong at the root, the walk must not
// silently return that wrong answer: the LLM still stands behind it for the
// nodes the decider declined, and nothing panics.
func TestRouteCorpusSurvivesAConfidentlyWrongDecider(t *testing.T) {
	t.Parallel()
	tree, res, _ := corpusTree()
	// Says no to everything, confidently. Every option is pruned.
	oracle := &oracleDecider{score: func(string, optionView) float64 { return 0.01 }}
	llm := newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":"nothing"}`})

	r := NewRouter(res,
		WithRouterLLM(llm),
		WithRouterDecision(decision.NewRouter(oracle, decision.WithBands(liveBand())), &memShadowStore{}))
	r.SetTree(tree)

	got, err := r.Route(context.Background(), "corpus", "this query takes 40 seconds", nil, "h")
	require.NoError(t, err, "a wrong decider degrades, never errors")
	assert.Equal(t, 1, int(oracle.calls.Load()), "one call at the root")
	assert.Equal(t, 1, llm.calls,
		"discarding every option at a node is the answer most worth a second opinion, "+
			"so the walk asks the LLM rather than pruning the whole tree on one model's say-so")
	assert.Empty(t, got, "here the LLM had nothing either, so the caller falls back to FTS5")
}

// Route caps its result, so the order it collects names in decides which
// skills survive. Collecting them in a map made identical input return
// different skills between runs; this pins the fix.
func TestRouteIsDeterministicWhenMoreSkillsQualifyThanTheCap(t *testing.T) {
	t.Parallel()
	tree, res, _ := corpusTree()
	// Say yes to everything: six skills qualify under the SQL leaf against a
	// cap of three, which is exactly the case map iteration used to scramble.
	oracle := &oracleDecider{score: func(_ string, opt optionView) float64 {
		if opt.Kind == sites.OptionKindSubtree && opt.Title != domainTitles["sql"] {
			return 0.02
		}
		return 0.95
	}}
	r := NewRouter(res,
		WithRouterLLM(newScriptedLLM([]string{`{"descend":[],"skills":[],"reason":"x"}`})),
		WithRouterDecision(decision.NewRouter(oracle, decision.WithBands(liveBand())), &memShadowStore{}))
	r.SetTree(tree)

	var first []string
	for i := 0; i < 20; i++ {
		got, err := r.Route(context.Background(), "det", "tune a slow query", nil, "h")
		require.NoError(t, err)
		require.Len(t, got, r.maxCandidates, "the cap holds")
		names := make([]string, 0, len(got))
		for _, s := range got {
			names = append(names, s.Name)
		}
		if first == nil {
			first = names
			continue
		}
		assert.Equal(t, first, names, "run %d returned a different set for identical input", i)
	}
}

// countingErrLLM stands in for the tree-walk LLM so that anything the router
// gets right is attributable to the decider and nothing else, while still
// reporting how often the walk had to reach for it.
type countingErrLLM struct{ calls atomic.Int32 }

func (e *countingErrLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	e.calls.Add(1)
	return nil, fmt.Errorf("no LLM in this evaluation")
}
func (e *countingErrLLM) Name() string  { return "none" }
func (e *countingErrLLM) Model() string { return "none" }

// TestRouteCorpusAgainstJev measures the real decider against the labelled
// corpus across a grid of band settings.
//
// Two measurements, because they answer different questions:
//
//   - End to end: did the right skill surface, did an unrelated one, and did
//     an out-of-scope message wrongly load something. This is what a user
//     feels, but 73 messages is a small sample for it.
//   - Per decision: every option the decider was asked about is a labelled
//     yes/no — a subtree is a yes when it contains the wanted skill, a skill
//     option when it is the wanted skill. That is roughly a thousand
//     labelled judgements per configuration, which is enough to separate
//     configurations that end-to-end numbers cannot.
//
// Skipped without credentials, so it never runs in CI. To run it:
//
//	AI_GATEWAY_API_KEY=... go test -tags fts5 -run TestRouteCorpusAgainstJev -v -timeout 60m ./pkg/skills/index/
func TestRouteCorpusAgainstJev(t *testing.T) {
	cfg, err := jev.FromDecisionConfig(&loomv1.DecisionConfig{
		Model: jev.VercelGatewayModel, AllowAlias: true, RequestsPerMinute: 120,
	})
	if err != nil {
		t.Skipf("no Jev credentials: %v", err)
	}
	client, err := jev.New(cfg)
	require.NoError(t, err)
	chunked := decision.Chunk(client)

	tree, res, skillDomain := corpusTree()
	cases := routeCases()
	oos := outOfScopeMessages()

	// Ground truth per option, derived from the label: a subtree is a yes
	// when the wanted skill lives under it, a skill option when it is the
	// wanted skill.
	nodeDomain := map[string]string{}
	for domain := range domainTitles {
		nodeDomain[NodeID("ent/"+domain)] = domain
	}
	optionTruth := func(want, subject string) (truth, known bool) {
		switch {
		case strings.HasPrefix(subject, "subtree:"):
			d, ok := nodeDomain[strings.TrimPrefix(subject, "subtree:")]
			return ok && d == skillDomain[want], ok
		case strings.HasPrefix(subject, "skill:"):
			return strings.TrimPrefix(subject, "skill:") == want, true
		}
		return false, false
	}

	type result struct {
		actMin, trueMin float64
		hits, wrong     int
		empty, oosLoad  int
		llmCalls        int
		tp, fp, fn, tn  int
		byShape         map[string][2]int
		elapsed         time.Duration
	}
	var grid []result

	for _, actMin := range []float64{0.1, 0.5} {
		for _, trueMin := range []float64{0.05, 0.1, 0.2, 0.3, 0.5} {
			band := []*loomv1.DecisionBand{{
				Site: sites.SiteSkillRoute, ActMin: actMin, TrueMin: trueMin,
				Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION,
				Mode:      loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE,
			}}
			llm := &countingErrLLM{}
			store := &memShadowStore{}
			r := NewRouter(res, WithRouterLLM(llm),
				WithRouterDecision(decision.NewRouter(chunked, decision.WithBands(band)), store))
			r.SetTree(tree)

			out := result{actMin: actMin, trueMin: trueMin, byShape: map[string][2]int{}}
			start := time.Now()
			wantBySession := map[string]string{}
			for i, c := range cases {
				session := fmt.Sprintf("case-%d", i)
				wantBySession[session] = c.want
				got, err := r.Route(context.Background(), session, c.message, nil, "h")
				require.NoError(t, err, c.message)
				hit := false
				for _, sk := range got {
					switch {
					case sk.Name == c.want:
						hit = true
					case skillDomain[sk.Name] != skillDomain[c.want]:
						out.wrong++
					}
				}
				if len(got) == 0 {
					out.empty++
				}
				if hit {
					out.hits++
				}
				v := out.byShape[c.shape]
				out.byShape[c.shape] = [2]int{v[0] + boolToInt(hit), v[1] + 1}
			}
			for i, m := range oos {
				got, err := r.Route(context.Background(), fmt.Sprintf("oos-%d", i), m, nil, "h")
				require.NoError(t, err, m)
				out.oosLoad += len(got)
			}
			out.elapsed = time.Since(start)
			out.llmCalls = int(llm.calls.Load())

			r.WaitDecisionShadows()
			rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
			require.NoError(t, err)
			for _, row := range rows {
				want, ok := wantBySession[row.SessionId]
				if !ok || row.Path == loomv1.DecisionPath_DECISION_PATH_ERROR {
					continue
				}
				truth, known := optionTruth(want, row.Subject)
				if !known {
					continue
				}
				said := row.CandidateAnswer == "true"
				switch {
				case said && truth:
					out.tp++
				case said && !truth:
					out.fp++
				case !said && truth:
					out.fn++
				default:
					out.tn++
				}
			}
			grid = append(grid, out)
			t.Logf("act %.2f true %.2f | end-to-end %2d/%d correct, %2d wrong, %2d empty, %2d oos-loads | %s/msg",
				actMin, trueMin, out.hits, len(cases), out.wrong, out.empty, out.oosLoad,
				(out.elapsed / time.Duration(len(cases)+len(oos))).Round(time.Millisecond))
		}
	}

	t.Logf("")
	t.Logf("Per-decision judgement quality (band-independent: the raw yes/no at 0.5)")
	g := grid[0]
	prec, rec := ratio(g.tp, g.tp+g.fp), ratio(g.tp, g.tp+g.fn)
	t.Logf("  %d labelled option decisions: precision %.1f%%, recall %.1f%% (tp %d fp %d fn %d tn %d)",
		g.tp+g.fp+g.fn+g.tn, 100*prec, 100*rec, g.tp, g.fp, g.fn, g.tn)

	best := grid[0]
	for _, x := range grid {
		if x.hits > best.hits {
			best = x
		}
	}
	t.Logf("")
	t.Logf("best end-to-end: act_min %.2f true_min %.2f -> %d/%d (%.0f%%), %d wrong-domain, %d out-of-scope loads",
		best.actMin, best.trueMin, best.hits, len(cases),
		100*float64(best.hits)/float64(len(cases)), best.wrong, best.oosLoad)
	for _, shape := range []string{"literal", "paraphrase", "distractor"} {
		v := best.byShape[shape]
		t.Logf("  %-11s %d/%d", shape, v[0], v[1])
	}

	assert.Greater(t, best.hits, len(cases)/4,
		"the decider should route better than chance on a labelled corpus")
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
