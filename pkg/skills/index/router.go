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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// Resolver resolves a candidate skill name back to its full Skill record.
// skills.Library satisfies this via its Load() method.
type Resolver interface {
	Load(name string) (*skills.Skill, error)
}

// Router walks the SkillIndex via an LLM to pick skills relevant to a user
// message. The walk is bounded by depth and width to cap latency and cost.
//
// Concurrency: SetTree is the only mutating method and is safe for use
// concurrent with Route. Route never mutates router state.
type Router struct {
	mu       sync.RWMutex
	tree     *Tree
	resolver Resolver
	llm      types.LLMProvider
	cache    *Cache
	tracer   observability.Tracer
	logger   *zap.Logger

	maxDepth      int
	maxBranching  int
	maxCandidates int
	maxRetries    int
}

// RouterOption configures a Router during construction.
type RouterOption func(*Router)

// WithRouterLLM attaches the LLM used for tree-walk decisions. Required.
func WithRouterLLM(llm types.LLMProvider) RouterOption {
	return func(r *Router) { r.llm = llm }
}

// WithRouterCache attaches a session-scoped decision cache.
func WithRouterCache(c *Cache) RouterOption {
	return func(r *Router) { r.cache = c }
}

// WithRouterTracer attaches an observability tracer.
func WithRouterTracer(t observability.Tracer) RouterOption {
	return func(r *Router) {
		if t != nil {
			r.tracer = t
		}
	}
}

// WithRouterLogger attaches a zap logger.
func WithRouterLogger(l *zap.Logger) RouterOption {
	return func(r *Router) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithMaxDepth caps how deep the tree walk descends. Default 4 (root + 4
// levels). Hard limit prevents pathological prompts on degenerate trees.
func WithMaxDepth(d int) RouterOption {
	return func(r *Router) {
		if d > 0 {
			r.maxDepth = d
		}
	}
}

// WithMaxBranching caps how many siblings are presented to the LLM per
// step. Default 12. Larger trees are window-projected by selecting the
// alphabetically-first N children. (We rely on PageIndex-style
// summaries to make even a windowed view useful.)
func WithMaxBranching(n int) RouterOption {
	return func(r *Router) {
		if n > 0 {
			r.maxBranching = n
		}
	}
}

// WithRouterMaxCandidates caps the number of skills returned. Default 3,
// aligned with skills.SkillsConfig.MaxConcurrentSkills.
func WithRouterMaxCandidates(n int) RouterOption {
	return func(r *Router) {
		if n > 0 {
			r.maxCandidates = n
		}
	}
}

// NewRouter builds a Router. resolver is used to expand selected skill
// names into full Skill records; library is the typical source.
func NewRouter(resolver Resolver, opts ...RouterOption) *Router {
	r := &Router{
		resolver:     resolver,
		tracer:       observability.NewNoOpTracer(),
		logger:       zap.NewNop(),
		maxDepth:     4,
		maxBranching: 12,
		// Aligned with skills.SkillsConfig.MaxConcurrentSkills (default 3).
		// Keeping these in sync prevents the orchestrator from silently
		// dropping router decisions: anything the router emits beyond the
		// orchestrator's cap gets sorted alphabetically and trimmed,
		// wasting per-leaf LLM picks. Aligning at 3 also lowers the
		// leaf-filter threshold so any bucket with 4+ skills engages
		// pickFromFatLeaf, letting the LLM pick the relevant subset
		// instead of dumping the alphabetical-first slice.
		maxCandidates: 3,
		maxRetries:    1,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// SetTree replaces the navigation tree. Called by Discovery whenever the
// index is (re)built. Safe to call concurrently with Route.
func (r *Router) SetTree(tree *Tree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tree = tree
}

// Tree returns the currently-installed navigation tree.
func (r *Router) Tree() *Tree {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tree
}

// Route walks the tree given a user message and returns up to MaxCandidates
// skills relevant to it. eligible filters the result so a skill never
// surfaces to an agent that hasn't bound it. When eligible is nil, no
// filtering is applied and any skill in the tree may surface.
//
// Returns (nil, nil) when the router can't make a decision (no tree, no
// LLM, or LLM error). Callers should fall back to FTS5 in that case;
// errors are returned for unrecoverable issues only.
func (r *Router) Route(ctx context.Context, sessionID, message string,
	eligible map[string]bool, bindingsHash string) ([]*skills.Skill, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.RLock()
	tree := r.tree
	r.mu.RUnlock()
	if tree == nil || tree.RootNode() == nil {
		return nil, nil
	}
	if r.llm == nil {
		return nil, nil
	}

	ctx, span := r.tracer.StartSpan(ctx, "skills.index.router.route")
	defer r.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("session.id", sessionID)
		span.SetAttribute("message.length", fmt.Sprintf("%d", len(message)))
	}

	cacheKey := CacheKey{
		SessionID:    sessionID,
		MessageHash:  HashMessage(message),
		BindingsHash: bindingsHash,
	}
	if cached := r.cache.Get(cacheKey); cached != nil {
		if span != nil {
			span.SetAttribute("cache.hit", "true")
		}
		return filterEligible(cached, eligible, r.maxCandidates), nil
	}

	// BFS-style descent: at each step, the LLM picks zero or more children
	// to descend AND zero or more skills to select directly.
	visited := map[string]bool{}
	frontier := []*skills.SkillIndexNode{tree.RootNode()}
	selectedNames := map[string]bool{}

	for depth := 0; depth < r.maxDepth && len(frontier) > 0; depth++ {
		nextFrontier := nextFrontier{}
		for _, node := range frontier {
			if visited[node.ID] {
				continue
			}
			visited[node.ID] = true

			children := tree.Children(node.ID)
			children = sortAndCap(children, r.maxBranching)

			directSkills := r.expandSkills(node.SkillRefs, eligible)
			if len(children) == 0 && len(directSkills) == 0 {
				continue
			}

			// At a leaf with attached skills and no children:
			//   - if the leaf is small (<= maxCandidates), surface them all
			//     without an LLM call: the router has already arrived and
			//     the orchestrator's MaxConcurrentSkills cap will narrow
			//     further if needed.
			//   - if the leaf is fat, do a per-leaf LLM pick so we don't
			//     dump every skill into the agent's context. This is the
			//     fallback for nodes where the index builder didn't (or
			//     couldn't) sub-categorize.
			if len(children) == 0 {
				if len(directSkills) <= r.maxCandidates {
					for _, s := range directSkills {
						selectedNames[s.Name] = true
					}
					continue
				}
				picked, err := r.pickFromFatLeaf(ctx, message, node, directSkills)
				if err != nil {
					r.logger.Debug("router fat-leaf pick failed; surfacing alphabetical-first slice",
						zap.String("node", node.ID),
						zap.Int("leaf_skills", len(directSkills)),
						zap.Error(err))
					// Deterministic degradation: take the first maxCandidates
					// in stable order so the response isn't silently empty.
					for i, s := range directSkills {
						if i >= r.maxCandidates {
							break
						}
						selectedNames[s.Name] = true
					}
					continue
				}
				for _, name := range picked {
					selectedNames[name] = true
				}
				continue
			}

			decision, err := r.askDecision(ctx, message, node, children, directSkills)
			if err != nil {
				r.logger.Debug("router LLM call failed; treating as no-decision",
					zap.String("node", node.ID),
					zap.Error(err))
				return nil, nil
			}
			r.logger.Debug("router askDecision result",
				zap.String("node_title", node.Title),
				zap.Strings("descend", decision.Descend),
				zap.Strings("skills", decision.Skills),
				zap.String("reason", decision.Reason),
			)

			for _, name := range decision.Skills {
				selectedNames[name] = true
			}
			for _, childID := range decision.Descend {
				if c := tree.Get(childID); c != nil {
					nextFrontier.add(c)
				}
			}
		}
		frontier = nextFrontier.list
	}

	// Resolve names to full Skill records.
	out := make([]*skills.Skill, 0, len(selectedNames))
	for name := range selectedNames {
		s, err := r.resolver.Load(name)
		if err != nil || s == nil {
			continue
		}
		out = append(out, s)
	}
	r.logger.Debug("router walk complete",
		zap.String("session", sessionID),
		zap.Int("selected_names", len(selectedNames)),
		zap.Int("resolved", len(out)),
		zap.Int("eligible_size", len(eligible)),
	)
	out = filterEligible(out, eligible, r.maxCandidates)

	// Cache decision (positive results only; cold results stay cold).
	if len(out) > 0 && r.cache != nil {
		r.cache.Put(cacheKey, out, 0)
	}
	return out, nil
}

// expandSkills resolves skill names to full Skill records, dropping any
// not in eligible (when eligible is non-nil).
func (r *Router) expandSkills(names []string, eligible map[string]bool) []*skills.Skill {
	out := make([]*skills.Skill, 0, len(names))
	for _, name := range names {
		if eligible != nil && !eligible[name] {
			continue
		}
		s, err := r.resolver.Load(name)
		if err != nil || s == nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// routerDecision is the JSON shape we ask the LLM to return at each step.
type routerDecision struct {
	Descend []string `json:"descend"` // child node ids to walk into
	Skills  []string `json:"skills"`  // skill names to select directly
	Reason  string   `json:"reason"`  // optional rationale (logged, not used)
}

// pickFromFatLeaf asks the LLM to select up to maxCandidates skills from a
// terminal-leaf node whose skill_refs exceed the candidate budget. This is
// the per-leaf filter that prevents fat leaves (e.g., 22 teradata-* skills
// flatly attached under a single "Teradata" node) from dumping every skill
// into the response.
//
// Returns the selected skill names. The skills argument is the already-
// eligibility-filtered slice surface for this node.
func (r *Router) pickFromFatLeaf(ctx context.Context, message string,
	node *skills.SkillIndexNode, candidates []*skills.Skill) ([]string, error) {
	prompt := buildLeafPickPrompt(message, node, candidates, r.maxCandidates)
	messages := []types.Message{{Role: "user", Content: prompt}}
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		resp, err := r.llm.Chat(ctx, messages, nil)
		if err != nil {
			return nil, err
		}
		picked, parseErr := parseLeafPick(resp.Content)
		if parseErr == nil {
			// Validate names against the candidate set; drop hallucinations.
			valid := make(map[string]bool, len(candidates))
			for _, c := range candidates {
				valid[c.Name] = true
			}
			out := make([]string, 0, len(picked))
			for _, name := range picked {
				if valid[name] {
					out = append(out, name)
				}
				if len(out) >= r.maxCandidates {
					break
				}
			}
			return out, nil
		}
		if attempt < r.maxRetries {
			messages = append(messages,
				types.Message{Role: "assistant", Content: resp.Content},
				types.Message{Role: "user", Content: fmt.Sprintf(
					"Output was not valid JSON. Error: %s\n\n"+
						"Respond with a single JSON object: {\"skills\":[<skill_names>],\"reason\":\"<short>\"}. "+
						"Pick at most %d names from the list provided. No markdown fences.",
					parseErr.Error(), r.maxCandidates)},
			)
			continue
		}
		return nil, fmt.Errorf("router leaf-pick parse failed: %w", parseErr)
	}
	return nil, fmt.Errorf("router: unreachable")
}

// buildLeafPickPrompt asks the LLM to pick a subset of skills attached to a
// fat terminal leaf. Tighter than the full router-step prompt because we
// already know we're not descending further.
func buildLeafPickPrompt(message string, node *skills.SkillIndexNode,
	candidates []*skills.Skill, maxPick int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pick the %d most relevant skills for the user's message from the list below.\n", maxPick)
	b.WriteString("Return a JSON object: {\"skills\":[<skill_names>],\"reason\":\"<short>\"}\n")
	b.WriteString("The \"skills\" array MUST contain exact names from the list (case-sensitive).\n")
	fmt.Fprintf(&b, "Pick at most %d names. Output ONLY the JSON object.\n\n", maxPick)

	fmt.Fprintf(&b, "User message:\n%s\n\n", strings.TrimSpace(message))
	fmt.Fprintf(&b, "Current node: %s\n", nodeLabel(node))
	if node.Summary != "" {
		fmt.Fprintf(&b, "Node summary: %s\n", node.Summary)
	}

	b.WriteString("\nSkills attached here:\n")
	for _, s := range candidates {
		t := s.Title
		if t == "" {
			t = s.Name
		}
		fmt.Fprintf(&b, "- name=%s | %s | %s\n",
			s.Name, t, truncate(s.Description, 200))
	}

	return b.String()
}

// leafPickDecision is the JSON shape pickFromFatLeaf expects. Distinct from
// routerDecision because there's no descend list at a terminal leaf.
type leafPickDecision struct {
	Skills []string `json:"skills"`
	Reason string   `json:"reason"`
}

// parseLeafPick is tolerant of the same LLM quirks as parseRouterDecision.
func parseLeafPick(raw string) ([]string, error) {
	cleaned := extractJSON(raw)
	var d leafPickDecision
	if err := json.Unmarshal([]byte(cleaned), &d); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w (first 200 chars: %s)",
			err, truncate(cleaned, 200))
	}
	return d.Skills, nil
}

// askDecision builds a router-step prompt and parses the LLM response.
func (r *Router) askDecision(ctx context.Context, message string,
	node *skills.SkillIndexNode, children []*skills.SkillIndexNode,
	directSkills []*skills.Skill) (*routerDecision, error) {
	prompt := buildRouterPrompt(message, node, children, directSkills)
	messages := []types.Message{{Role: "user", Content: prompt}}
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		resp, err := r.llm.Chat(ctx, messages, nil)
		if err != nil {
			return nil, err
		}
		decision, parseErr := parseRouterDecision(resp.Content)
		if parseErr == nil {
			return decision, nil
		}
		if attempt < r.maxRetries {
			messages = append(messages,
				types.Message{Role: "assistant", Content: resp.Content},
				types.Message{Role: "user", Content: fmt.Sprintf(
					"Output was not valid JSON. Error: %s\n\n"+
						"Respond with a single JSON object: {\"descend\":[<child_ids>],"+
						"\"skills\":[<skill_names>],\"reason\":\"<short>\"}. "+
						"Both arrays may be empty. No markdown fences.",
					parseErr.Error())},
			)
			continue
		}
		return nil, fmt.Errorf("router decision parse failed: %w", parseErr)
	}
	return nil, fmt.Errorf("router: unreachable")
}

// buildRouterPrompt asks the LLM to pick zero-or-more children to descend
// AND zero-or-more skills to select directly. The prompt is intentionally
// tight; thousands of routing calls per agent per day add up.
func buildRouterPrompt(message string, node *skills.SkillIndexNode,
	children []*skills.SkillIndexNode, directSkills []*skills.Skill) string {
	var b strings.Builder
	b.WriteString("You are routing a user message through a hierarchical skill index.\n")
	b.WriteString("Pick child subtrees to descend into AND/OR skills to select directly.\n")
	b.WriteString("Return a JSON object: {\"descend\":[<child_ids>],\"skills\":[<skill_names>],\"reason\":\"<short>\"}\n")
	b.WriteString("Both arrays may be empty. Output ONLY the JSON object.\n\n")

	fmt.Fprintf(&b, "User message:\n%s\n\n", strings.TrimSpace(message))
	fmt.Fprintf(&b, "Current node: %s\n", nodeLabel(node))
	if node.Summary != "" {
		fmt.Fprintf(&b, "Node summary: %s\n", node.Summary)
	}

	if len(children) > 0 {
		b.WriteString("\nChild subtrees (use ids in \"descend\"):\n")
		for _, c := range children {
			fmt.Fprintf(&b, "- id=%s | %s | %s\n",
				c.ID, c.Title, truncate(c.Summary, 200))
		}
	}

	if len(directSkills) > 0 {
		b.WriteString("\nSkills directly attached here (use names in \"skills\"):\n")
		for _, s := range directSkills {
			t := s.Title
			if t == "" {
				t = s.Name
			}
			fmt.Fprintf(&b, "- name=%s | %s | %s\n",
				s.Name, t, truncate(s.Description, 200))
		}
	}

	return b.String()
}

func nodeLabel(n *skills.SkillIndexNode) string {
	if n == nil {
		return "(none)"
	}
	if n.Title != "" {
		return fmt.Sprintf("%s (%s)", n.Title, n.ID)
	}
	return n.ID
}

// parseRouterDecision is tolerant of common LLM quirks (markdown fences,
// leading/trailing prose).
func parseRouterDecision(raw string) (*routerDecision, error) {
	cleaned := extractJSON(raw)
	var d routerDecision
	if err := json.Unmarshal([]byte(cleaned), &d); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w (first 200 chars: %s)",
			err, truncate(cleaned, 200))
	}
	return &d, nil
}

// extractJSON strips markdown code fences and trims to the first {..} block.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)
	for _, fence := range []string{"```json", "```JSON", "```", "~~~"} {
		s = strings.TrimPrefix(s, fence)
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSuffix(s, "~~~")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	return s
}

// sortAndCap returns at most n nodes sorted by Title (or ID) for stable
// presentation order to the LLM.
func sortAndCap(nodes []*skills.SkillIndexNode, n int) []*skills.SkillIndexNode {
	out := append([]*skills.SkillIndexNode(nil), nodes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ID < out[j].ID
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// filterEligible drops skills not in eligible (when non-nil) and caps the
// result at n. The eligible map represents the post-binding filter from
// the resolver; it ensures the router never returns a skill the agent
// hasn't bound.
func filterEligible(in []*skills.Skill, eligible map[string]bool, n int) []*skills.Skill {
	out := in
	if eligible != nil {
		out = out[:0]
		for _, s := range in {
			if eligible[s.Name] {
				out = append(out, s)
			}
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// nextFrontier dedupes nodes within a single BFS step to avoid expanding
// the same subtree twice when two parents share a child id.
type nextFrontier struct {
	seen map[string]bool
	list []*skills.SkillIndexNode
}

func (f *nextFrontier) add(n *skills.SkillIndexNode) {
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[n.ID] {
		return
	}
	f.seen[n.ID] = true
	f.list = append(f.list, n)
}
