package blueprint

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"project-synapse/backend/internal/embed"
	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// Blueprint modes: Validate answers "should we build this, and how does it
// impact the product?"; Roadmap answers "what do we build, reuse, and where?".
const (
	ModeRoadmap  = "roadmap"
	ModeValidate = "validate"
)

// normalizeMode defaults anything unrecognised to roadmap.
func normalizeMode(m string) string {
	if strings.EqualFold(strings.TrimSpace(m), ModeValidate) {
		return ModeValidate
	}
	return ModeRoadmap
}

// Engine runs feature discovery: extract intents, score each against the
// codebase concurrently, and assemble the blueprint.
type Engine struct {
	Store       *store.Store
	Embedder    embed.Embedder
	Extractor   *Extractor
	Concurrency int // bounded to stay within the DB connection pool
	TopK        int
}

type target struct {
	kind string // entity | action
	name string
}

// Discover produces the reuse blueprint for a feature description, scoped to one
// repo (root == "" scores against every ingested repo).
func (e *Engine) Discover(ctx context.Context, description, root string) (*Response, error) {
	intents := e.Extractor.Extract(ctx, description)

	var targets []target
	for _, en := range intents.Entities {
		targets = append(targets, target{"entity", en.Name})
	}
	for _, ac := range intents.Actions {
		targets = append(targets, target{"action", ac.Name})
	}

	matches := make([]Match, len(targets))

	concurrency := e.Concurrency
	if concurrency <= 0 {
		concurrency = 6
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			matches[i] = e.score(ctx, targets[i], root)
		}(i)
	}
	wg.Wait()

	return assemble(description, intents, matches), nil
}

const roadmapSystem = `You are a staff engineer writing an implementation blueprint for a proposed feature in THIS codebase. You are given a code-reuse analysis (what already EXISTS to reuse, what to EXTEND, and the GAPS to build) with the real files involved, plus a sample of the repository's directory layout.

Write an actionable blueprint in GitHub-flavored markdown using these sections (skip a section only if it is truly empty):
### Reuse
Existing files/symbols to build on — name the real paths.
### Extend
Which existing files to modify, and what to add to each.
### Build new
The new files/folders to create and WHERE they belong — mirror the repository's existing folder conventions from the layout sample.
### Steps
A short ordered build plan (3-6 concrete steps).

Be concrete and reference real file paths. Keep it tight and skimmable. Output only the markdown — no preamble, no closing remarks, and do NOT wrap your whole answer in a code fence.`

const validateSystem = `You are a senior product engineer advising whether a proposed feature is worth building for THIS codebase, grounded in a code-reuse analysis (how much already exists to reuse vs. must be built new).

Write a decisive validation in GitHub-flavored markdown using these sections:
### Verdict
One line — **Build**, **Build later**, or **Reconsider** — plus a one-sentence why.
### Why it fits
How much leverages existing code (name the reusable pieces); high reuse means lower cost & risk. Say plainly if it is mostly net-new.
### Product impact
What this enables for users and the product, and who benefits.
### Effort & risks
Rough build effort (from the extend/build counts) and the main risks or dependencies to watch.

Be concrete and honest — recommend against low-value or disproportionately costly features. Keep it around 150-200 words. Output only the markdown, and do NOT wrap your whole answer in a code fence.`

// StreamNarrative streams a natural-language briefing for an assembled blueprint
// via onToken, framed by mode: "roadmap" (implementation plan) or "validate"
// (build/impact recommendation). Uses the LLM when configured (token-by-token
// when supported) and falls back to a deterministic summary.
func (e *Engine) StreamNarrative(ctx context.Context, resp *Response, mode, root string, onToken func(string)) error {
	mode = normalizeMode(mode)
	chat := e.chat()
	if chat == nil {
		onToken(deterministicNarrative(resp, mode))
		return nil
	}
	system := roadmapSystem
	layout := ""
	if mode == ModeValidate {
		system = validateSystem
	} else {
		layout = e.repoLayout(ctx, root)
	}
	prompt := buildModePrompt(resp, mode, layout)
	if sc, ok := chat.(llm.StreamingChatClient); ok {
		_, err := sc.Stream(ctx, system, prompt, onToken)
		return err
	}
	raw, err := chat.Complete(ctx, system, prompt)
	if err != nil {
		return err
	}
	onToken(raw)
	return nil
}

// repoLayout returns a compact sample of the repo's most-populated directories,
// so a roadmap targets real folders and follows existing conventions.
func (e *Engine) repoLayout(ctx context.Context, root string) string {
	if e.Store == nil || strings.TrimSpace(root) == "" {
		return ""
	}
	files, err := e.Store.FilesByRoot(ctx, root)
	if err != nil || len(files) == 0 {
		return ""
	}
	count := map[string]int{}
	for _, f := range files {
		if d := twoLevelDir(f.FilePath); d != "" {
			count[d]++
		}
	}
	dirs := make([]string, 0, len(count))
	for d := range count {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if count[dirs[i]] != count[dirs[j]] {
			return count[dirs[i]] > count[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	if len(dirs) > 14 {
		dirs = dirs[:14]
	}
	var b strings.Builder
	for _, d := range dirs {
		fmt.Fprintf(&b, "  %s/ (%d files)\n", d, count[d])
	}
	return b.String()
}

// twoLevelDir returns the first two path segments of a file's directory.
func twoLevelDir(p string) string {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	if len(parts) <= 1 {
		return ""
	}
	parts = parts[:len(parts)-1] // drop the filename
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

func (e *Engine) chat() llm.ChatClient {
	if e.Extractor == nil {
		return nil
	}
	return e.Extractor.Chat
}

func buildModePrompt(resp *Response, mode, layout string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Feature: %s\n\n", resp.Description)
	fmt.Fprintf(&b, "Reuse score: %.0f%% (%d reuse / %d extend / %d build of %d intents)\n\n",
		resp.Summary.ReuseScore*100, resp.Summary.Green, resp.Summary.Yellow, resp.Summary.Red, resp.Summary.Total)

	byCat := func(label string, cat Category) {
		var lines []string
		for _, m := range resp.Matches {
			if m.Category != cat {
				continue
			}
			files := "no direct file"
			if len(m.Files) > 0 {
				files = strings.Join(m.Files, ", ")
			}
			lines = append(lines, fmt.Sprintf("  - %s (%s) → %s", m.Name, m.Kind, files))
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "%s:\n%s\n", label, strings.Join(lines, "\n"))
		}
	}
	byCat("REUSE (already exists)", CategoryGreen)
	byCat("EXTEND (partial coverage)", CategoryYellow)
	byCat("BUILD (missing)", CategoryRed)

	if len(resp.Gaps) > 0 {
		var g []string
		for _, gap := range resp.Gaps {
			g = append(g, fmt.Sprintf("  - %s (%s)", gap.Label, gap.Kind))
		}
		fmt.Fprintf(&b, "\nGaps to build:\n%s\n", strings.Join(g, "\n"))
	}
	if mode == ModeRoadmap && strings.TrimSpace(layout) != "" {
		fmt.Fprintf(&b, "\nRepository layout (most-populated directories — follow these conventions when placing new files):\n%s", layout)
	}
	return b.String()
}

// deterministicNarrative is the offline briefing used when no LLM is configured.
func deterministicNarrative(resp *Response, mode string) string {
	var b strings.Builder
	g, y, r := resp.Summary.Green, resp.Summary.Yellow, resp.Summary.Red
	if g == 0 && y == 0 && r == 0 {
		return "No intents were extracted from the description. Try describing the feature in more detail."
	}

	if mode == ModeValidate {
		verdict := "Build later"
		switch {
		case resp.Summary.ReuseScore >= 0.6:
			verdict = "Build"
		case r > g+y:
			verdict = "Reconsider"
		}
		fmt.Fprintf(&b, "### Verdict\n**%s** — %.0f%% of this feature is already covered by existing code.\n\n", verdict, resp.Summary.ReuseScore*100)
		fmt.Fprintf(&b, "### Why\n- **Reuse (%d)** / **Extend (%d)** / **Build new (%d)** across %d intents.\n", g, y, r, resp.Summary.Total)
		b.WriteString("\n_(Offline summary — configure an LLM key for a full validation.)_")
		return b.String()
	}

	fmt.Fprintf(&b, "### Blueprint — %.0f%% reuse across %d intents\n\n", resp.Summary.ReuseScore*100, resp.Summary.Total)
	if g > 0 {
		fmt.Fprintf(&b, "- **Reuse (%d):** existing structures already cover these capabilities.\n", g)
	}
	if y > 0 {
		fmt.Fprintf(&b, "- **Extend (%d):** partial coverage exists — extend the highlighted files.\n", y)
	}
	if r > 0 {
		fmt.Fprintf(&b, "- **Build new (%d):** no existing structure — create new files.\n", r)
	}
	b.WriteString("\n_(Offline summary — configure an LLM key for a full blueprint.)_")
	return b.String()
}

// score runs concurrent-by-caller semantic + relational search for one intent
// and categorises it, scoped to one repo (root == "" = all repos).
func (e *Engine) score(ctx context.Context, t target, root string) Match {
	variants := termVariants(t.name)
	patterns := make([]string, 0, len(variants))
	for _, v := range variants {
		patterns = append(patterns, "%"+v+"%")
	}

	// Semantic search.
	var bestSim float64
	if vecs, err := e.Embedder.Embed(ctx, []string{t.name}); err == nil && len(vecs) > 0 {
		if hits, err := e.Store.VectorSearch(ctx, vecs[0], e.TopK, root); err == nil && len(hits) > 0 {
			bestSim = 1 - hits[0].Distance
		}
	}

	// Relational search.
	files, _ := e.Store.KeywordSearchFiles(ctx, patterns, root)
	rels, _ := e.Store.KeywordSearchRelationships(ctx, patterns, root)

	containsVariant := func(s string) bool {
		ls := strings.ToLower(s)
		for _, v := range variants {
			if strings.Contains(ls, v) {
				return true
			}
		}
		return false
	}

	// Split evidence into "structural" (a dedicated symbol/endpoint/filename
	// matches the term) vs "mention" (the term only appears in file content or
	// is merely imported/used). Green highlights use the structural set so a
	// broad content term doesn't paint the whole graph green.
	structuralSet := map[string]bool{}
	mentionSet := map[string]bool{}
	var structural, mention, evSymbols, evEndpoints []string
	exportedSymbols := []string{}
	endpointRoutes := []string{}

	addStructural := func(p string) {
		if p != "" && !structuralSet[p] {
			structuralSet[p] = true
			structural = append(structural, p)
		}
	}
	addMention := func(p string) {
		if p != "" && !mentionSet[p] {
			mentionSet[p] = true
			mention = append(mention, p)
		}
	}

	symSeen := map[string]bool{}
	for _, r := range rels {
		switch r.RelationshipType {
		case "exports":
			exportedSymbols = append(exportedSymbols, r.TargetSymbol)
			if containsVariant(r.TargetSymbol) {
				addStructural(r.SourceSymbol)
				if !symSeen[r.TargetSymbol] {
					symSeen[r.TargetSymbol] = true
					evSymbols = append(evSymbols, r.TargetSymbol)
				}
			}
		case "endpoint":
			endpointRoutes = append(endpointRoutes, r.TargetSymbol)
			if containsVariant(r.TargetSymbol) {
				addStructural(r.SourceSymbol)
				evEndpoints = append(evEndpoints, r.TargetSymbol)
			}
		case "imports":
			addMention(r.SourceSymbol) // a usage edge, not a definition
		}
	}
	for _, f := range files {
		if containsVariant(f.Filename) || containsVariant(f.FilePath) {
			addStructural(f.FilePath)
		} else {
			addMention(f.FilePath) // matched via raw content only
		}
	}

	// Combined evidence (structural first), with mentions not already structural.
	evFiles := append([]string{}, structural...)
	for _, m := range mention {
		if !structuralSet[m] {
			evFiles = append(evFiles, m)
		}
	}

	green := isGreen(variants, exportedSymbols, files, endpointRoutes)
	hasSignal := len(files) > 0 || len(rels) > 0 || bestSim >= 0.25

	var category Category
	var confidence float64
	switch {
	case green:
		category = CategoryGreen
		confidence = clamp(0.85+bestSim*0.2, 0.85, 0.99)
	case hasSignal:
		category = CategoryYellow
		strength := float64(len(files)+len(rels)) / 3.0
		confidence = clamp(0.40+0.30*minF(1, strength)+0.14*bestSim, 0.40, 0.84)
	default:
		category = CategoryRed
		confidence = minF(0.39, bestSim)
	}

	return Match{
		Kind:           t.kind,
		Name:           t.name,
		Category:       category,
		Confidence:     round2(confidence),
		Files:          cap6(evFiles),
		Symbols:        cap6(evSymbols),
		Endpoints:      cap6(evEndpoints),
		Recommendation: recommend(category, t.name),
		structural:     cap6(structural),
	}
}

// isGreen reports an exact/dedicated structure: an exported symbol whose name
// is the term (+ a common suffix), a filename headed by the term, or an
// endpoint route segment equal to the term.
func isGreen(variants []string, exportedSymbols []string, files []store.FileRow, endpointRoutes []string) bool {
	forms := greenForms(variants)

	for _, s := range exportedSymbols {
		if forms[strings.ToLower(s)] {
			return true
		}
	}
	for _, f := range files {
		base := strings.ToLower(strings.TrimSuffix(f.Filename, ext(f.Filename)))
		if forms[base] {
			return true
		}
	}
	for _, route := range endpointRoutes {
		// route looks like "GET /category"
		for _, seg := range strings.Split(route, "/") {
			seg = strings.ToLower(strings.TrimSpace(seg))
			for _, v := range variants {
				if seg == v {
					return true
				}
			}
		}
	}
	return false
}

var commonSuffixes = []string{"", "s", "controller", "service", "model", "repository", "handler", "schema", "table", "router", "routes"}

// greenForms is the set of concatenated lowercase names that count as a
// dedicated structure for the term variants.
func greenForms(variants []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range variants {
		for _, suf := range commonSuffixes {
			out[v+suf] = true
		}
	}
	return out
}

// --- assembly ---------------------------------------------------------------

func assemble(description string, intents IntentBreakdown, matches []Match) *Response {
	resp := &Response{
		Description: description,
		Intents:     intents,
		Matches:     matches,
		Highlights:  Highlights{Green: []string{}, Yellow: []string{}},
		Gaps:        []GapNode{},
		GapEdges:    []GapEdge{},
		DiffSummary: []DiffItem{},
	}

	greenSet := map[string]bool{}
	yellowSet := map[string]bool{}
	fileFreq := map[string]int{}
	diffSeen := map[string]bool{}

	for _, m := range matches {
		switch m.Category {
		case CategoryGreen:
			resp.Summary.Green++
			greenFiles := m.structural
			if len(greenFiles) == 0 {
				greenFiles = m.Files
			}
			for _, f := range greenFiles {
				greenSet[f] = true
				fileFreq[f]++
			}
		case CategoryYellow:
			resp.Summary.Yellow++
			for _, f := range m.Files {
				yellowSet[f] = true
				fileFreq[f]++
				key := f + "|" + m.Name
				if !diffSeen[key] {
					diffSeen[key] = true
					resp.DiffSummary = append(resp.DiffSummary, DiffItem{
						File:       f,
						ChangeType: "extend",
						Category:   CategoryYellow,
						Detail:     fmt.Sprintf("Extend %s to support %q (%s).", f, m.Name, m.Kind),
					})
				}
			}
		case CategoryRed:
			resp.Summary.Red++
			gapID := "gap:" + slug(m.Name)
			suggested := suggestedFile(m.Kind, m.Name)
			resp.Gaps = append(resp.Gaps, GapNode{
				ID:            gapID,
				Label:         m.Name,
				Kind:          m.Kind,
				Reason:        fmt.Sprintf("No existing structure covers this %s.", m.Kind),
				SuggestedFile: suggested,
			})
			resp.DiffSummary = append(resp.DiffSummary, DiffItem{
				File:       suggested,
				ChangeType: "create",
				Category:   CategoryRed,
				Detail:     fmt.Sprintf("Create new %s structure for %q.", m.Kind, m.Name),
			})
		}
	}

	for f := range greenSet {
		resp.Highlights.Green = append(resp.Highlights.Green, nodeID(f))
	}
	for f := range yellowSet {
		if greenSet[f] {
			continue // green takes precedence
		}
		resp.Highlights.Yellow = append(resp.Highlights.Yellow, nodeID(f))
	}

	// Anchor gaps to the most-referenced existing file (where they'd wire in).
	anchor := ""
	best := 0
	for f, n := range fileFreq {
		if n > best {
			best = n
			anchor = f
		}
	}
	if anchor != "" {
		for _, g := range resp.Gaps {
			resp.GapEdges = append(resp.GapEdges, GapEdge{Source: g.ID, Target: nodeID(anchor)})
		}
	}

	resp.Summary.Total = len(matches)
	if resp.Summary.Total > 0 {
		resp.Summary.ReuseScore = round2(
			(float64(resp.Summary.Green) + 0.5*float64(resp.Summary.Yellow)) / float64(resp.Summary.Total),
		)
	}
	return resp
}

// --- helpers ----------------------------------------------------------------

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

func nodeID(filePath string) string { return "file:" + filePath }

func termVariants(name string) []string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "_", "")
	set := map[string]bool{n: true}
	switch {
	case strings.HasSuffix(n, "ies") && len(n) > 4:
		set[n[:len(n)-3]+"y"] = true
	case strings.HasSuffix(n, "s") && len(n) > 3:
		set[n[:len(n)-1]] = true
	default:
		set[n+"s"] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func recommend(c Category, name string) string {
	switch c {
	case CategoryGreen:
		return fmt.Sprintf("Reuse the existing implementation for %q.", name)
	case CategoryYellow:
		return fmt.Sprintf("Extend existing structures to cover %q.", name)
	default:
		return fmt.Sprintf("Build new — nothing in the codebase covers %q.", name)
	}
}

func suggestedFile(kind, name string) string {
	s := slug(name)
	s = strings.ReplaceAll(s, "-", "")
	if kind == "action" {
		return s + "Handler.ts"
	}
	return s + ".ts"
}

func slug(name string) string {
	s := nonAlnumRe.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

func ext(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

func cap6(s []string) []string {
	if len(s) > 6 {
		return s[:6]
	}
	return s
}

func clamp(v, lo, hi float64) float64 { return maxF(lo, minF(hi, v)) }
func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
