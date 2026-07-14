// Package axon computes an "Axon Pathway": a curated, dependency-ordered reading
// tour of a repo for onboarding a junior engineer. It selects the most
// instructive files (foundations, entry points, high-fan-in modules), orders
// them dependencies-first, tags each with a role + key symbols, and (when an LLM
// is configured) writes a plain-English summary of what each file does and why
// it matters — plus an intro telling the reader where to start.
package axon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// maxSteps caps the tour so it stays a readable journey, not a file dump.
const maxSteps = 16

// Step is one stop on the reading tour.
type Step struct {
	Order      int      `json:"order"`
	File       string   `json:"file"`
	Label      string   `json:"label"`
	Role       string   `json:"role"`    // foundation | core | entry | consumer | docs
	Summary    string   `json:"summary"` // plain-English explanation of what to read & why
	Symbols    []string `json:"symbols"` // key exported symbols
	Imports    []string `json:"imports"`
	ImportedBy []string `json:"imported_by"`
}

// Pathway is the ordered onboarding tour for a repo.
type Pathway struct {
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Intro string `json:"intro"`
	Steps []Step `json:"steps"`
}

// Engine builds pathways from the store, optionally narrated by an LLM.
type Engine struct {
	Store    *store.Store
	Chat     llm.ChatClient // nil => deterministic summaries
	sumCache sync.Map       // (root\x00path) -> string, memoized per-file summaries
}

// Pathway builds the curated reading tour for a repo.
func (e *Engine) Pathway(ctx context.Context, root string) (*Pathway, error) {
	files, err := e.Store.FilesByRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	rels, err := e.Store.RelationshipsByRoot(ctx, root)
	if err != nil {
		return nil, err
	}

	fileset := make(map[string]bool, len(files))
	isDoc := make(map[string]bool, len(files))
	all := make([]string, 0, len(files))
	for _, f := range files {
		fileset[f.FilePath] = true
		isDoc[f.FilePath] = f.Language == "markdown"
		all = append(all, f.FilePath)
	}
	sort.Strings(all)

	deps := map[string]map[string]bool{}
	importedBy := map[string]map[string]bool{}
	exportsByFile := map[string][]string{}
	hasEndpoint := map[string]bool{}
	for _, f := range all {
		deps[f] = map[string]bool{}
		importedBy[f] = map[string]bool{}
	}
	for _, r := range rels {
		switch r.RelationshipType {
		case "imports":
			if ext, _ := r.Metadata["external"].(bool); ext {
				continue
			}
			if r.SourceSymbol == r.TargetSymbol || !fileset[r.SourceSymbol] || !fileset[r.TargetSymbol] {
				continue
			}
			deps[r.SourceSymbol][r.TargetSymbol] = true
			importedBy[r.TargetSymbol][r.SourceSymbol] = true
		case "exports":
			if fileset[r.SourceSymbol] {
				exportsByFile[r.SourceSymbol] = append(exportsByFile[r.SourceSymbol], r.TargetSymbol)
			}
		case "endpoint":
			if fileset[r.SourceSymbol] {
				hasEndpoint[r.SourceSymbol] = true
			}
		}
	}

	order := topoOrder(all, deps)

	// Importance score → curate the most instructive files.
	score := func(f string) int {
		s := len(importedBy[f])*3 + len(exportsByFile[f])
		if hasEndpoint[f] {
			s += 6
		}
		if len(deps[f]) == 0 && len(importedBy[f]) > 0 {
			s += 4 // pure foundation
		}
		if isDoc[f] {
			s += 2
		}
		return s
	}
	keep := map[string]bool{}
	if len(order) <= maxSteps {
		for _, f := range order {
			keep[f] = true
		}
	} else {
		ranked := append([]string{}, order...)
		sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })
		for _, f := range ranked[:maxSteps] {
			keep[f] = true
		}
	}

	// Steps in dependency order (foundations first), keeping only curated files.
	steps := []Step{}
	for _, f := range order {
		if !keep[f] {
			continue
		}
		syms := uniqueSorted(exportsByFile[f])
		if len(syms) > 8 {
			syms = syms[:8]
		}
		steps = append(steps, Step{
			Order:      len(steps) + 1,
			File:       f,
			Label:      filepath.Base(f),
			Role:       roleFor(isDoc[f], deps[f], importedBy[f], hasEndpoint[f]),
			Symbols:    syms,
			Imports:    sortedKeys(deps[f]),
			ImportedBy: sortedKeys(importedBy[f]),
		})
	}

	pw := &Pathway{Repo: root, Name: repoName(root), Steps: steps}
	e.narrate(ctx, pw)
	return pw, nil
}

// FileSummary returns a concise 2-3 sentence explanation of a single file's
// responsibility, grounded in its real signatures — for the canvas detail panel
// on node-click. Results are memoized per (root, path) for the process lifetime;
// it falls back to a deterministic line when no LLM is configured or the call
// fails.
func (e *Engine) FileSummary(ctx context.Context, root, path string) (string, error) {
	key := root + "\x00" + path
	if v, ok := e.sumCache.Load(key); ok {
		return v.(string), nil
	}
	rows, err := e.Store.FileFunctions(ctx, root, path)
	if err != nil {
		return "", err
	}
	summary := deterministicFileSummary(path, rows)
	if e.Chat != nil {
		if s := e.llmFileSummary(ctx, path, rows); s != "" {
			summary = s
		}
	}
	e.sumCache.Store(key, summary)
	return summary, nil
}

const fileSummarySystem = `You explain a single source file to a new teammate. You are given the file path and a few real signatures pulled from it.

Respond with ONE JSON object and nothing else:
{"summary": "2-3 plain-English sentences describing what this file actually implements: its responsibility and its single most important function or type, named specifically, plus what kind of code depends on it. Flowing prose — no markdown, no lists."}

Be concrete and name the real symbols. Avoid filler like "a core module", "handles the logic", or "an important file". If the signatures are sparse, infer from the path and names. Never invent behaviour that is not visible in the input. Output valid JSON only — no prose outside the object, no code fences.`

func (e *Engine) llmFileSummary(ctx context.Context, path string, rows []store.FunctionRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\n", path)
	if syms := symbolNames(rows); len(syms) > 0 {
		fmt.Fprintf(&b, "Symbols: %s\n", strings.Join(syms, ", "))
	}
	if ex := codeExcerpt(rows); ex != "" {
		fmt.Fprintf(&b, "Signatures:\n%s", ex)
	}
	raw, err := e.Chat.Complete(ctx, fileSummarySystem, b.String())
	if err != nil {
		return ""
	}
	// Preferred path: the model returns {"summary": "..."}.
	var parsed struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(extractJSON(raw)), &parsed) == nil {
		if s := strings.TrimSpace(llm.CleanMarkdown(parsed.Summary)); s != "" {
			return s
		}
	}
	// Fallback: plain prose, or a differently-shaped object to flatten.
	return cleanFileSummary(raw)
}

var summaryJSONValRe = regexp.MustCompile(`:\s*"((?:[^"\\]|\\.)*)"`)

// cleanFileSummary normalizes the model output to plain prose. Some models
// ignore the "no JSON" instruction and wrap the answer in an object; when that
// happens we flatten its string values back into a sentence, and discard it
// entirely (so the caller falls back to the deterministic line) if nothing
// salvageable remains.
func cleanFileSummary(raw string) string {
	s := strings.TrimSpace(llm.CleanMarkdown(strings.TrimSpace(raw)))
	if strings.HasPrefix(s, "{") {
		var parts []string
		for _, m := range summaryJSONValRe.FindAllStringSubmatch(s, -1) {
			var v string
			if json.Unmarshal([]byte(`"`+m[1]+`"`), &v) != nil {
				v = m[1]
			}
			if v = strings.TrimSpace(v); v != "" {
				parts = append(parts, v)
			}
		}
		flat := strings.Join(parts, " ")
		if len(flat) < 24 {
			return ""
		}
		return flat
	}
	return s
}

func deterministicFileSummary(path string, rows []store.FunctionRow) string {
	base := filepath.Base(path)
	syms := symbolNames(rows)
	if len(syms) == 0 {
		return fmt.Sprintf("%s exposes no functions or classes — likely configuration, type declarations, or a re-export/barrel file.", base)
	}
	if len(syms) > 5 {
		syms = syms[:5]
	}
	return fmt.Sprintf("%s defines %s.", base, strings.Join(syms, ", "))
}

// symbolNames returns the unique top-level symbol names of a file's chunks,
// collapsing "foo#part2" chunk-split markers.
func symbolNames(rows []store.FunctionRow) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range rows {
		base := r.Symbol
		if i := strings.IndexByte(base, '#'); i >= 0 {
			base = base[:i]
		}
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		out = append(out, base)
	}
	return out
}

func roleFor(isDoc bool, deps, importedBy map[string]bool, hasEndpoint bool) string {
	switch {
	case isDoc:
		return "docs"
	case hasEndpoint:
		return "entry"
	case len(deps) == 0 && len(importedBy) > 0:
		return "foundation"
	case len(importedBy) == 0 && len(deps) > 0:
		return "consumer"
	default:
		return "core"
	}
}

// narrate fills Intro + each Step.Summary, via the LLM when available.
func (e *Engine) narrate(ctx context.Context, pw *Pathway) {
	for i := range pw.Steps {
		pw.Steps[i].Summary = derivedSummary(pw.Steps[i])
	}
	pw.Intro = fmt.Sprintf("A %d-stop reading tour of %s, ordered so each file builds on the ones before it. Start at the top — the foundations — and work down to the entry points.", len(pw.Steps), pw.Name)

	if e.Chat == nil || len(pw.Steps) == 0 {
		return
	}

	// Pull a few real signatures per file so the LLM can write concrete,
	// file-specific summaries instead of generic role boilerplate.
	excerpts := make(map[string]string, len(pw.Steps))
	for _, s := range pw.Steps {
		rows, ferr := e.Store.FileFunctions(ctx, pw.Repo, s.File)
		if ferr != nil || len(rows) == 0 {
			continue
		}
		if ex := codeExcerpt(rows); ex != "" {
			excerpts[s.File] = ex
		}
	}

	raw, err := e.Chat.Complete(ctx, narrateSystem, buildNarratePrompt(pw, excerpts))
	if err != nil {
		return
	}
	var parsed struct {
		Intro string `json:"intro"`
		Files []struct {
			File    string `json:"file"`
			Summary string `json:"summary"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &parsed); err != nil {
		return
	}
	if strings.TrimSpace(parsed.Intro) != "" {
		pw.Intro = llm.CleanMarkdown(parsed.Intro)
	}
	byFile := map[string]string{}
	for _, f := range parsed.Files {
		if strings.TrimSpace(f.Summary) != "" {
			byFile[f.File] = llm.CleanMarkdown(f.Summary)
		}
	}
	for i := range pw.Steps {
		if s, ok := byFile[pw.Steps[i].File]; ok {
			pw.Steps[i].Summary = s
		}
	}
}

const narrateSystem = `You are a staff engineer giving a new teammate a guided tour of a real codebase. You receive a dependency-ordered list of the most important files; for each you get its role, exported symbols, the internal files it imports, how many files depend on it, and a few real function/type signatures pulled from the source.

Respond with ONE JSON object, nothing else:
{
  "intro": "3-4 sentences orienting the reader: what this system actually DOES (infer it from the files and signatures), the shape of its architecture (the main layers/subsystems and how they connect), and a concrete strategy for reading this tour.",
  "files": [ { "file": "exact/path", "summary": "2-3 sentences, SPECIFIC to this file: what it actually implements (its real responsibility), the single most important function or type and what that does, and how it connects to the files immediately around it in the flow." } ]
}

Be concrete and technical — name the actual functions, types, and data flow visible in the signatures. NEVER use filler like "a core module", "wires things together", "an important file", or "handles the logic"; if you cannot say something specific, describe what the signatures imply. Ground every claim ONLY in the provided paths, symbols, and signatures — never invent files, routes, or behaviour. Output valid JSON only — no prose, no code fences.`

func buildNarratePrompt(pw *Pathway, excerpts map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n\nThe reading tour is dependency-ordered (foundations first). For each file: role, exported symbols, the internal files it reads, its fan-in, and real signatures from the source.\n\n", pw.Name)
	for _, s := range pw.Steps {
		syms := "—"
		if len(s.Symbols) > 0 {
			syms = strings.Join(s.Symbols, ", ")
		}
		reads := "—"
		if len(s.Imports) > 0 {
			reads = strings.Join(baseNames(s.Imports), ", ")
		}
		fmt.Fprintf(&b, "%d. %s\n   role: %s | exports: %s | reads: %s | used by %d file(s)\n",
			s.Order, s.File, s.Role, syms, reads, len(s.ImportedBy))
		if ex := excerpts[s.File]; ex != "" {
			fmt.Fprintf(&b, "   signatures:\n%s", ex)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// codeExcerpt returns up to four key signatures from a file's chunks, compactly,
// so the LLM's summary is grounded in the actual code, not just symbol names.
func codeExcerpt(rows []store.FunctionRow) string {
	var b strings.Builder
	seen := map[string]bool{}
	n := 0
	for _, r := range rows {
		base := r.Symbol
		if i := strings.IndexByte(base, '#'); i >= 0 {
			base = base[:i] // collapse "foo#part2" chunk splits
		}
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		sig := firstCodeLine(r.Code)
		if sig == "" {
			continue
		}
		if len(sig) > 140 {
			sig = sig[:140] + "…"
		}
		fmt.Fprintf(&b, "      %s\n", sig)
		if n++; n >= 4 {
			break
		}
	}
	return b.String()
}

// firstCodeLine returns the first non-blank, non-comment line of a code chunk
// (its declaration/signature).
func firstCodeLine(code string) string {
	for _, ln := range strings.Split(code, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
			continue
		}
		return t
	}
	return ""
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func derivedSummary(s Step) string {
	syms := ""
	if len(s.Symbols) > 0 {
		show := s.Symbols
		if len(show) > 4 {
			show = show[:4]
		}
		syms = " Exports " + strings.Join(show, ", ") + "."
	}
	switch s.Role {
	case "foundation":
		return "A foundational module the rest of the code builds on — a good first read." + syms
	case "entry":
		return "An entry point / route handler — where requests enter and execution begins." + syms
	case "consumer":
		return "A high-level module that wires the lower-level pieces together." + syms
	case "docs":
		return "Project documentation — read this for context before the code."
	default:
		return "A core module in the dependency graph." + syms
	}
}

// topoOrder returns files dependencies-first (Kahn-style; cycles broken by
// fewest-unplaced-deps, ties by path).
func topoOrder(all []string, deps map[string]map[string]bool) []string {
	placed := make(map[string]bool, len(all))
	order := make([]string, 0, len(all))
	for len(order) < len(all) {
		var ready []string
		for _, f := range all {
			if placed[f] {
				continue
			}
			ok := true
			for d := range deps[f] {
				if !placed[d] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, f)
			}
		}
		if len(ready) == 0 {
			best, bestCount := "", 1<<30
			for _, f := range all {
				if placed[f] {
					continue
				}
				c := 0
				for d := range deps[f] {
					if !placed[d] {
						c++
					}
				}
				if c < bestCount || (c == bestCount && f < best) {
					bestCount, best = c, f
				}
			}
			if best == "" {
				break
			}
			ready = []string{best}
		}
		sort.Strings(ready)
		for _, f := range ready {
			if !placed[f] {
				placed[f] = true
				order = append(order, f)
			}
		}
	}
	return order
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func repoName(root string) string {
	r := strings.TrimRight(root, `/\`)
	if i := strings.LastIndexAny(r, `/\`); i >= 0 {
		return r[i+1:]
	}
	return r
}

func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
