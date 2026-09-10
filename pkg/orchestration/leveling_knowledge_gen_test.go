// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"hash/crc32"
	"regexp"
	"strings"
	"testing"
)

// This file generates the synthetic knowledge corpus for the knowledge-gap
// experiment (leveling_knowledge_live_test.go) and pins the properties that
// experiment depends on. Nothing here touches the network, so these tests run in
// `-short` mode alongside everything else.
//
// Why a FICTIONAL corpus. Every earlier leveling measurement used a task whose
// answer is in principle derivable (arithmetic) or memorized (country facts). A
// knowledge-augmentation arm cannot be measured on either: a strong model may
// already know the fact, and then "retrieval helped" is unfalsifiable. These 200
// device records are invented, so the answer is unknowable to ANY weights at any
// scale. The only way a model can answer is if the fact is in its prompt — which
// is exactly the claim under test.
//
// Two properties are load-bearing and tested below:
//
//  1. Ground truth comes from the structured fields, never from parsing the
//     rendered sentence.
//  2. Record i is a pure function of i, so any run of the experiment (and any
//     re-analysis of its JSONL) sees the identical corpus.

const (
	// kgCorpusSize is how many device records the corpus holds. All of them are
	// ingested into the memory store for the retrieval arms, so 200 records means
	// the retriever has 199 distractors per question.
	kgCorpusSize = 200

	// kgPairOffset is the near-collision stride: record i+kgPairOffset shares
	// record i's two-letter prefix and carries a rotation of its four digits.
	// That makes half the corpus a deliberate lexical near-miss for the other
	// half, so a retriever cannot succeed by matching the letter prefix alone.
	kgPairOffset = kgCorpusSize / 2

	// kgQuestionCount is how many questions the experiment asks: one per distinct
	// device, indices 0..kgQuestionCount-1, cycling through the four attributes.
	kgQuestionCount = 30

	// kgAbstain is the sentinel a model is told to return when it does not have
	// the fact. It separates "I don't know" from "here is a wrong number", which
	// is the distinction the no-context arms exist to measure. No real attribute
	// can take this value (all four ranges are positive).
	kgAbstain = -1
)

// ─────────────────────────── deterministic derivation ───────────────────────────

// kgStableSeed is the corpus seed derivation: crc32(IEEE) over "knowledge|<index>".
// Same idiom as arithStableSeed in leveling_arith_gen_test.go.
func kgStableSeed(index int) uint32 {
	return crc32.ChecksumIEEE([]byte(fmt.Sprintf("knowledge|%d", index)))
}

// kgRand is splitmix64, seeded from a record's crc32 seed.
//
// It is hand-rolled rather than taken from math/rand for one reason: the corpus
// must be byte-reproducible from the index forever, including from a future Go
// release or a re-analysis script in another language. splitmix64 is nine lines
// and fully specified by its constants, so there is nothing to drift.
type kgRand struct{ state uint64 }

func newKGRand(seed uint32) *kgRand { return &kgRand{state: uint64(seed)} }

func (r *kgRand) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intn returns a value in [lo, hi], inclusive on both ends. The modulo bias is
// irrelevant here — the corpus only needs determinism and range coverage, not a
// uniform distribution.
func (r *kgRand) intn(lo, hi int) int {
	return lo + int(r.next()%uint64(hi-lo+1)) //nolint:gosec // ranges are small positive constants
}

// ─────────────────────────── the corpus ───────────────────────────

// kgRecord is one fictional device plus the four integer attributes a question
// can ask about.
type kgRecord struct {
	Index            int    `json:"index"`
	Seed             uint32 `json:"seed"`
	DeviceID         string `json:"device_id"`
	RatedWatts       int    `json:"rated_watts"`
	FirmwareBuild    int    `json:"firmware_build"`
	BayNumber        int    `json:"bay_number"`
	CommissionedYear int    `json:"commissioned_year"`
	// Sentence is the one-sentence rendering ingested into the memory store and
	// prepended as context. It is the ONLY form a model ever sees.
	Sentence string `json:"sentence"`
}

// genKGRecord generates record `index` of the corpus.
//
// For index < kgPairOffset the device ID is drawn from the record's own seed. For
// index >= kgPairOffset the ID is derived from record index-kgPairOffset: same
// two letters, digits rotated left by one. The ATTRIBUTES are always drawn from
// the record's own seed, so a near-collision pair looks alike and answers
// differently — which is what makes a retrieval miss cost accuracy instead of
// being silently masked.
func genKGRecord(index int) kgRecord {
	seed := kgStableSeed(index)
	r := newKGRand(seed)

	// Attribute draw order is fixed; changing it changes the corpus.
	watts := r.intn(200, 2000)
	firmware := r.intn(100, 999)
	bay := r.intn(1, 96)
	year := r.intn(2004, 2024)

	var letters, digits string
	if index < kgPairOffset {
		letters = string([]byte{
			byte('A' + r.intn(0, 25)),
			byte('A' + r.intn(0, 25)),
		})
		d := []byte{
			byte('0' + r.intn(0, 9)),
			byte('0' + r.intn(0, 9)),
			byte('0' + r.intn(0, 9)),
			byte('0' + r.intn(0, 9)),
		}
		// Guarantee at least two distinct digits, so the rotation used by this
		// record's pair is always a DIFFERENT string. Without this, "7777"
		// would rotate to itself and the pair would collide outright.
		if d[0] == d[1] && d[1] == d[2] && d[2] == d[3] {
			d[3] = '0' + (d[3]-'0'+1)%10
		}
		digits = string(d)
	} else {
		base := genKGRecord(index - kgPairOffset)
		letters, digits = base.DeviceID[:2], kgRotateLeft(base.DeviceID[3:])
	}

	deviceID := letters + "-" + digits
	return kgRecord{
		Index:            index,
		Seed:             seed,
		DeviceID:         deviceID,
		RatedWatts:       watts,
		FirmwareBuild:    firmware,
		BayNumber:        bay,
		CommissionedYear: year,
		Sentence: fmt.Sprintf(
			"Device %s runs firmware build %d, is rated at %d watts, sits in bay %d, and was commissioned in %d.",
			deviceID, firmware, watts, bay, year),
	}
}

// kgRotateLeft rotates a string left by one character: "4471" → "4714".
func kgRotateLeft(s string) string {
	if len(s) < 2 {
		return s
	}
	return s[1:] + s[:1]
}

// kgCorpus generates the whole corpus, in index order.
func kgCorpus() []kgRecord {
	out := make([]kgRecord, kgCorpusSize)
	for i := range out {
		out[i] = genKGRecord(i)
	}
	return out
}

// ─────────────────────────── the questions ───────────────────────────

// kgAttribute is one askable integer attribute: how to ask for it, and how to
// read the truth out of the structured record.
type kgAttribute struct {
	Key      string
	Template string // one %s, the device ID
	Value    func(kgRecord) int
}

// kgAttributes are cycled round-robin over the question indices, so the
// experiment is not measuring one attribute's phrasing.
var kgAttributes = []kgAttribute{
	{"rated_watts", "What is the rated wattage of device %s?", func(r kgRecord) int { return r.RatedWatts }},
	{"firmware_build", "What is the firmware build of device %s?", func(r kgRecord) int { return r.FirmwareBuild }},
	{"bay_number", "What is the bay number of device %s?", func(r kgRecord) int { return r.BayNumber }},
	{"commissioned_year", "In what year was device %s commissioned?", func(r kgRecord) int { return r.CommissionedYear }},
}

// kgQuestion is one question plus its ground truth and the record that answers
// it (the "gold" record, used to score retrieval hits).
type kgQuestion struct {
	Index     int      `json:"index"`
	Gold      kgRecord `json:"gold"`
	Attribute string   `json:"attribute"`
	Text      string   `json:"question"`
	Truth     int      `json:"truth"`
}

// genKGQuestion builds question `index`: it asks one attribute of device
// `index`, cycling the attribute by index%4.
func genKGQuestion(index int) kgQuestion {
	rec := genKGRecord(index)
	attr := kgAttributes[index%len(kgAttributes)]
	return kgQuestion{
		Index:     index,
		Gold:      rec,
		Attribute: attr.Key,
		Text:      fmt.Sprintf(attr.Template, rec.DeviceID),
		Truth:     attr.Value(rec),
	}
}

// ─────────────────────────── FTS5 query rewrite ───────────────────────────

// kgTermJunkRE strips everything that is not a letter, a digit, or a hyphen.
var kgTermJunkRE = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// kgFTS5Query rewrites a natural-language question into a query FTS5 will accept,
// by quoting every term and joining with OR.
//
// The quoting is REQUIRED, and this was found by measurement, not by reading. A
// question passed through verbatim fails two ways:
//
//	"…device KX-4471?"  → fts5: syntax error near "?"   (bare "?" is not a term)
//	"KX-4471"  unquoted → no such column: 4441          (parsed as column:term)
//
// Quoting each term makes each one a phrase: "KX-4471" becomes the two-token
// phrase [kx, 4471], which matches the gold sentence and — because the digit
// groups differ — does NOT match the near-collision pair. Because the result
// contains a double quote, GraphMemoryStore.toFTS5OrQuery passes it through
// unchanged, so the harness controls the whole query rather than half of it.
//
// OR (not AND) is kept deliberately: it mirrors what Loom's own recall path does,
// and it means BM25 ranking — not a hard filter — decides the top-k. That is the
// realistic behavior the arm is supposed to measure.
func kgFTS5Query(question string) string {
	terms := make([]string, 0, 16)
	for _, field := range strings.Fields(question) {
		clean := strings.Trim(kgTermJunkRE.ReplaceAllString(field, ""), "-")
		if clean == "" {
			continue
		}
		terms = append(terms, `"`+clean+`"`)
	}
	return strings.Join(terms, " OR ")
}

// ─────────────────────────── tests (no network) ───────────────────────────

// TestKGCorpusDeterminism pins the pure-function property the whole experiment
// rests on: the same index always yields the same record.
func TestKGCorpusDeterminism(t *testing.T) {
	for _, i := range []int{0, 1, 29, 100, 129, 199, 0, 100} {
		a, b := genKGRecord(i), genKGRecord(i)
		if a != b {
			t.Fatalf("record %d is not deterministic: %+v vs %+v", i, a, b)
		}
	}
	// And the seed derivation itself is pinned, so a corpus regenerated from a
	// committed JSONL row can be checked against it.
	if got, want := kgStableSeed(0), crc32.ChecksumIEEE([]byte("knowledge|0")); got != want {
		t.Fatalf("kgStableSeed(0) = %d, want %d", got, want)
	}
}

// TestKGCorpusIDCollisionProperty is the retrieval-difficulty guarantee: record
// i and record i+100 share a letter prefix and a digit multiset, and differ.
// Without this the retrieval arms would be trivially easy — matching two letters
// would be enough.
func TestKGCorpusIDCollisionProperty(t *testing.T) {
	for i := 0; i < kgPairOffset; i++ {
		base, pair := genKGRecord(i), genKGRecord(i+kgPairOffset)

		if base.DeviceID[:2] != pair.DeviceID[:2] {
			t.Errorf("records %d/%d: prefixes %q/%q differ, want the same",
				i, i+kgPairOffset, base.DeviceID[:2], pair.DeviceID[:2])
		}
		if base.DeviceID == pair.DeviceID {
			t.Errorf("records %d/%d: IDs are identical (%s); the pair must be a near-miss, not a duplicate",
				i, i+kgPairOffset, base.DeviceID)
		}
		if sortDigits(base.DeviceID[3:]) != sortDigits(pair.DeviceID[3:]) {
			t.Errorf("records %d/%d: digits %q/%q are not permutations of each other",
				i, i+kgPairOffset, base.DeviceID[3:], pair.DeviceID[3:])
		}
	}

	// Spot-check the shape on record 0 so a reader can see what the corpus looks
	// like without running the live harness.
	r0, r100 := genKGRecord(0), genKGRecord(kgPairOffset)
	t.Logf("record   0: %s", r0.Sentence)
	t.Logf("record %d: %s", kgPairOffset, r100.Sentence)
}

func sortDigits(s string) string {
	b := []byte(s)
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j] < b[j-1]; j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
	return string(b)
}

// TestKGCorpusIDsUnique checks that all 200 device IDs are distinct. A duplicate
// ID would make retrieval_hit ambiguous and ground truth non-unique.
func TestKGCorpusIDsUnique(t *testing.T) {
	seen := make(map[string]int, kgCorpusSize)
	for _, rec := range kgCorpus() {
		if prev, dup := seen[rec.DeviceID]; dup {
			t.Errorf("device ID %s is used by both record %d and record %d", rec.DeviceID, prev, rec.Index)
		}
		seen[rec.DeviceID] = rec.Index
	}
	if len(seen) != kgCorpusSize {
		t.Errorf("got %d unique device IDs over %d records, want %d", len(seen), kgCorpusSize, kgCorpusSize)
	}
}

// TestKGCorpusAttributeRanges pins the attribute ranges and the ID shape, and
// checks that the rendered sentence carries every attribute — the sentence is
// the only thing a model ever sees, so an attribute missing from it would be
// unanswerable even with perfect retrieval.
func TestKGCorpusAttributeRanges(t *testing.T) {
	for _, rec := range kgCorpus() {
		if rec.RatedWatts < 200 || rec.RatedWatts > 2000 {
			t.Errorf("record %d: rated_watts=%d outside [200,2000]", rec.Index, rec.RatedWatts)
		}
		if rec.FirmwareBuild < 100 || rec.FirmwareBuild > 999 {
			t.Errorf("record %d: firmware_build=%d outside [100,999]", rec.Index, rec.FirmwareBuild)
		}
		if rec.BayNumber < 1 || rec.BayNumber > 96 {
			t.Errorf("record %d: bay_number=%d outside [1,96]", rec.Index, rec.BayNumber)
		}
		if rec.CommissionedYear < 2004 || rec.CommissionedYear > 2024 {
			t.Errorf("record %d: commissioned_year=%d outside [2004,2024]", rec.Index, rec.CommissionedYear)
		}

		// ID shape: two uppercase letters, a hyphen, four digits.
		if len(rec.DeviceID) != 7 || rec.DeviceID[2] != '-' {
			t.Fatalf("record %d: device ID %q is not LL-DDDD", rec.Index, rec.DeviceID)
		}
		for i, ch := range []byte(rec.DeviceID[:2]) {
			if ch < 'A' || ch > 'Z' {
				t.Errorf("record %d: device ID %q byte %d is not an uppercase letter", rec.Index, rec.DeviceID, i)
			}
		}
		for i, ch := range []byte(rec.DeviceID[3:]) {
			if ch < '0' || ch > '9' {
				t.Errorf("record %d: device ID %q byte %d is not a digit", rec.Index, rec.DeviceID, i+3)
			}
		}

		for _, want := range []string{
			rec.DeviceID,
			fmt.Sprintf("firmware build %d", rec.FirmwareBuild),
			fmt.Sprintf("%d watts", rec.RatedWatts),
			fmt.Sprintf("bay %d", rec.BayNumber),
			fmt.Sprintf("commissioned in %d", rec.CommissionedYear),
		} {
			if !strings.Contains(rec.Sentence, want) {
				t.Errorf("record %d: sentence %q is missing %q", rec.Index, rec.Sentence, want)
			}
		}
	}
}

// TestKGQuestions checks the question set: 30 questions over 30 distinct devices,
// truth read from the structured record, attributes cycled evenly, and every
// question answerable from its gold sentence alone.
func TestKGQuestions(t *testing.T) {
	seenText := make(map[string]int, kgQuestionCount)
	seenDevice := make(map[string]int, kgQuestionCount)
	byAttr := map[string]int{}

	for i := 0; i < kgQuestionCount; i++ {
		q := genKGQuestion(i)

		if prev, dup := seenText[q.Text]; dup {
			t.Errorf("question %d duplicates question %d: %q", i, prev, q.Text)
		}
		seenText[q.Text] = i

		if prev, dup := seenDevice[q.Gold.DeviceID]; dup {
			t.Errorf("question %d asks about device %s, already asked by question %d",
				i, q.Gold.DeviceID, prev)
		}
		seenDevice[q.Gold.DeviceID] = i

		byAttr[q.Attribute]++

		if !strings.Contains(q.Text, q.Gold.DeviceID) {
			t.Errorf("question %d does not name its device: %q", i, q.Text)
		}
		// The truth must be the structured field, and it must be present in the
		// gold sentence — that is what makes the oracle arm a true RAG ceiling.
		if !strings.Contains(q.Gold.Sentence, fmt.Sprintf("%d", q.Truth)) {
			t.Errorf("question %d: truth %d is not present in the gold sentence %q", i, q.Truth, q.Gold.Sentence)
		}
		if q.Truth == kgAbstain {
			t.Errorf("question %d: truth collides with the abstention sentinel %d", i, kgAbstain)
		}
	}

	if len(seenText) != kgQuestionCount {
		t.Errorf("got %d unique questions, want %d", len(seenText), kgQuestionCount)
	}
	// 30 questions over 4 attributes: 8,8,7,7.
	for _, attr := range kgAttributes {
		if n := byAttr[attr.Key]; n < 7 || n > 8 {
			t.Errorf("attribute %s asked %d times, want 7 or 8 (round-robin over %d questions)",
				attr.Key, n, kgQuestionCount)
		}
	}
	t.Logf("question 0: %q truth=%d (%s)", genKGQuestion(0).Text, genKGQuestion(0).Truth, genKGQuestion(0).Attribute)
}

// TestKGFTS5QueryEscaping pins the query rewrite the BM25 arm depends on.
//
// This is not cosmetic. A raw question string handed to memory.RecallOpts.Query
// is an FTS5 SYNTAX ERROR, twice over: the trailing "?" is not a legal bare
// term, and an unquoted "KX-4471" parses as a column reference ("no such column:
// 4471"). kgFTS5Query is what makes the retrieval arm run at all; see its
// comment for the details.
func TestKGFTS5QueryEscaping(t *testing.T) {
	got := kgFTS5Query("What is the rated wattage of device KX-4471?")
	want := `"What" OR "is" OR "the" OR "rated" OR "wattage" OR "of" OR "device" OR "KX-4471"`
	if got != want {
		t.Errorf("kgFTS5Query() = %q, want %q", got, want)
	}
	if strings.Contains(got, "?") {
		t.Errorf("query still carries a bare %q, which FTS5 rejects: %q", "?", got)
	}
	// Every real question must survive the rewrite with its device ID intact —
	// the ID is the only discriminative term in the query.
	for i := 0; i < kgQuestionCount; i++ {
		q := genKGQuestion(i)
		fts := kgFTS5Query(q.Text)
		if !strings.Contains(fts, `"`+q.Gold.DeviceID+`"`) {
			t.Errorf("question %d: rewritten query %q lost the device ID %s", i, fts, q.Gold.DeviceID)
		}
	}
}
