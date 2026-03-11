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

package skills

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"gopkg.in/yaml.v3"
)

// Library manages skill discovery, loading, caching, and search.
// It supports three tiers of skill sources in order of precedence:
//  1. Configured search paths (from agent config skills_dir)
//  2. $LOOM_SKILLS_DIR env var (default $HOME/.loom/skills/)
//  3. Embedded examples (lowest precedence, read-only)
//
// Thread safety: All public methods are safe for concurrent use.
type Library struct {
	mu         sync.RWMutex
	skillCache map[string]*Skill // name -> loaded skill
	skillIndex []SkillSummary    // cached catalog of all skills
	indexInit  bool              // whether skillIndex has been populated

	searchPaths []string  // directories to search (highest precedence first)
	embeddedFS  *embed.FS // optional embedded skills (lowest precedence)
	tracer      observability.Tracer
}

// LibraryOption configures a Library during construction.
type LibraryOption func(*Library)

// WithSearchPaths adds filesystem directories to search for skill YAML files.
// Paths are searched in the order provided, with earlier paths taking precedence.
func WithSearchPaths(paths ...string) LibraryOption {
	return func(l *Library) {
		l.searchPaths = append(l.searchPaths, paths...)
	}
}

// WithEmbeddedFS sets an embedded filesystem as the lowest-precedence skill source.
func WithEmbeddedFS(efs *embed.FS) LibraryOption {
	return func(l *Library) {
		l.embeddedFS = efs
	}
}

// WithTracer sets the observability tracer for the library.
func WithTracer(t observability.Tracer) LibraryOption {
	return func(l *Library) {
		l.tracer = t
	}
}

// NewLibrary creates a skill library with the given options.
// If no search paths are provided, it defaults to $LOOM_SKILLS_DIR
// (falling back to $HOME/.loom/skills/).
func NewLibrary(opts ...LibraryOption) *Library {
	l := &Library{
		skillCache: make(map[string]*Skill),
		tracer:     observability.NewNoOpTracer(),
	}

	for _, opt := range opts {
		opt(l)
	}

	// If no search paths configured, use the environment default.
	if len(l.searchPaths) == 0 {
		envDir := os.Getenv("LOOM_SKILLS_DIR")
		if envDir == "" {
			home, err := os.UserHomeDir()
			if err == nil {
				envDir = filepath.Join(home, ".loom", "skills")
			}
		}
		if envDir != "" {
			l.searchPaths = []string{envDir}
		}
	}

	return l
}

// Load reads a skill by name.
// It checks three tiers in order: cache, filesystem search paths, embedded FS.
// Returns an error if the skill is not found in any source.
func (l *Library) Load(name string) (*Skill, error) {
	startTime := time.Now()
	_, span := l.tracer.StartSpan(context.Background(), "skills.library.load")
	defer l.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("skill.name", name)
	}

	// 1. Check cache
	l.mu.RLock()
	cached, found := l.skillCache[name]
	l.mu.RUnlock()

	if found {
		l.recordLoadMetric(span, startTime, "cache", true)
		return cached, nil
	}

	// 2. Try filesystem search paths
	skill, err := l.loadFromFilesystem(name)
	if err == nil {
		l.cacheSkill(name, skill)
		l.recordLoadMetric(span, startTime, "filesystem", true)
		return skill, nil
	}

	// 3. Try embedded FS
	if l.embeddedFS != nil {
		skill, err = l.loadFromEmbedded(name)
		if err == nil {
			l.cacheSkill(name, skill)
			l.recordLoadMetric(span, startTime, "embedded", true)
			return skill, nil
		}
	}

	l.recordLoadMetric(span, startTime, "not_found", false)
	if span != nil {
		span.RecordError(fmt.Errorf("skill not found: %s", name))
	}
	return nil, fmt.Errorf("skill not found: %s", name)
}

// ListAll returns summaries for all available skills.
// The result is cached; call InvalidateCache to force re-indexing.
// Skills from filesystem paths take precedence over embedded skills with the same name.
func (l *Library) ListAll() []SkillSummary {
	startTime := time.Now()
	_, span := l.tracer.StartSpan(context.Background(), "skills.library.list_all")
	defer l.tracer.EndSpan(span)

	// Return cached index if available.
	l.mu.RLock()
	if l.indexInit {
		idx := l.skillIndex
		l.mu.RUnlock()
		if span != nil {
			span.SetAttribute("index.cached", "true")
			span.SetAttribute("result.count", fmt.Sprintf("%d", len(idx)))
			span.SetAttribute("duration_ms", fmtDurationMS(startTime))
		}
		return idx
	}
	l.mu.RUnlock()

	// Build index: filesystem first, then embedded.
	seen := make(map[string]bool)
	summaries := make([]SkillSummary, 0)

	fsSummaries := l.indexFilesystem()
	for _, s := range fsSummaries {
		if !seen[s.Name] {
			seen[s.Name] = true
			summaries = append(summaries, s)
		}
	}

	if l.embeddedFS != nil {
		embSummaries := l.indexEmbedded()
		for _, s := range embSummaries {
			if !seen[s.Name] {
				seen[s.Name] = true
				summaries = append(summaries, s)
			}
		}
	}

	// Sort alphabetically by name for stable output.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})

	// Cache the index.
	l.mu.Lock()
	l.skillIndex = summaries
	l.indexInit = true
	l.mu.Unlock()

	if span != nil {
		span.SetAttribute("index.cached", "false")
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(summaries)))
		span.SetAttribute("duration_ms", fmtDurationMS(startTime))
	}
	l.tracer.RecordMetric("skills.library.list_all", 1.0, map[string]string{
		"result_count": fmt.Sprintf("%d", len(summaries)),
	})

	return summaries
}

// Search performs free-text keyword matching across skill name, title, and description.
// Returns scored results sorted by descending relevance.
func (l *Library) Search(query string) []*ScoredSkill {
	startTime := time.Now()
	_, span := l.tracer.StartSpan(context.Background(), "skills.library.search")
	defer l.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("search.query", query)
	}

	if query == "" {
		// Return all skills with score 1.0 when no query.
		all := l.allSkills()
		results := make([]*ScoredSkill, 0, len(all))
		for _, s := range all {
			results = append(results, &ScoredSkill{Skill: s, Score: 1.0})
		}
		return results
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	all := l.allSkills()
	var results []*ScoredSkill

	for _, skill := range all {
		score := scoreSkill(skill, queryTokens)
		if score > 0 {
			results = append(results, &ScoredSkill{Skill: skill, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if span != nil {
		span.SetAttribute("result.count", fmt.Sprintf("%d", len(results)))
		span.SetAttribute("duration_ms", fmtDurationMS(startTime))
	}

	return results
}

// FindBySlashCommand searches for a skill matching the given slash command.
// Returns the matching skill and true if found, nil and false otherwise.
func (l *Library) FindBySlashCommand(cmd string) (*Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	cmdLower := strings.ToLower(cmd)
	for _, skill := range l.skillCache {
		for _, sc := range skill.Trigger.SlashCommands {
			if strings.ToLower(sc) == cmdLower {
				return skill, true
			}
		}
	}

	return nil, false
}

// FindByKeywords matches a user message against each skill's trigger keywords.
// Returns scored results sorted by descending relevance.
func (l *Library) FindByKeywords(msg string) []*ScoredSkill {
	l.mu.RLock()
	defer l.mu.RUnlock()

	msgTokens := tokenize(msg)
	if len(msgTokens) == 0 {
		return nil
	}
	msgTokenSet := make(map[string]bool, len(msgTokens))
	for _, t := range msgTokens {
		msgTokenSet[t] = true
	}

	var results []*ScoredSkill

	for _, skill := range l.skillCache {
		if len(skill.Trigger.Keywords) == 0 {
			continue
		}
		matches := 0
		for _, kw := range skill.Trigger.Keywords {
			kwLower := strings.ToLower(kw)
			// Check if the keyword (which may be multi-word) appears as a token match
			// or as a substring of the message.
			if msgTokenSet[kwLower] || strings.Contains(strings.ToLower(msg), kwLower) {
				matches++
			}
		}
		if matches == 0 {
			continue
		}
		score := float64(matches) / float64(len(skill.Trigger.Keywords))
		results = append(results, &ScoredSkill{Skill: skill, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// ListByDomain returns all skills matching the given domain.
func (l *Library) ListByDomain(domain string) []*Skill {
	all := l.allSkills()
	domainLower := strings.ToLower(domain)

	var results []*Skill
	for _, s := range all {
		if strings.ToLower(s.Domain) == domainLower {
			results = append(results, s)
		}
	}
	return results
}

// WriteSkill writes a skill as YAML to the primary (first writable) search path.
// Uses SkillToYAML from loader.go for serialization.
func (l *Library) WriteSkill(skill *Skill) error {
	_, span := l.tracer.StartSpan(context.Background(), "skills.library.write_skill")
	defer l.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("skill.name", skill.Name)
	}

	data, err := SkillToYAML(skill)
	if err != nil {
		return fmt.Errorf("failed to serialize skill %s: %w", skill.Name, err)
	}

	// Find first writable search path.
	dir := l.writableDir()
	if dir == "" {
		return fmt.Errorf("no writable skills directory configured")
	}

	// Ensure directory exists.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create skills directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, skill.Name+".yaml")
	// Validate the resolved path stays within dir to prevent path traversal.
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) && cleanPath != cleanDir {
		return fmt.Errorf("skill name results in path outside skills directory: %s", skill.Name)
	}

	if err := os.WriteFile(cleanPath, data, 0o644); err != nil { // #nosec G306 -- skill YAML is not sensitive
		return fmt.Errorf("failed to write skill file %s: %w", cleanPath, err)
	}

	// Update cache with the new skill.
	l.mu.Lock()
	l.skillCache[skill.Name] = skill
	l.indexInit = false // Invalidate index so ListAll picks up the new skill.
	l.mu.Unlock()

	if span != nil {
		span.SetAttribute("skill.path", cleanPath)
	}

	return nil
}

// Register adds a skill directly to the in-memory cache.
// This is useful for programmatically created skills that don't come from YAML files.
func (l *Library) Register(skill *Skill) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.skillCache[skill.Name] = skill
	l.indexInit = false
}

// InvalidateCache clears the skill cache and index.
// The next call to Load or ListAll will re-read from disk/embedded sources.
func (l *Library) InvalidateCache() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.skillCache = make(map[string]*Skill)
	l.skillIndex = nil
	l.indexInit = false
}

// RemoveFromCache removes a single skill from the cache by name.
func (l *Library) RemoveFromCache(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.skillCache, name)
	l.indexInit = false
}

// ---------- internal helpers ----------

// loadFromFilesystem searches all configured search paths for {name}.yaml.
func (l *Library) loadFromFilesystem(name string) (*Skill, error) {
	for _, dir := range l.searchPaths {
		path := filepath.Join(dir, name+".yaml")
		cleanPath := filepath.Clean(path)
		cleanDir := filepath.Clean(dir)
		// Prevent directory traversal.
		if !strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) && cleanPath != cleanDir {
			continue
		}
		skill, err := LoadSkill(cleanPath)
		if err == nil {
			return skill, nil
		}
		// If the file doesn't exist, try the next path. If it exists but is
		// invalid, return the parse error immediately.
		if !os.IsNotExist(err) && !isNotExistWrapped(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("skill %s not found in filesystem search paths", name)
}

// loadFromEmbedded searches the embedded FS for {name}.yaml.
func (l *Library) loadFromEmbedded(name string) (*Skill, error) {
	if l.embeddedFS == nil {
		return nil, fmt.Errorf("no embedded FS configured")
	}

	// Try root-level first, then walk subdirectories.
	candidates := []string{name + ".yaml"}

	// Walk embedded FS to find the file in any subdirectory.
	_ = fs.WalkDir(l.embeddedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(path), ".yaml")
		if base == name && strings.HasSuffix(path, ".yaml") {
			candidates = append(candidates, path)
		}
		return nil
	})

	for _, path := range candidates {
		data, err := l.embeddedFS.ReadFile(path)
		if err != nil {
			continue
		}
		skill, err := parseSkillBytes(data, name)
		if err != nil {
			return nil, err
		}
		return skill, nil
	}

	return nil, fmt.Errorf("skill %s not found in embedded FS", name)
}

// indexFilesystem walks all search path directories and returns summaries for every .yaml skill.
func (l *Library) indexFilesystem() []SkillSummary {
	var summaries []SkillSummary
	seen := make(map[string]bool)

	for _, dir := range l.searchPaths {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			name := strings.TrimSuffix(filepath.Base(path), ".yaml")
			if seen[name] {
				return nil
			}
			skill, loadErr := LoadSkill(path)
			if loadErr != nil {
				return nil // skip invalid files
			}
			seen[name] = true
			l.cacheSkill(name, skill)
			summaries = append(summaries, skill.Summary())
			return nil
		})
	}

	return summaries
}

// indexEmbedded walks the embedded FS and returns summaries for every .yaml skill.
func (l *Library) indexEmbedded() []SkillSummary {
	if l.embeddedFS == nil {
		return nil
	}

	var summaries []SkillSummary

	_ = fs.WalkDir(l.embeddedFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, readErr := l.embeddedFS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		skill, parseErr := parseSkillBytes(data, name)
		if parseErr != nil {
			return nil // skip invalid files
		}
		l.cacheSkill(name, skill)
		summaries = append(summaries, skill.Summary())
		return nil
	})

	return summaries
}

// cacheSkill stores a skill in the cache under write lock.
func (l *Library) cacheSkill(name string, skill *Skill) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.skillCache[name] = skill
}

// allSkills returns a snapshot of all cached skills.
// If the cache is empty, it triggers ListAll to populate it first.
func (l *Library) allSkills() []*Skill {
	l.mu.RLock()
	count := len(l.skillCache)
	l.mu.RUnlock()

	// Ensure the cache is populated by indexing.
	if count == 0 {
		l.ListAll()
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]*Skill, 0, len(l.skillCache))
	for _, s := range l.skillCache {
		result = append(result, s)
	}
	return result
}

// writableDir returns the first search path that is writable (or creatable).
func (l *Library) writableDir() string {
	for _, dir := range l.searchPaths {
		// If it exists, check writability.
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			return dir
		}
		// If it doesn't exist, check if the parent is writable.
		parent := filepath.Dir(dir)
		if pInfo, pErr := os.Stat(parent); pErr == nil && pInfo.IsDir() {
			return dir
		}
	}
	return ""
}

// parseSkillBytes parses raw YAML bytes into a Skill using the loader's YAML types.
func parseSkillBytes(data []byte, name string) (*Skill, error) {
	// Expand environment variables.
	dataStr := os.ExpandEnv(string(data))

	var sy SkillYAML
	if err := yaml.Unmarshal([]byte(dataStr), &sy); err != nil {
		return nil, fmt.Errorf("failed to parse skill YAML for %s: %w", name, err)
	}

	if err := validateSkillYAML(&sy); err != nil {
		return nil, fmt.Errorf("invalid skill %s: %w", name, err)
	}

	return yamlToSkill(&sy), nil
}

// scoreSkill computes a relevance score for a skill against a set of query tokens.
// Score = (matched tokens / total query tokens) with boosts for name and title matches.
func scoreSkill(skill *Skill, queryTokens []string) float64 {
	searchText := strings.ToLower(fmt.Sprintf("%s %s %s %s",
		skill.Name, skill.Title, skill.Description, skill.Domain))

	// Include trigger keywords in searchable text.
	for _, kw := range skill.Trigger.Keywords {
		searchText += " " + strings.ToLower(kw)
	}

	matchCount := 0
	for _, token := range queryTokens {
		if strings.Contains(searchText, token) {
			matchCount++
		}
	}

	if matchCount == 0 {
		return 0
	}

	score := float64(matchCount) / float64(len(queryTokens))

	// Boost for name/title matches.
	nameLower := strings.ToLower(skill.Name)
	titleLower := strings.ToLower(skill.Title)
	for _, token := range queryTokens {
		if strings.Contains(nameLower, token) {
			score += 0.5
		}
		if strings.Contains(titleLower, token) {
			score += 0.3
		}
	}

	return score
}

// tokenize splits text into lowercase tokens, filtering out short words and stop words.
func tokenize(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '-' || r == '_' || r == '/' || r == '.'
	})

	stopWords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "for": true, "from": true, "has": true, "he": true,
		"in": true, "is": true, "it": true, "its": true, "of": true, "on": true,
		"that": true, "the": true, "to": true, "was": true, "will": true, "with": true,
	}

	tokens := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) > 2 && !stopWords[w] {
			tokens = append(tokens, w)
		}
	}
	return tokens
}

// fmtDurationMS formats elapsed time since start as a millisecond string.
func fmtDurationMS(start time.Time) string {
	return fmt.Sprintf("%.2f", time.Since(start).Seconds()*1000)
}

// recordLoadMetric records a tracing span attribute and metric for a Load call.
// List returns all skills in the library cache.
// If the cache is empty, it triggers indexing first.
func (l *Library) List() []*Skill {
	return l.allSkills()
}

func (l *Library) recordLoadMetric(span *observability.Span, startTime time.Time, source string, hit bool) {
	if span != nil {
		span.SetAttribute("source", source)
		span.SetAttribute("duration_ms", fmtDurationMS(startTime))
	}
	cacheHit := "false"
	if hit && source == "cache" {
		cacheHit = "true"
	}
	l.tracer.RecordMetric("skills.library.load", 1.0, map[string]string{
		"cache_hit": cacheHit,
		"source":    source,
	})
}

// isNotExistWrapped checks whether an error wraps an os.ErrNotExist.
func isNotExistWrapped(err error) bool {
	return os.IsNotExist(err) || strings.Contains(err.Error(), "no such file")
}
