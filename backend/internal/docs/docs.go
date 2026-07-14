// Package docs generates hybrid repository documentation: LLM-written narrative
// sections (Introduction, Architecture) plus auto-derived module/function
// reference sections built straight from the parsed AST. Results are cached per
// repo root until a refresh is requested.
package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// Section is one documentation page — narrative markdown, grouped in the sidebar.
type Section struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Group   string `json:"group"` // sidebar grouping
	Content string `json:"content"`
}

// Docs is the full generated documentation for one repo.
type Docs struct {
	Repo     string    `json:"repo"` // root_path
	Name     string    `json:"name"`
	Sections []Section `json:"sections"`
}

// Engine builds documentation from the store + an (optional) LLM.
type Engine struct {
	Store *store.Store
	Chat  llm.ChatClient // nil => deterministic fallback narrative

	mu    sync.Mutex
	cache map[string]*Docs
}

// narrativeDoc is the structured set of LLM-written documentation pages.
type narrativeDoc struct {
	Introduction string `json:"introduction"`
	Architecture string `json:"architecture"`
	Concepts     string `json:"concepts"`
	DataFlow     string `json:"data_flow"`
}

const narrativeSystem = `You are a staff engineer writing the OFFICIAL documentation site for a codebase, read by engineers who will work in it. You receive a structured summary: the file tree, a USAGE-RANKED dependency list, exported symbols per file, and HTTP endpoints. Document grounded ONLY in that summary — never invent files, routes, commands, or features.

RELEVANCE IS CRITICAL. Describe only what is actually used and central to the running system. The dependency list is ranked by how many source files import each library. A library marked "likely legacy / not part of the live stack" or imported by only a single file is very probably dead or vestigial — do NOT present it as part of the tech stack or architecture. When two or more libraries fill the SAME role (e.g. two state managers, two routers, two HTTP clients), identify and document ONLY the dominant one that is genuinely wired in across the codebase; ignore the unused alternative entirely.

Respond with ONE JSON object, nothing else:
{
  "introduction": "markdown",
  "architecture": "markdown",
  "concepts": "markdown",
  "data_flow": "markdown"
}

Write like a high-quality technology documentation site: precise, concrete, and THOROUGH. These are the flagship overview pages — favour completeness and depth over brevity, while staying strictly grounded in the summary. Per section:
- introduction: a full narrative of what this project IS, the problem it solves, and who it is for (2-4 paragraphs), then a "## Capabilities" bullet list covering every major capability, then a "## Tech Stack" section. The tech stack MUST list ONLY libraries that actually appear in the ranked dependency list above (plus the implementation language/runtime), and should explain what each key library is used for. NEVER add a framework or library by inference — do not guess "Express", "GraphQL", "Redux" etc. just because APIs or state exist. Omit any dependency flagged legacy/unused.
- architecture: a detailed tour of the layers/subsystems and how they fit together. Open with a compact markdown table (columns Layer | Location | Responsibility) covering every subsystem, then a "##" subsection per subsystem that names the REAL folders/files in ` + "`code`" + ` spans, states its responsibility, its key components, and which other subsystems it depends on and is used by. Be comprehensive.
- concepts: the 5-10 most important domain concepts, types, or abstractions a contributor must understand. Each as "### Name" + a precise 2-4 sentence definition grounded in the actual symbols, noting where it lives and how it is used.
- data_flow: trace 2-3 REAL end-to-end paths through the system (e.g. an HTTP request from entry point to data layer, the ingestion pipeline, a background job) as numbered lists, naming the actual files/functions at each hop and what happens there.

Reference real paths and symbols in ` + "`code`" + ` spans. Be specific and detailed — never use filler. If a section genuinely cannot be grounded in the summary, return an empty string for it. Output valid JSON only — no prose, no code fences around the JSON.`

// Generate returns the documentation for one repo root, building (and caching)
// it on first request. refresh forces regeneration.
func (e *Engine) Generate(ctx context.Context, root string, refresh bool) (*Docs, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repo is required")
	}

	e.mu.Lock()
	if e.cache == nil {
		e.cache = map[string]*Docs{}
	}
	if !refresh {
		if d, ok := e.cache[root]; ok {
			e.mu.Unlock()
			return d, nil
		}
	}
	e.mu.Unlock()

	files, err := e.Store.FilesByRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found for repo")
	}
	rels, _ := e.Store.RelationshipsByRoot(ctx, root)
	funcs, _ := e.Store.FunctionsWithCodeByRoot(ctx, root)
	name := repoName(root)

	// Overview narrative — the high-level pages.
	nd := e.narrative(ctx, name, files, rels)
	sections := []Section{
		{ID: "introduction", Title: "Introduction", Group: "Overview", Content: nd.Introduction},
		{ID: "architecture", Title: "Architecture", Group: "Overview", Content: nd.Architecture},
	}
	if strings.TrimSpace(nd.Concepts) != "" {
		sections = append(sections, Section{ID: "concepts", Title: "Core Concepts", Group: "Overview", Content: nd.Concepts})
	}
	if strings.TrimSpace(nd.DataFlow) != "" {
		sections = append(sections, Section{ID: "data-flow", Title: "Data Flow", Group: "Overview", Content: nd.DataFlow})
	}

	// Elaborate, code-grounded deep-dive page per module/subsystem.
	sections = append(sections, e.subsystems(ctx, name, files, rels, funcs)...)

	d := &Docs{Repo: root, Name: name, Sections: sections}
	e.mu.Lock()
	e.cache[root] = d
	e.mu.Unlock()
	return d, nil
}

func (e *Engine) narrative(ctx context.Context, name string, files []store.FileRow, rels []store.RelRow) narrativeDoc {
	fb := narrativeDoc{Introduction: fallbackIntro(name, files), Architecture: fallbackArch(name, files, rels)}
	if e.Chat == nil {
		return fb
	}
	raw, err := e.Chat.Complete(ctx, narrativeSystem, buildSummary(name, files, rels))
	if err != nil {
		return fb
	}
	var parsed narrativeDoc
	if err := json.Unmarshal([]byte(extractJSON(raw)), &parsed); err != nil || strings.TrimSpace(parsed.Introduction) == "" {
		return fb
	}
	// Repair models that double-escape newlines inside the JSON strings.
	parsed.Introduction = llm.CleanMarkdown(parsed.Introduction)
	parsed.Architecture = llm.CleanMarkdown(parsed.Architecture)
	parsed.Concepts = llm.CleanMarkdown(parsed.Concepts)
	parsed.DataFlow = llm.CleanMarkdown(parsed.DataFlow)
	return parsed
}

// buildSummary renders a compact, bounded summary of the repo for the LLM.
func buildSummary(name string, files []store.FileRow, rels []store.RelRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n\nFiles (%d):\n", name, len(files))
	for i, f := range files {
		if i >= 120 {
			fmt.Fprintf(&b, "- …and %d more\n", len(files)-i)
			break
		}
		fmt.Fprintf(&b, "- %s\n", f.FilePath)
	}

	endpoints := []string{}
	exportsBySrc := map[string][]string{}
	depFiles := map[string]map[string]bool{} // external dep -> set of importing files
	internalAdj := map[string][]string{}      // file -> internal files it imports
	hasEndpoint := map[string]bool{}
	for _, r := range rels {
		switch r.RelationshipType {
		case "endpoint":
			endpoints = append(endpoints, fmt.Sprintf("%s  (in %s)", r.TargetSymbol, r.SourceSymbol))
			hasEndpoint[r.SourceSymbol] = true
		case "exports":
			exportsBySrc[r.SourceSymbol] = append(exportsBySrc[r.SourceSymbol], r.TargetSymbol)
		case "imports":
			if ext, _ := r.Metadata["external"].(bool); ext {
				if spec, _ := r.Metadata["specifier"].(string); spec != "" {
					key := depKey(spec)
					if depFiles[key] == nil {
						depFiles[key] = map[string]bool{}
					}
					depFiles[key][r.SourceSymbol] = true
				}
			} else {
				internalAdj[r.SourceSymbol] = append(internalAdj[r.SourceSymbol], r.TargetSymbol)
			}
		}
	}

	// Reachability: a dependency imported only by dead (unreachable) files is
	// almost certainly legacy/unused, so rank deps by how many *reachable* files
	// import each. BFS from entry points (route handlers + entry-like filenames).
	reachable := reachableFiles(files, internalAdj, hasEndpoint)
	if len(depFiles) > 0 {
		type dep struct {
			name      string
			liveCount int
			total     int
		}
		deps := make([]dep, 0, len(depFiles))
		for k, fs := range depFiles {
			d := dep{name: k, total: len(fs)}
			for f := range fs {
				if reachable == nil || reachable[f] { // nil = couldn't determine → count all
					d.liveCount++
				}
			}
			deps = append(deps, d)
		}
		sort.Slice(deps, func(i, j int) bool {
			if deps[i].liveCount != deps[j].liveCount {
				return deps[i].liveCount > deps[j].liveCount
			}
			if deps[i].total != deps[j].total {
				return deps[i].total > deps[j].total
			}
			return deps[i].name < deps[j].name
		})
		b.WriteString("\nDependency usage — ranked by how many source files import each (higher = more central to the live stack):\n")
		for i, d := range deps {
			if i >= 50 {
				break
			}
			switch {
			case d.liveCount == 0:
				fmt.Fprintf(&b, "- %s (%d file(s), all in unused/unreachable code — likely legacy, NOT part of the live stack)\n", d.name, d.total)
			case d.liveCount == 1:
				fmt.Fprintf(&b, "- %s (1 file — limited use)\n", d.name)
			default:
				fmt.Fprintf(&b, "- %s (%d files)\n", d.name, d.liveCount)
			}
		}
	}
	if len(endpoints) > 0 {
		b.WriteString("\nHTTP endpoints:\n")
		for i, ep := range endpoints {
			if i >= 50 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", ep)
		}
	}
	if len(exportsBySrc) > 0 {
		b.WriteString("\nExported symbols by file:\n")
		srcs := make([]string, 0, len(exportsBySrc))
		for s := range exportsBySrc {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		for i, s := range srcs {
			if i >= 60 {
				break
			}
			syms := exportsBySrc[s]
			if len(syms) > 10 {
				syms = syms[:10]
			}
			fmt.Fprintf(&b, "- %s: %s\n", s, strings.Join(syms, ", "))
		}
	}
	return b.String()
}

// --- subsystem deep dives ---------------------------------------------------

const moduleSystem = `You are a staff engineer writing the deep-dive documentation page for ONE module (a directory) of a codebase, for engineers who will work in it. You receive the module's files, their exported symbols, key function signatures, and its internal dependencies — all extracted from the real parsed code.

Respond with ONE JSON object, nothing else: {"content": "<the full markdown page as a single string>"}.

The markdown page must be THOROUGH, accurate, and detailed, structured as:
- ## Purpose — what this module is responsible for and why it exists (1-2 paragraphs).
- ## Key components — the important files, types, and functions. Use "###" per notable file or abstraction, name real symbols in ` + "`code`" + ` spans, and explain what each does and how it works. Cover the significant exports — describe them, don't just list them.
- ## How it works — the main flows or algorithms inside the module, referencing real functions in order.
- ## Dependencies & integration — which other modules it imports and how it fits into the wider system.

Ground EVERYTHING in the provided symbols — never invent files, functions, or behaviour not implied by the names and signatures. Prefer precise technical prose over filler; be comprehensive. Do NOT include a top-level "#" title (the page title is added separately). Output valid JSON only — no prose, no code fences around the JSON.`

// subsystems generates a detailed, code-grounded deep-dive page per module
// (directory), concurrently. Returns nil without an LLM configured.
func (e *Engine) subsystems(ctx context.Context, name string, files []store.FileRow, rels []store.RelRow, funcs []store.FuncCodeRow) []Section {
	if e.Chat == nil {
		return nil
	}

	exportsBySrc := map[string][]string{}
	modImports := map[string]map[string]bool{} // dir -> internal dirs it imports
	for _, r := range rels {
		switch r.RelationshipType {
		case "exports":
			exportsBySrc[r.SourceSymbol] = append(exportsBySrc[r.SourceSymbol], r.TargetSymbol)
		case "imports":
			if ext, _ := r.Metadata["external"].(bool); !ext {
				sd, td := dirOf(r.SourceSymbol), dirOf(r.TargetSymbol)
				if sd != td && r.TargetSymbol != "" {
					if modImports[sd] == nil {
						modImports[sd] = map[string]bool{}
					}
					modImports[sd][td] = true
				}
			}
		}
	}
	funcsByFile := map[string][]store.FuncCodeRow{}
	for _, f := range funcs {
		funcsByFile[f.File] = append(funcsByFile[f.File], f)
	}
	filesByDir := map[string][]store.FileRow{}
	for _, f := range files {
		if f.Language == "markdown" {
			continue // markdown wikis aren't code modules
		}
		filesByDir[dirOf(f.FilePath)] = append(filesByDir[dirOf(f.FilePath)], f)
	}

	// Pick the most substantial modules (bounded for cost), then order by path.
	type dc struct {
		dir string
		n   int
	}
	dirs := make([]dc, 0, len(filesByDir))
	for d, fs := range filesByDir {
		dirs = append(dirs, dc{d, len(fs)})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].n != dirs[j].n {
			return dirs[i].n > dirs[j].n
		}
		return dirs[i].dir < dirs[j].dir
	})
	const maxModules = 16
	if len(dirs) > maxModules {
		dirs = dirs[:maxModules]
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].dir < dirs[j].dir })

	out := make([]Section, len(dirs))
	ok := make([]bool, len(dirs))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, d := range dirs {
		wg.Add(1)
		go func(i int, dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			payload := buildModuleContext(name, dir, filesByDir[dir], exportsBySrc, funcsByFile, modImports[dir])
			raw, err := e.Chat.Complete(ctx, moduleSystem, payload)
			if err != nil {
				return
			}
			var parsed struct {
				Content string `json:"content"`
			}
			if uerr := json.Unmarshal([]byte(extractJSON(raw)), &parsed); uerr != nil || strings.TrimSpace(parsed.Content) == "" {
				return
			}
			out[i] = Section{ID: "module-" + slug(dir), Title: dispDir(dir), Group: "Subsystems", Content: llm.CleanMarkdown(parsed.Content)}
			ok[i] = true
		}(i, d.dir)
	}
	wg.Wait()

	var sections []Section
	for i := range out {
		if ok[i] {
			sections = append(sections, out[i])
		}
	}
	return sections
}

// buildModuleContext renders one module's files, exports, signatures, and
// internal dependencies as a compact, bounded prompt payload.
func buildModuleContext(repo, dir string, files []store.FileRow, exportsBySrc map[string][]string, funcsByFile map[string][]store.FuncCodeRow, imports map[string]bool) string {
	var b strings.Builder
	lang := ""
	if len(files) > 0 {
		lang = files[0].Language
	}
	fmt.Fprintf(&b, "Repository: %s\nModule (directory): %s\nPrimary language: %s\n\nFiles in this module — with their exported symbols and key function signatures:\n", repo, dispDir(dir), lang)

	sort.Slice(files, func(i, j int) bool { return files[i].FilePath < files[j].FilePath })
	for fi, f := range files {
		if fi >= 40 {
			fmt.Fprintf(&b, "- …and %d more files\n", len(files)-fi)
			break
		}
		fmt.Fprintf(&b, "- %s\n", baseName(f.FilePath))
		if exps := exportsBySrc[f.FilePath]; len(exps) > 0 {
			if len(exps) > 16 {
				exps = exps[:16]
			}
			fmt.Fprintf(&b, "    exports: %s\n", strings.Join(exps, ", "))
		}
		if fns := funcsByFile[f.FilePath]; len(fns) > 0 {
			var sigs []string
			for _, fn := range fns {
				if s := firstSignature(fn.Code); s != "" {
					sigs = append(sigs, s)
				}
				if len(sigs) >= 10 {
					break
				}
			}
			if len(sigs) > 0 {
				fmt.Fprintf(&b, "    functions: %s\n", strings.Join(sigs, " | "))
			}
		}
	}
	if len(imports) > 0 {
		deps := make([]string, 0, len(imports))
		for d := range imports {
			deps = append(deps, dispDir(d))
		}
		sort.Strings(deps)
		if len(deps) > 20 {
			deps = deps[:20]
		}
		fmt.Fprintf(&b, "\nInternal modules this one imports: %s\n", strings.Join(deps, ", "))
	}
	return b.String()
}

func firstSignature(code string) string {
	for _, ln := range strings.Split(code, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "#") {
			continue
		}
		if len(t) > 140 {
			t = t[:140] + "…"
		}
		return t
	}
	return ""
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[:i]
	}
	return ""
}

func dispDir(d string) string {
	if d == "" {
		return "(root)"
	}
	return d
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func fallbackIntro(name string, files []store.FileRow) string {
	return fmt.Sprintf("# %s\n\nAuto-generated documentation for **%s** — %d source files. "+
		"Configure an LLM provider (Anthropic / OpenAI / OpenRouter / Ollama) to generate the full "+
		"written overview and per-module deep-dive documentation.", name, name, len(files))
}

func fallbackArch(name string, files []store.FileRow, rels []store.RelRow) string {
	byFolder := map[string]int{}
	for _, f := range files {
		byFolder[topFolder(f.FilePath)]++
	}
	folders := make([]string, 0, len(byFolder))
	for f := range byFolder {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	var b strings.Builder
	b.WriteString("## Structure\n\n")
	for _, f := range folders {
		label := f + "/"
		if f == rootFolder {
			label = "(root)"
		}
		fmt.Fprintf(&b, "- `%s` — %d files\n", label, byFolder[f])
	}
	var endpoints int
	for _, r := range rels {
		if r.RelationshipType == "endpoint" {
			endpoints++
		}
	}
	if endpoints > 0 {
		fmt.Fprintf(&b, "\nExposes **%d HTTP endpoint(s)**.\n", endpoints)
	}
	return b.String()
}

// reachableFiles returns the set of files reachable (over internal imports) from
// the repo's entry points — HTTP route handlers + entry-like filenames. Returns
// nil when no entry points can be identified, signalling "can't determine" so
// callers fall back to treating every file as live.
func reachableFiles(files []store.FileRow, adj map[string][]string, hasEndpoint map[string]bool) map[string]bool {
	reachable := map[string]bool{}
	var queue []string
	for _, f := range files {
		if hasEndpoint[f.FilePath] || isEntryLike(f.FilePath) {
			if !reachable[f.FilePath] {
				reachable[f.FilePath] = true
				queue = append(queue, f.FilePath)
			}
		}
	}
	if len(queue) == 0 {
		return nil // no seeds → undecidable; don't flag anything as dead
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if !reachable[nb] {
				reachable[nb] = true
				queue = append(queue, nb)
			}
		}
	}
	return reachable
}

// isEntryLike reports whether a file is a plausible entry point by name — a main
// package, a Next.js route (page/layout/route), or a barrel/module index.
func isEntryLike(path string) bool {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch strings.ToLower(base) {
	case "index", "main", "app", "_app", "layout", "page", "route", "server", "mod", "lib", "cli":
		return true
	}
	return false
}

// depKey normalises an import specifier to a package name so subpaths collapse:
// npm "react-dom/client" -> "react-dom", scoped "@scope/pkg/x" -> "@scope/pkg".
// Path-like specifiers (Go/Rust, a dot in the first segment) are kept whole.
func depKey(spec string) string {
	if spec == "" || strings.HasPrefix(spec, ".") {
		return spec
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	if strings.Contains(parts[0], ".") { // github.com/…, golang.org/… — keep full
		return spec
	}
	return parts[0]
}

// --- helpers ----------------------------------------------------------------

const rootFolder = "(root)"

func topFolder(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return rootFolder
}

func repoName(root string) string {
	r := strings.TrimRight(root, `/\`)
	if i := strings.LastIndexAny(r, `/\`); i >= 0 {
		return r[i+1:]
	}
	return r
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// extractJSON pulls the first {...} object out of a model response.
func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
