// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file ports the calibrated reasoning-bound task family into Go so the
// live leveling experiment (leveling_reasoning_live_test.go) can generate the
// exact same problems the Python calibration harness measured, without needing
// Python at test time.
//
// The task family is arith_chain level 11 from the calibration run:
//
//	(a + b) * c - d   with a,b ∈ [10,49], c ∈ [11,19], d ∈ [10,99]
//
// Calibrated wrong-rates on this family: llama2:latest 60% wrong (n=30, 95% CI
// 42.5–77.5), deepseek-r1:latest 0% wrong (n=20), both at a 100% JSON contract
// rate under a tolerant parser. That gap is what makes the family usable as a
// leveling probe: the weak model produces confidently wrong but perfectly
// well-formed answers.
//
// Two properties are load-bearing and are tested here (no network needed):
//
//  1. Ground truth is computed from the structured parts (a, b, c, d), never by
//     parsing or evaluating the rendered expression string.
//  2. Problem i is a pure function of i, reproducing the calibration problems
//     bit-for-bit. That requires reproducing CPython's random.Random, because
//     the calibration generator drew its operands from it. See pyRandom.

// ─────────────────── CPython random.Random reproduction ───────────────────

// pyRandom reproduces CPython's random.Random for the three operations the
// calibration generator used: integer seeding, getrandbits, and randint.
//
// This is MT19937 with CPython's init_by_array seeding and CPython's
// _randbelow_with_getrandbits rejection sampling. Go's math/rand is a different
// generator and cannot reproduce the calibration draws, so porting the RNG is
// the only way to keep the Go problems identical to the measured ones.
// TestArithChain11MatchesCalibration pins that claim to 40 problems from the
// calibration generator, the first 30 of which are the ones actually sent during
// the calibration run.
type pyRandom struct {
	mt  [624]uint32
	idx int
}

// newPyRandom seeds like CPython's random.Random(n) for a non-negative n that
// fits in 32 bits: init_genrand(19650218) followed by init_by_array with the
// seed as a single 32-bit key word.
func newPyRandom(seed uint32) *pyRandom {
	r := &pyRandom{}
	r.initGenrand(19650218)
	r.initByArray([]uint32{seed})
	return r
}

func (r *pyRandom) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < len(r.mt); i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i) //nolint:gosec // MT19937 constant, wraparound intended
	}
	r.idx = len(r.mt)
}

func (r *pyRandom) initByArray(key []uint32) {
	n := len(r.mt)
	i, j := 1, 0
	k := n
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j) //nolint:gosec // MT19937 constant
		i++
		j++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = n - 1; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i) //nolint:gosec // MT19937 constant
		i++
		if i >= n {
			r.mt[0] = r.mt[n-1]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
	r.idx = n
}

func (r *pyRandom) generate() {
	const m = 397
	n := len(r.mt)
	mag01 := [2]uint32{0, 0x9908b0df}
	for i := 0; i < n; i++ {
		y := (r.mt[i] & 0x80000000) | (r.mt[(i+1)%n] & 0x7fffffff)
		r.mt[i] = r.mt[(i+m)%n] ^ (y >> 1) ^ mag01[y&1]
	}
	r.idx = 0
}

// nextUint32 is MT19937's genrand_uint32 with the standard tempering.
func (r *pyRandom) nextUint32() uint32 {
	if r.idx >= len(r.mt) {
		r.generate()
	}
	y := r.mt[r.idx]
	r.idx++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// getRandBits mirrors Random.getrandbits(k) for 1 <= k <= 32.
func (r *pyRandom) getRandBits(k int) uint32 {
	if k <= 0 {
		return 0
	}
	if k >= 32 {
		return r.nextUint32()
	}
	return r.nextUint32() >> (32 - k)
}

// randBelow mirrors Random._randbelow_with_getrandbits: draw bit_length(n) bits
// and reject anything >= n. Reproducing the rejections matters — a different
// rejection rule desynchronizes the whole draw sequence.
func (r *pyRandom) randBelow(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	k := bits.Len32(n)
	v := r.getRandBits(k)
	for v >= n {
		v = r.getRandBits(k)
	}
	return v
}

// randInt mirrors Random.randint(a, b): inclusive on both ends.
func (r *pyRandom) randInt(a, b int) int {
	return a + int(r.randBelow(uint32(b-a+1))) //nolint:gosec // b > a by construction
}

// ─────────────────── the calibrated task family ───────────────────

// arithLevel is the calibrated level of the arith_chain family used here.
const (
	arithFamily = "arith_chain"
	arithLevel  = 11
)

// arithAnswerSchema is the leveling contract for this task. Note what it does
// NOT do: a numerically wrong answer satisfies it perfectly. That is the whole
// point of the experiment — it is the control that isolates what a structural
// signal can and cannot detect.
const arithAnswerSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer"],
  "properties": {
    "answer": {"type": "integer"}
  }
}`

// arithPromptSuffix is copied verbatim from the calibration harness, including
// whitespace, so the Go prompts are byte-identical to the measured ones. No role
// framing, per project rules.
const arithPromptSuffix = "\n\nWork out the exact value. Then reply with a single JSON object and nothing else, in this form:\n{\"answer\": <integer>}"

// arithProblem is one generated problem plus its ground truth.
type arithProblem struct {
	Index      int    `json:"index"`
	Seed       uint32 `json:"seed"`
	A          int    `json:"a"`
	B          int    `json:"b"`
	C          int    `json:"c"`
	D          int    `json:"d"`
	Expression string `json:"expression"`
	Answer     int    `json:"answer"`
	Prompt     string `json:"-"`
}

// arithStableSeed is the calibration's seed derivation: crc32(IEEE) over
// "family|level|index".
func arithStableSeed(family string, level, index int) uint32 {
	return crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%d|%d", family, level, index)))
}

// genArithChain11 generates problem `index` of arith_chain level 11.
//
// The draw order (a, b, c, d) and the rejection of non-positive totals mirror
// the calibration generator exactly; for this level the total is positive by
// construction (minimum (10+10)*11-99 = 121), so the rejection loop never
// actually rejects, but it is kept so the port stays faithful.
func genArithChain11(index int) arithProblem {
	seed := arithStableSeed(arithFamily, arithLevel, index)
	r := newPyRandom(seed)

	for attempt := 0; attempt < 500; attempt++ {
		a, b := r.randInt(10, 49), r.randInt(10, 49)
		c := r.randInt(11, 19)
		d := r.randInt(10, 99)

		// Ground truth from the structured parts. The rendered string is only
		// ever shown to the model; it is never parsed or evaluated here.
		total := (a+b)*c - d
		if total <= 0 {
			continue
		}

		expr := fmt.Sprintf("(%d + %d) * %d - %d", a, b, c, d)
		body := "Evaluate this expression using standard order of operations " +
			"(parentheses first, then multiplication, then addition and " +
			"subtraction left to right):\n\n" + expr
		return arithProblem{
			Index:      index,
			Seed:       seed,
			A:          a,
			B:          b,
			C:          c,
			D:          d,
			Expression: expr,
			Answer:     total,
			Prompt:     body + arithPromptSuffix,
		}
	}
	panic("genArithChain11: rejection sampling failed")
}

// ─────────────────── answer parsing (defensive) ───────────────────

var (
	thinkTagRE  = regexp.MustCompile(`(?s)<think>.*?</think>`)
	answerKeyRE = regexp.MustCompile(`"answer"\s*:\s*(-?\d+)`)
)

// parseArithAnswer extracts the model's final integer answer from a reply.
//
// Deliberately more tolerant than the JSON schema: the models under test wrap
// their JSON in prose (llama2) or emit reasoning before it, and this function
// measures "did the model commit to a number" rather than "was the reply
// well-formed". The schema verdict is measured separately, on the raw output.
//
// Order: strip <think> blocks (older Ollama inlines reasoning that way), try the
// whole reply as JSON, then the last balanced {...} that carries an integer
// "answer", then a regex over the tail as a last resort.
func parseArithAnswer(output string) (int, bool) {
	s := strings.TrimSpace(thinkTagRE.ReplaceAllString(output, ""))
	if s == "" {
		return 0, false
	}

	type answerDoc struct {
		Answer *json.Number `json:"answer"`
	}
	decode := func(candidate string) (int, bool) {
		var doc answerDoc
		if err := json.Unmarshal([]byte(candidate), &doc); err != nil || doc.Answer == nil {
			return 0, false
		}
		// Tolerate 537.0 as well as 537; reject 537.5.
		f, err := doc.Answer.Float64()
		if err != nil || f != float64(int64(f)) {
			return 0, false
		}
		return int(int64(f)), true
	}

	if v, ok := decode(s); ok {
		return v, true
	}
	// Last balanced object wins: models often restate the answer at the end.
	for _, candidate := range balancedJSONObjects(s) {
		if v, ok := decode(candidate); ok {
			return v, true
		}
	}
	if m := answerKeyRE.FindAllStringSubmatch(s, -1); len(m) > 0 {
		if v, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return v, true
		}
	}
	return 0, false
}

// balancedJSONObjects returns every balanced {...} span in s, last one first.
func balancedJSONObjects(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		depth, inStr, esc := 0, false, false
		for j := i; j < len(s); j++ {
			ch := s[j]
			switch {
			case esc:
				esc = false
			case ch == '\\' && inStr:
				esc = true
			case ch == '"':
				inStr = !inStr
			case inStr:
			case ch == '{':
				depth++
			case ch == '}':
				depth--
				if depth == 0 {
					out = append(out, s[i:j+1])
					i = j
					j = len(s)
				}
			}
		}
	}
	// Reverse so the last object in the text is examined first.
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// ─────────────────── fixtures + tests (no network) ───────────────────

// calibratedArithChain11 is the first 40 problems of arith_chain level 11 as
// emitted by the Python calibration generator. If the Go port drifts from
// CPython's RNG by even one draw, this table stops matching.
//
// Indices 0–29 are the problems that were actually generated AND sent during the
// calibration run, lifted from its per-trial JSONL — those are the rows the
// measured wrong-rates attach to, and they are the indices every arm runs.
// Indices 30–39 were never sent to a model; they come from the same generator
// and are pinned here so the RNG port stays verified past the measured set (the
// experiment can be widened to N=40 without re-deriving ground truth).
var calibratedArithChain11 = []struct {
	index int
	seed  uint32
	expr  string
	ans   int
}{
	{0, 1361258780, "(13 + 18) * 18 - 21", 537},
	{1, 639900042, "(36 + 49) * 17 - 56", 1389},
	{2, 3207415856, "(40 + 24) * 13 - 78", 754},
	{3, 3358226598, "(22 + 47) * 17 - 24", 1149},
	{4, 1448013061, "(30 + 38) * 18 - 89", 1135},
	{5, 558488979, "(42 + 30) * 17 - 86", 1138},
	{6, 3091237929, "(46 + 31) * 16 - 61", 1171},
	{7, 3477585087, "(16 + 47) * 13 - 50", 769},
	{8, 1610129710, "(15 + 24) * 15 - 77", 508},
	{9, 687837624, "(23 + 39) * 13 - 62", 744},
	{10, 4187001094, "(30 + 30) * 12 - 59", 661},
	{11, 2392301968, "(30 + 30) * 18 - 76", 1004},
	{12, 396292138, "(46 + 12) * 11 - 92", 546},
	{13, 1620689084, "(13 + 10) * 18 - 81", 333},
	{14, 4278015263, "(33 + 10) * 12 - 58", 458},
	{15, 2314888585, "(44 + 21) * 16 - 10", 1030},
	{16, 284374067, "(47 + 26) * 11 - 30", 773},
	{17, 1744045221, "(27 + 44) * 11 - 49", 732},
	{18, 4148894004, "(29 + 29) * 18 - 49", 995},
	{19, 2152475042, "(31 + 23) * 16 - 99", 765},
	{20, 3535651525, "(48 + 10) * 13 - 27", 727},
	{21, 2780492371, "(26 + 30) * 13 - 71", 657},
	{22, 1018405865, "(24 + 27) * 13 - 44", 619},
	{23, 1270125439, "(32 + 28) * 12 - 27", 693},
	{24, 3587180252, "(21 + 14) * 13 - 68", 387},
	{25, 2732013130, "(16 + 12) * 11 - 78", 230},
	{26, 1004431344, "(46 + 45) * 12 - 97", 995},
	{27, 1289312102, "(11 + 22) * 16 - 74", 454},
	{28, 3697691383, "(49 + 33) * 13 - 93", 973},
	{29, 2875292257, "(41 + 15) * 12 - 23", 649},
	{30, 3416716164, "(13 + 47) * 15 - 46", 854},
	{31, 3164717842, "(41 + 44) * 16 - 53", 1307},
	{32, 631804584, "(39 + 34) * 15 - 76", 1019},
	{33, 1387242046, "(33 + 43) * 13 - 93", 895},
	{34, 3435870109, "(42 + 15) * 11 - 64", 563},
	{35, 3150710539, "(43 + 27) * 15 - 94", 956},
	{36, 583358129, "(44 + 38) * 14 - 74", 1074},
	{37, 1438803495, "(48 + 24) * 11 - 52", 740},
	{38, 3313335222, "(22 + 36) * 19 - 74", 1028},
	{39, 2994359072, "(49 + 29) * 16 - 78", 1170},
}

// TestArithChain11MatchesCalibration is the port's correctness gate: the Go
// generator must reproduce the calibration problems exactly, or the calibrated
// 60%/0% wrong-rates do not transfer to the Go experiment.
func TestArithChain11MatchesCalibration(t *testing.T) {
	for _, want := range calibratedArithChain11 {
		got := genArithChain11(want.index)
		if got.Seed != want.seed {
			t.Errorf("index %d: seed = %d, want %d (crc32 derivation drifted)", want.index, got.Seed, want.seed)
		}
		if got.Expression != want.expr {
			t.Errorf("index %d: expression = %q, want %q (RNG port drifted)", want.index, got.Expression, want.expr)
		}
		if got.Answer != want.ans {
			t.Errorf("index %d: answer = %d, want %d", want.index, got.Answer, want.ans)
		}
		if !strings.Contains(got.Prompt, want.expr) || !strings.HasSuffix(got.Prompt, arithPromptSuffix) {
			t.Errorf("index %d: prompt is not the calibration prompt: %q", want.index, got.Prompt)
		}
	}
}

// TestArithChain11Sanity checks the distributional properties the experiment
// relies on: 40 consecutive indices give 40 distinct problems, every operand
// stays in its calibrated range, and every answer lands in the observed band.
func TestArithChain11Sanity(t *testing.T) {
	const n = 40
	seen := make(map[string]int, n)
	minAns, maxAns := 1<<31, 0
	for i := 0; i < n; i++ {
		p := genArithChain11(i)
		if prev, dup := seen[p.Expression]; dup {
			t.Errorf("index %d duplicates index %d: %s", i, prev, p.Expression)
		}
		seen[p.Expression] = i

		if p.A < 10 || p.A > 49 || p.B < 10 || p.B > 49 {
			t.Errorf("index %d: a=%d b=%d outside [10,49]", i, p.A, p.B)
		}
		if p.C < 11 || p.C > 19 {
			t.Errorf("index %d: c=%d outside [11,19]", i, p.C)
		}
		if p.D < 10 || p.D > 99 {
			t.Errorf("index %d: d=%d outside [10,99]", i, p.D)
		}
		if want := (p.A+p.B)*p.C - p.D; p.Answer != want {
			t.Errorf("index %d: answer %d != structured truth %d", i, p.Answer, want)
		}
		if p.Answer < minAns {
			minAns = p.Answer
		}
		if p.Answer > maxAns {
			maxAns = p.Answer
		}
	}
	if len(seen) != n {
		t.Errorf("got %d unique problems over %d indices, want %d", len(seen), n, n)
	}
	// The calibration's observed band over these indices was [230, 1389].
	if minAns < 121 || maxAns > 1862 {
		t.Errorf("answers spanned [%d,%d], outside the constructible band [121,1862]", minAns, maxAns)
	}
	t.Logf("40 unique problems, answers in [%d,%d]", minAns, maxAns)
}

// TestArithChain11Determinism pins the pure-function property: the same index
// generates the same problem every time, in any order.
func TestArithChain11Determinism(t *testing.T) {
	for _, i := range []int{0, 7, 29, 3, 29, 0} {
		a, b := genArithChain11(i), genArithChain11(i)
		if a != b {
			t.Fatalf("index %d not deterministic: %+v vs %+v", i, a, b)
		}
	}
}

// TestParseArithAnswer covers the reply shapes both models actually produced in
// calibration, including a wrong-but-well-formed answer and reasoning-model
// output with an inline <think> block.
func TestParseArithAnswer(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   int
		ok     bool
	}{
		{"bare json", `{"answer": 537}`, 537, true},
		{"llama2 prose then json",
			"Sure! Here's the evaluation:\n\n(13 + 18) = 31\n31 * 18 = 578\n578 - 21 = 557\n\n{\"answer\": 557}",
			557, true},
		{"fenced json", "```json\n{\"answer\": 754}\n```", 754, true},
		{"think block stripped", "<think>31*18=558, minus 21</think>\n{\"answer\": 537}", 537, true},
		{"restates answer twice", `{"answer": 100} and after checking {"answer": 537}`, 537, true},
		{"float form", `{"answer": 537.0}`, 537, true},
		{"regex fallback", `the result is "answer": 649 as computed`, 649, true},
		{"no answer at all", "I cannot compute that.", 0, false},
		{"empty", "   ", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseArithAnswer(tc.output)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Errorf("parseArithAnswer(%q) = (%d,%t), want (%d,%t)", tc.output, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestArithAnswerSchemaAcceptsWrongAnswers is the experiment's control, asserted
// in code: the structural contract cannot tell a right answer from a wrong one.
// H1 rests entirely on this.
func TestArithAnswerSchemaAcceptsWrongAnswers(t *testing.T) {
	p := genArithChain11(0) // (13 + 18) * 18 - 21 = 537
	right := fmt.Sprintf(`{"answer": %d}`, p.Answer)
	wrong := `{"answer": 557}` // what llama2 actually answered in calibration

	if err := validateJSONSchema(arithAnswerSchema, right); err != nil {
		t.Fatalf("correct answer rejected by schema: %v", err)
	}
	if err := validateJSONSchema(arithAnswerSchema, wrong); err != nil {
		t.Fatalf("wrong answer rejected by schema: %v — the control assumption is broken", err)
	}
	// And the shape llama2 actually emits does NOT satisfy the schema directly,
	// which is why coercion (not correctness) drives the schema-only arm.
	prose := "Sure! Here's the evaluation:\n\n" + wrong
	if err := validateJSONSchema(arithAnswerSchema, prose); err == nil {
		t.Fatal("prose-wrapped JSON unexpectedly satisfied the schema")
	}
	if extracted := extractJSONFromText(prose); extracted != wrong {
		t.Fatalf("coercion extracted %q, want %q", extracted, wrong)
	}
}
