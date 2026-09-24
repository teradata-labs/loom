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

package decision

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// SizeHinter is implemented by deciders that know how many questions one
// request should carry. Chunked reads it when no explicit size is set.
type SizeHinter interface {
	MaxQuestionsPerRequest() int
}

// DefaultChunkConcurrency bounds how many chunks of one request are in
// flight at once.
const DefaultChunkConcurrency = 4

// Chunked splits a request that carries more questions than a decider
// handles well into concurrent smaller requests and merges the answers.
// The limit is the configured size, else the decider's SizeHinter, else
// none. Whatever the limit, a chunk that fails with an overload, rate-limit,
// size or transport error and still has more than one question is bisected
// and both halves retried, so an unknown or shifting provider limit is
// found at run time instead of failing the whole request. Auth, validation
// and malformed-answer errors are not retried: they would fail at any size.
//
// A fan-out request (DecisionRequest.fan_out_key) has its state carved with
// its questions: each chunk keeps only the items for its own question ids,
// so the request the provider sees shrinks with the question set. Every
// other state is copied unchanged into every chunk. Answers are the same
// either way; only the number of round trips changes. Usage is summed.
type Chunked struct {
	decider     Decider
	size        int
	concurrency int
	bisect      bool
}

// ChunkOption configures Chunk.
type ChunkOption func(*Chunked)

// WithChunkSize sets the most questions one request carries. 0 keeps the
// decider's hint (or no pre-split when it has none).
func WithChunkSize(n int) ChunkOption { return func(c *Chunked) { c.size = n } }

// WithChunkConcurrency bounds chunks in flight for one request (default
// DefaultChunkConcurrency, minimum 1).
func WithChunkConcurrency(n int) ChunkOption {
	return func(c *Chunked) {
		if n >= 1 {
			c.concurrency = n
		}
	}
}

// WithoutBisect disables splitting a failed chunk; the failure is returned.
func WithoutBisect() ChunkOption { return func(c *Chunked) { c.bisect = false } }

// Chunk wraps d. A nil d is returned as is.
func Chunk(d Decider, opts ...ChunkOption) Decider {
	if d == nil {
		return nil
	}
	c := &Chunked{decider: d, concurrency: DefaultChunkConcurrency, bisect: true}
	for _, o := range opts {
		o(c)
	}
	if c.size <= 0 {
		if h, ok := d.(SizeHinter); ok {
			c.size = h.MaxQuestionsPerRequest()
		}
	}
	if c.size < 0 {
		c.size = 0
	}
	return c
}

// Name implements Decider.
func (c *Chunked) Name() string { return c.decider.Name() }

// Model implements Decider.
func (c *Chunked) Model() string { return c.decider.Model() }

// Unwrap returns the wrapped decider.
func (c *Chunked) Unwrap() Decider { return c.decider }

// Size is the configured chunk size (0 = no pre-split).
func (c *Chunked) Size() int { return c.size }

// Decide implements Decider.
func (c *Chunked) Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrValidation)
	}
	n := len(req.Questions)
	if n == 0 || (c.size == 0 || n <= c.size) {
		resp, err := c.decider.Decide(ctx, req)
		if err == nil || !c.bisect || n < 2 || !chunkRetryable(ctx, err) {
			return resp, err
		}
		// The whole request failed with something a smaller request may
		// clear: bisect from here.
		return c.decideIDs(ctx, req, sortedQuestionIDs(req), 1)
	}
	return c.decideIDs(ctx, req, sortedQuestionIDs(req), 0)
}

// decideIDs answers the given question ids, splitting into chunks of c.size
// (or in half when depth > 0, i.e. after a failure) and merging.
func (c *Chunked) decideIDs(ctx context.Context, req *loomv1.DecisionRequest, ids []string, depth int) (*loomv1.DecisionResponse, error) {
	var parts [][]string
	switch {
	case depth == 0 && c.size > 0 && len(ids) > c.size:
		for i := 0; i < len(ids); i += c.size {
			end := i + c.size
			if end > len(ids) {
				end = len(ids)
			}
			parts = append(parts, ids[i:end])
		}
	case depth > 0 && len(ids) >= 2:
		mid := len(ids) / 2
		parts = [][]string{ids[:mid], ids[mid:]}
	default:
		parts = [][]string{ids}
	}
	if len(parts) == 1 {
		return c.decideOne(ctx, req, parts[0], depth)
	}

	results := make([]*loomv1.DecisionResponse, len(parts))
	errs := make([]error, len(parts))
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	for i, part := range parts {
		wg.Add(1)
		go func(i int, part []string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			results[i], errs[i] = c.decideOne(ctx, req, part, depth)
		}(i, part)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return mergeResponses(results), nil
}

// decideOne sends one chunk; on a retryable failure with more than one
// question it bisects (bounded by the question count, so at most log2(n)
// levels deep).
func (c *Chunked) decideOne(ctx context.Context, req *loomv1.DecisionRequest, ids []string, depth int) (*loomv1.DecisionResponse, error) {
	sub := SubRequest(req, ids)
	resp, err := c.decider.Decide(ctx, sub)
	if err == nil {
		return resp, nil
	}
	if !c.bisect || len(ids) < 2 || !chunkRetryable(ctx, err) {
		return nil, err
	}
	return c.decideIDs(ctx, req, ids, depth+1)
}

// chunkRetryable says whether a smaller request might succeed where this
// one failed. Context cancellation is the caller's, not the provider's.
func chunkRetryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch {
	case errors.Is(err, ErrUnauthorized), errors.Is(err, ErrValidation),
		errors.Is(err, ErrMalformedAnswer), errors.Is(err, ErrDisabled), errors.Is(err, ErrBudgetExhausted):
		return false
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return false
	}
	// Overload, rate limit, state too large, transport failures, a chunk's
	// own deadline: all may clear with less work per request.
	return true
}

// sortedQuestionIDs returns the request's question ids in a stable order so
// chunks are deterministic.
func sortedQuestionIDs(req *loomv1.DecisionRequest) []string {
	ids := make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SubRequest is req restricted to the given question ids. When the request
// names a fan-out key, the state array under it keeps only the items whose
// "id" is one of ids; everything else in state is copied unchanged. The
// original request is not modified.
func SubRequest(req *loomv1.DecisionRequest, ids []string) *loomv1.DecisionRequest {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	sub := &loomv1.DecisionRequest{
		Model:     req.Model,
		Site:      req.Site,
		FanOutKey: req.FanOutKey,
		Questions: make(map[string]*loomv1.DecisionQuestion, len(ids)),
	}
	for id := range want {
		if q, ok := req.Questions[id]; ok {
			sub.Questions[id] = q
		}
	}
	sub.State = req.State
	if req.FanOutKey != "" && req.State != nil {
		if st := req.State.GetStructValue(); st != nil {
			if list := st.Fields[req.FanOutKey].GetListValue(); list != nil {
				clone := proto.Clone(st).(*structpb.Struct)
				kept := make([]*structpb.Value, 0, len(ids))
				for _, item := range list.Values {
					if id, ok := fanOutItemID(item); ok {
						if _, keep := want[id]; !keep {
							continue
						}
					}
					kept = append(kept, item)
				}
				clone.Fields[req.FanOutKey] = structpb.NewListValue(&structpb.ListValue{Values: kept})
				sub.State = structpb.NewStructValue(clone)
			}
		}
	}
	return sub
}

// fanOutItemID reads an item's "id" field. Items without one are kept in
// every chunk (they are context, not candidates).
func fanOutItemID(item *structpb.Value) (string, bool) {
	st := item.GetStructValue()
	if st == nil {
		return "", false
	}
	v, ok := st.Fields["id"]
	if !ok {
		return "", false
	}
	if s, isStr := v.GetKind().(*structpb.Value_StringValue); isStr {
		return s.StringValue, true
	}
	return "", false
}

// mergeResponses unions the chunks' answers and sums their usage. Model is
// the first non-empty one.
func mergeResponses(parts []*loomv1.DecisionResponse) *loomv1.DecisionResponse {
	out := &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{}, Usage: &loomv1.DecisionUsage{}}
	for _, p := range parts {
		if p == nil {
			continue
		}
		if out.Model == "" {
			out.Model = p.Model
		}
		for id, a := range p.Answers {
			out.Answers[id] = a
		}
		if p.Usage != nil {
			out.Usage.InputTokens += p.Usage.InputTokens
			out.Usage.OutputTokens += p.Usage.OutputTokens
			out.Usage.CostUsd += p.Usage.CostUsd
		}
	}
	return out
}
