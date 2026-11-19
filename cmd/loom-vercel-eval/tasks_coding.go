package main

// codingTasks are A1–A6: file and code work in a small Go module or text
// fixture. Checks run the Go toolchain or compare bytes; the model is not
// consulted.
func codingTasks() []task {
	return []task{
		{
			ID:       "A1",
			Category: "coding",
			Summary:  "fix a failing test without editing the test",
			Turns:    []string{"The tests in pricing/ fail. Find the bug and fix it without changing the tests."},
			Files: map[string]string{
				"go.mod": "module example.com/pricing\n\ngo 1.22\n",
				"pricing/pricing.go": `package pricing

// Discount returns price after a percentage discount of pct percent.
func Discount(price float64, pct int) float64 {
	return price * float64(100-pct+1) / 100
}
`,
				"pricing/pricing_test.go": `package pricing

import "testing"

func TestDiscount(t *testing.T) {
	cases := []struct {
		price float64
		pct   int
		want  float64
	}{
		{100, 10, 90},
		{50, 0, 50},
		{200, 50, 100},
		{80, 25, 60},
	}
	for _, c := range cases {
		if got := Discount(c.price, c.pct); got != c.want {
			t.Errorf("Discount(%v, %d) = %v, want %v", c.price, c.pct, got, c.want)
		}
	}
}
`,
			},
			Checks: []check{
				{Kind: checkCmdExit0, Value: "go test ./pricing/..."},
				{Kind: checkFileUnchanged, Path: "pricing/pricing_test.go"},
			},
		},
		{
			ID:       "A2",
			Category: "coding",
			Summary:  "rename a function across the module, keeping a string literal",
			Turns:    []string{"Rename GetUser to FetchUser across the module, including callers and comments. Keep behaviour the same."},
			Files: map[string]string{
				"go.mod": "module example.com/users\n\ngo 1.22\n",
				"users/users.go": `package users

import "fmt"

type User struct {
	ID   int
	Name string
}

var store = map[int]User{1: {1, "ana"}, 2: {2, "bo"}}

// GetUser returns the user with the given id.
func GetUser(id int) (User, error) {
	u, ok := store[id]
	if !ok {
		return User{}, fmt.Errorf("GetUser: not found: %d", id)
	}
	return u, nil
}
`,
				"users/cache.go": `package users

var cache = map[int]User{}

// CachedUser wraps GetUser with a cache.
func CachedUser(id int) (User, error) {
	if u, ok := cache[id]; ok {
		return u, nil
	}
	u, err := GetUser(id)
	if err != nil {
		return User{}, err
	}
	cache[id] = u
	return u, nil
}
`,
				"api/handler.go": `package api

import (
	"fmt"

	"example.com/users/users"
)

// Handle looks up a user by id. It calls GetUser for each request.
func Handle(id int) string {
	u, err := users.GetUser(id)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("hello %s", u.Name)
}
`,
			},
			Checks: []check{
				{Kind: checkCmdExit0, Value: "go build ./... && go vet ./..."},
				{Kind: checkFileContains, Path: "users/users.go", Value: "func FetchUser("},
				{Kind: checkFileContains, Path: "api/handler.go", Value: "users.FetchUser("},
				{Kind: checkFileContains, Path: "users/users.go", Value: `"GetUser: not found: %d"`},
				{Kind: checkCmdExit0, Value: `test -z "$(grep -rn 'GetUser' --include=*.go . | grep -v 'GetUser: not found')"`},
			},
		},
		{
			ID:       "A3",
			Category: "coding",
			Summary:  "add a --dry-run flag that writes nothing",
			Turns:    []string{"Add a --dry-run flag to the CLI in cmd/tool. With --dry-run it must print what it would write and must not create any files."},
			Files: map[string]string{
				"go.mod":    "module example.com/tool\n\ngo 1.22\n",
				"input.txt": "hello world\n",
				"cmd/tool/main.go": `package main

import (
	"os"
	"path/filepath"
	"strings"
)

func main() {
	data, err := os.ReadFile("input.txt")
	if err != nil {
		panic(err)
	}
	out := strings.ToUpper(string(data))
	if err := os.MkdirAll("out", 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join("out", "result.txt"), []byte(out), 0o644); err != nil {
		panic(err)
	}
	println("wrote out/result.txt")
}
`,
			},
			Checks: []check{
				// Dry run first: no output file may appear. Then the real run must write it.
				{Kind: checkCmdExit0, Value: "go run ./cmd/tool --dry-run && test ! -e out/result.txt && go run ./cmd/tool && test -e out/result.txt"},
			},
		},
		{
			ID:       "A4",
			Category: "coding",
			Summary:  "change one YAML value and keep comments and anchors",
			Turns:    []string{"In config.yaml, set the timeout for the billing service to 30s. Leave everything else alone, including comments."},
			Files: map[string]string{
				"config.yaml": `# service settings
defaults: &defaults
  timeout: 10s
  retries: 3

services:
  billing:
    <<: *defaults
    timeout: 5s   # tuned for batch jobs
  search:
    <<: *defaults
`,
			},
			Checks: []check{
				{Kind: checkFileContains, Path: "config.yaml", Value: "timeout: 30s   # tuned for batch jobs"},
				{Kind: checkFileNotContain, Path: "config.yaml", Value: "timeout: 5s"},
				{Kind: checkFileContains, Path: "config.yaml", Value: "# service settings"},
				{Kind: checkFileContains, Path: "config.yaml", Value: "<<: *defaults"},
				{Kind: checkFileContains, Path: "config.yaml", Value: "timeout: 10s"},
			},
		},
		{
			ID:       "A5",
			Category: "coding",
			Summary:  "make the README match the code, code is the source of truth",
			Turns:    []string{"The README says the server listens on 8080 but main.go uses 9090. Make them agree, using the code as the source of truth."},
			Files: map[string]string{
				"main.go": `package main

const addr = ":9090"

func main() {
	println("listening on", addr)
}
`,
				"README.md": "# Server\n\nThe server listens on port 8080.\n",
			},
			Checks: []check{
				{Kind: checkFileUnchanged, Path: "main.go"},
				{Kind: checkFileContains, Path: "README.md", Value: "9090"},
				{Kind: checkFileNotContain, Path: "README.md", Value: "8080"},
			},
		},
		{
			ID:       "A6",
			Category: "coding",
			Summary:  "stop an unbounded retry loop, leave the tests and unrelated code alone",
			Turns:    []string{"Find the retry loop that never stops and fix it so it gives up after 5 attempts. Do not change the tests."},
			Files: map[string]string{
				"go.mod": "module example.com/retry\n\ngo 1.22\n",
				"retry.go": `package retry

import "errors"

// ErrGiveUp is returned when every attempt failed.
var ErrGiveUp = errors.New("gave up")

// Do calls fn until it succeeds, giving up after 5 attempts.
func Do(fn func() error) error {
	for attempt := 1; ; attempt++ {
		if err := fn(); err == nil {
			return nil
		}
	}
}
`,
				"retry_test.go": `package retry

import (
	"errors"
	"testing"
)

func TestDoSucceeds(t *testing.T) {
	n := 0
	err := Do(func() error {
		n++
		if n < 3 {
			return errors.New("x")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestDoGivesUp(t *testing.T) {
	n := 0
	err := Do(func() error {
		n++
		return errors.New("x")
	})
	if err == nil || n != 5 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}
`,
				"util.go": `package retry

// Sum adds the values in xs.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
`,
			},
			Checks: []check{
				{Kind: checkCmdExit0, Value: "go test ./..."},
				{Kind: checkFileUnchanged, Path: "retry_test.go"},
				{Kind: checkFileUnchanged, Path: "util.go"},
			},
		},
	}
}
