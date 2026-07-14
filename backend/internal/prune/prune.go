// Package prune detects likely dead / stale code in a repo — "Synaptic Pruning",
// after the brain's elimination of unused connections. It works off the AST
// dependency graph (resolved file→file imports, exports, the intra-file call
// graph) and reports CANDIDATES with a confidence level + evidence, never
// certainties: static analysis cannot see dynamic dispatch, reflection,
// framework-invoked handlers, or cross-repo public-API usage, so everything here
// is meant for human review, not automated deletion.
package prune

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// Candidate is one piece of likely-dead code with the evidence behind it.
type Candidate struct {
	Kind       string   `json:"kind"`       // file | export | function
	Tier       string   `json:"tier"`       // orphan_file | dead_cluster | unused_export | unused_function
	Path       string   `json:"path"`       // file path
	Symbol     string   `json:"symbol,omitempty"`
	Language   string   `json:"language"`
	Confidence string   `json:"confidence"` // high | medium
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence"`
	Uncertain  bool     `json:"uncertain"` // dynamic imports in the repo could hide usage
}

// Report is the full Synaptic Pruning analysis for a repo.
type Report struct {
	Repo        string         `json:"repo"`
	Name        string         `json:"name"`
	TotalFiles  int            `json:"total_files"`
	CodeFiles   int            `json:"code_files"`
	EntryPoints []string       `json:"entry_points"`
	Candidates  []Candidate    `json:"candidates"`
	Summary     map[string]int `json:"summary"` // counts per tier
	Notes       []string       `json:"notes"`   // caveats (dynamic imports, language coverage)
}

// Engine builds pruning reports from the store's AST graph. When Chat is set and
// Verify is true, an LLM pass reviews file-level candidates to drop
// framework-invoked / dynamically-loaded false positives.
type Engine struct {
	Store  *store.Store
	Chat   llm.ChatClient // optional — enables the verification pass
	Verify bool

	mu    sync.Mutex
	cache map[string]*Report
}

// Analyze runs the full multi-signal dead-code analysis for a repo root, fetching
// the AST graph from the store. The result (including the LLM verification pass)
// is cached per root; pass refresh=true to recompute.
func (e *Engine) Analyze(ctx context.Context, root string, refresh bool) (*Report, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repo is required")
	}
	e.mu.Lock()
	if e.cache == nil {
		e.cache = map[string]*Report{}
	}
	if !refresh {
		if r, ok := e.cache[root]; ok {
			e.mu.Unlock()
			return r, nil
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
	calls, _ := e.Store.CallsByRoot(ctx, root)
	decls, _ := e.Store.DeclarationsByRoot(ctx, root)
	rep := analyze(root, files, rels, calls, decls)
	if e.Chat != nil && e.Verify {
		e.verify(ctx, rep, rels)
	}

	e.mu.Lock()
	e.cache[root] = rep
	e.mu.Unlock()
	return rep, nil
}

// analyze is the pure analysis over already-fetched graph data (store-free, so it
// is directly unit-testable).
func analyze(root string, files []store.FileRow, rels []store.RelRow, calls []store.CallRow, decls []store.DeclRow) *Report {
	g := buildGraph(files, rels, calls, decls)

	rep := &Report{
		Repo:       root,
		Name:       repoName(root),
		TotalFiles: len(files),
		CodeFiles:  g.codeFileCount,
		Summary:    map[string]int{},
	}
	for f := range g.entry {
		rep.EntryPoints = append(rep.EntryPoints, f)
	}
	sort.Strings(rep.EntryPoints)

	reach := g.reachable()
	add := func(c Candidate) {
		rep.Candidates = append(rep.Candidates, c)
		rep.Summary[c.Tier]++
	}

	// --- Tiers A & B: module-level reachability (all languages) ---------------
	for _, f := range g.codeFiles {
		if g.entry[f] || g.isTest[f] || reach[f] {
			continue
		}
		uncertain := g.hasDynamicImports
		m := g.moduleOf[f]
		importers := g.modImporters[m]
		unit := "this file"
		if g.lang[f] == "go" {
			unit = "this file's package (" + m + ")"
		}
		if len(importers) == 0 {
			exportCount := len(g.exportsByFile[f])
			reason := fmt.Sprintf("Exports %d symbol(s) but nothing in the repository imports %s, and it is not an entry point.", exportCount, unit)
			evidence := []string{fmt.Sprintf("incoming imports: 0 · exports: %d", exportCount)}
			if exportCount == 0 {
				// No exports AND no importers = an inert file: a leftover stub, a
				// fully commented-out module, or dead scratch code. Strongest signal.
				reason = "No exports and no importers — an inert file (leftover stub, fully commented-out module, or dead scratch code)."
				evidence = append(evidence, "0 exported symbols — nothing here can be referenced")
			}
			add(Candidate{
				Kind: "file", Tier: "orphan_file", Path: f, Language: g.lang[f],
				Confidence: "high",
				Reason:     reason,
				Evidence:   evidence,
				Uncertain:  uncertain,
			})
		} else {
			add(Candidate{
				Kind: "file", Tier: "dead_cluster", Path: f, Language: g.lang[f],
				Confidence: "medium",
				Reason:     "Reached only from other unreachable code — a disconnected island never reached from any entry point.",
				Evidence:   []string{"imported by: " + strings.Join(shortList(sortedKeys(importers), 6), ", ")},
				Uncertain:  uncertain,
			})
		}
	}

	// --- Tier C: unused exports (named-import languages: ts/js/rust) ----------
	for _, f := range g.codeFiles {
		if !reach[f] || g.entry[f] || g.isTest[f] || g.namespaceImported[f] {
			continue
		}
		if !namedImportLang(g.lang[f]) {
			continue
		}
		for _, s := range g.valueExports[f] {
			if s == "" || s == "default" || g.namedImported[s] {
				continue
			}
			add(Candidate{
				Kind: "export", Tier: "unused_export", Path: f, Symbol: s, Language: g.lang[f],
				Confidence: "medium",
				Reason:     "Exported but never imported by name anywhere else in the repository (dead export, or could be made private).",
				Evidence:   []string{"no `import { " + s + " }` found across the repo"},
				Uncertain:  g.hasDynamicImports,
			})
		}
	}

	// --- Tier D: unused private functions (file-scoped languages: ts/js) ------
	// Entry files are skipped: they invoke functions at module top-level (e.g.
	// `main()`, IIFEs, framework hooks), which the caller→callee graph doesn't
	// capture, so those would look "uncalled".
	for _, f := range g.codeFiles {
		if !reach[f] || g.isTest[f] || g.entry[f] || !fileScopedLang(g.lang[f]) {
			continue
		}
		exported := setOf(g.exportsByFile[f])
		called := g.localCallees[f]
		for _, d := range g.funcDecls[f] {
			if exported[d] || called[d] {
				continue
			}
			add(Candidate{
				Kind: "function", Tier: "unused_function", Path: f, Symbol: d, Language: g.lang[f],
				Confidence: "medium",
				Reason:     "Private (non-exported) function never called within its file.",
				Evidence:   []string{"no call to `" + d + "` in this file; module-scoped so it cannot be called elsewhere"},
				Uncertain:  true, // a callback / value-reference would not appear in the call graph
			})
		}
	}

	sortCandidates(rep.Candidates)
	rep.Notes = g.notes()
	return rep
}

// graph is the indexed AST view used by the analysis.
//
// Reachability runs at MODULE granularity, not file: Go imports a package
// (directory), so the resolver points a package-import at one representative
// file — meaning sibling files in an imported package would look orphaned at the
// file level. We therefore reach over modules (directory for Go; the file itself
// for file-module languages: TS/JS/Rust) and a file is live iff its module is.
type graph struct {
	codeFiles         []string
	codeFileCount     int
	lang              map[string]string
	isTest            map[string]bool
	entry             map[string]bool
	moduleOf          map[string]string          // file -> its module key
	modImportsOf      map[string]map[string]bool // module -> modules it imports
	modImporters      map[string]map[string]bool // module -> modules importing it
	entryMod          map[string]bool            // modules that are reachability roots
	exportsByFile     map[string][]string        // all exported symbols
	valueExports      map[string][]string        // exports whose usage is trackable (not TS type/interface)
	funcDecls         map[string][]string        // file -> non-method function names (deduped)
	localCallees      map[string]map[string]bool // file -> symbols called within it
	namedImported     map[string]bool            // every symbol imported by name, repo-wide
	namespaceImported map[string]bool            // files imported via `* as`
	hasDynamicImports bool
	langs             map[string]bool
}

// moduleKey maps a file to its module: the package directory for Go, the file
// itself otherwise.
func moduleKey(file, lang string) string {
	if lang == "go" {
		return path.Dir(file)
	}
	return file
}

func buildGraph(files []store.FileRow, rels []store.RelRow, calls []store.CallRow, decls []store.DeclRow) *graph {
	g := &graph{
		lang: map[string]string{}, isTest: map[string]bool{}, entry: map[string]bool{},
		moduleOf: map[string]string{}, modImportsOf: map[string]map[string]bool{},
		modImporters: map[string]map[string]bool{}, entryMod: map[string]bool{},
		exportsByFile: map[string][]string{}, valueExports: map[string][]string{},
		funcDecls: map[string][]string{},
		localCallees: map[string]map[string]bool{}, namedImported: map[string]bool{},
		namespaceImported: map[string]bool{}, langs: map[string]bool{},
	}
	isCode := map[string]bool{}
	for _, f := range files {
		g.lang[f.FilePath] = f.Language
		g.langs[f.Language] = true
		if f.Language == "markdown" {
			continue // docs aren't dead code
		}
		isCode[f.FilePath] = true
		g.codeFiles = append(g.codeFiles, f.FilePath)
		g.isTest[f.FilePath] = isTest(f.FilePath)
		g.moduleOf[f.FilePath] = moduleKey(f.FilePath, f.Language)
	}
	sort.Strings(g.codeFiles)
	g.codeFileCount = len(g.codeFiles)

	hasEndpoint := map[string]bool{}
	for _, r := range rels {
		switch r.RelationshipType {
		case "imports":
			src := r.SourceSymbol
			ext, _ := r.Metadata["external"].(bool)
			if unresolved, _ := r.Metadata["unresolved"].(bool); unresolved {
				g.hasDynamicImports = true
			}
			// Record named / namespace imports for the unused-export check.
			if syms, ok := r.Metadata["symbols"].([]any); ok {
				for _, s := range syms {
					name, _ := s.(string)
					if strings.HasPrefix(name, "* as ") || name == "*" {
						if isCode[r.TargetSymbol] {
							g.namespaceImported[r.TargetSymbol] = true
						}
					} else if name != "" {
						g.namedImported[name] = true
					}
				}
			}
			if ext || !isCode[src] || !isCode[r.TargetSymbol] || src == r.TargetSymbol {
				continue
			}
			// Edge at module granularity (Go: package dir; else: file).
			mA, mB := g.moduleOf[src], g.moduleOf[r.TargetSymbol]
			if mA == mB {
				continue // same package — not a cross-module dependency
			}
			if g.modImportsOf[mA] == nil {
				g.modImportsOf[mA] = map[string]bool{}
			}
			if g.modImporters[mB] == nil {
				g.modImporters[mB] = map[string]bool{}
			}
			g.modImportsOf[mA][mB] = true
			g.modImporters[mB][mA] = true
		case "exports":
			if isCode[r.SourceSymbol] {
				g.exportsByFile[r.SourceSymbol] = append(g.exportsByFile[r.SourceSymbol], r.TargetSymbol)
				// TS interface/type exports are erased — their usage as types isn't
				// captured by import symbols, so they can't be checked for "unused".
				if kind, _ := r.Metadata["kind"].(string); kind != "interface" && kind != "type" {
					g.valueExports[r.SourceSymbol] = append(g.valueExports[r.SourceSymbol], r.TargetSymbol)
				}
			}
		case "endpoint":
			if isCode[r.SourceSymbol] {
				hasEndpoint[r.SourceSymbol] = true
			}
		}
	}

	for _, c := range calls {
		if g.localCallees[c.File] == nil {
			g.localCallees[c.File] = map[string]bool{}
		}
		g.localCallees[c.File][baseSym(c.Callee)] = true
	}
	for _, d := range decls {
		if d.ChunkType == "function" {
			g.funcDecls[d.File] = appendUnique(g.funcDecls[d.File], baseSym(d.Symbol))
		}
	}

	for _, f := range g.codeFiles {
		// Only store TRUE entries — EntryPoints ranges over this map.
		if isEntry(f, g.lang[f], hasEndpoint[f]) || g.isTest[f] {
			g.entry[f] = true
			g.entryMod[g.moduleOf[f]] = true
		}
	}
	return g
}

// reachable returns the set of FILES that are live: a file is live iff its
// module is reachable (via internal imports) from any entry module. BFS runs
// over modules so a whole Go package goes live together.
func (g *graph) reachable() map[string]bool {
	seenMod := map[string]bool{}
	var stack []string
	for m := range g.entryMod {
		if !seenMod[m] {
			seenMod[m] = true
			stack = append(stack, m)
		}
	}
	for len(stack) > 0 {
		m := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for dep := range g.modImportsOf[m] {
			if !seenMod[dep] {
				seenMod[dep] = true
				stack = append(stack, dep)
			}
		}
	}
	seen := map[string]bool{}
	for _, f := range g.codeFiles {
		if seenMod[g.moduleOf[f]] {
			seen[f] = true
		}
	}
	return seen
}

func (g *graph) notes() []string {
	var n []string
	if g.hasDynamicImports {
		n = append(n, "This repo uses dynamic/unresolved imports — some files flagged as unreachable may actually be loaded dynamically. Treat file-level findings as candidates to verify.")
	}
	if g.langs["go"] {
		n = append(n, "Go: unused-export and unused-function checks are skipped — Go's package-scoped visibility means cross-file usage isn't captured by the graph. Run `staticcheck` (U1000) for those.")
	}
	if g.langs["rust"] {
		n = append(n, "Rust: unused-function checks are skipped (module scoping); `cargo` already reports `dead_code` for those.")
	}
	if g.langs["python"] {
		n = append(n, "Python: only file-level reachability is reported. Symbol-level checks are skipped because attribute access (`module.symbol`), dynamic dispatch, and framework-invoked handlers hide real usage — run `vulture` / `pyflakes` for those.")
	}
	n = append(n, "Findings are review candidates, not certainties: reflection, framework-invoked handlers, and public APIs consumed outside this repo can't be seen statically.")
	return n
}

// --- heuristics -------------------------------------------------------------

// isTest reports a test file. Test runners discover these by filename or
// directory — nothing imports them — so "0 incoming imports" is expected and must
// never be reported as dead code. pytest's `test_*.py` / `*_test.py` convention in
// particular accounts for whole directories of false positives when missed.
func isTest(file string) bool {
	b := path.Base(file)
	switch {
	case strings.HasSuffix(file, "_test.go"): // Go
		return true
	case strings.Contains(b, ".test.") || strings.Contains(b, ".spec."): // JS/TS
		return true
	case b == "conftest.py" || strings.HasSuffix(b, "_test.py") ||
		(strings.HasPrefix(b, "test_") && strings.HasSuffix(b, ".py")): // pytest/unittest
		return true
	}
	return strings.Contains(file, "/testdata/") || strings.Contains(file, "__tests__/") ||
		strings.Contains(file, "__mocks__/") ||
		strings.Contains(file, "/tests/") || strings.HasPrefix(file, "tests/") ||
		strings.Contains(file, "/test/") || strings.HasPrefix(file, "test/")
}

// isEntry reports whether a file is a reachability root (never dead): a route
// handler, a framework-convention entry, a CLI main, a crate root, or config.
func isEntry(file, lang string, hasEndpoint bool) bool {
	if hasEndpoint {
		return true
	}
	// Maintenance / one-off scripts are executed by hand (`python scripts/x.py`),
	// never imported. Same for anything under bin/.
	if strings.HasPrefix(file, "scripts/") || strings.Contains(file, "/scripts/") ||
		strings.HasPrefix(file, "bin/") || strings.Contains(file, "/bin/") {
		return true
	}
	b := path.Base(file)
	name := strings.TrimSuffix(b, path.Ext(b))
	switch lang {
	case "go":
		return b == "main.go" || strings.Contains(file, "/cmd/")
	case "rust":
		return b == "main.rs" || b == "lib.rs" || b == "mod.rs"
	case "python":
		// Package initialisers (run implicitly on package import), CLI/module
		// entry points, and framework-loaded conventions are reachability roots.
		// `cli.py` is the conventional console_scripts target in pyproject.toml
		// (e.g. `mytool = "src.cli:app"`), which the graph cannot see.
		switch b {
		case "__init__.py", "__main__.py", "manage.py", "setup.py",
			"conftest.py", "wsgi.py", "asgi.py", "app.py", "main.py",
			"cli.py", "settings.py", "urls.py":
			return true
		}
		return false
	}
	// TS/JS — Next.js conventions + Node/Nest bootstrap + general entry files.
	switch name {
	case "page", "layout", "route", "middleware", "proxy", "index",
		"_app", "_document", "error", "loading", "not-found", "global-error",
		"template", "default", "sitemap", "robots", "head", "manifest",
		"main", "server", "bootstrap": // Node/NestJS process entry points
		return true
	}
	// Config / tool-loaded files: consumed by a runtime or toolchain, never
	// imported (e.g. *.config.js, .eslintrc.js, .prettierrc.js, jest.setup.ts).
	if strings.Contains(b, ".config.") || strings.HasPrefix(b, ".") ||
		strings.HasPrefix(name, "opengraph-image") || strings.HasPrefix(name, "icon") ||
		strings.HasPrefix(name, "apple-icon") || strings.HasSuffix(name, ".setup") ||
		strings.HasSuffix(name, ".d") { // *.d.ts ambient declarations
		return true
	}
	return false
}

// namedImportLang: languages whose imports name the symbols they pull in, so an
// "exported but never imported by name" check is meaningful.
func namedImportLang(lang string) bool {
	switch lang {
	case "typescript", "javascript", "tsx", "jsx", "ts", "js", "rust":
		return true
	}
	// MultiParser labels TS/JS as "typescript"/"javascript".
	return lang == "typescript" || lang == "javascript"
}

// fileScopedLang: languages where a non-exported symbol is visible only within
// its own file, so "never called in this file" implies dead.
func fileScopedLang(lang string) bool {
	return lang == "typescript" || lang == "javascript"
}

// --- small helpers ----------------------------------------------------------

func baseSym(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i] // collapse "foo#part2" chunk splits
	}
	return s
}

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortList(xs []string, n int) []string {
	short := make([]string, 0, n+1)
	for i, x := range xs {
		if i >= n {
			short = append(short, fmt.Sprintf("+%d more", len(xs)-n))
			break
		}
		short = append(short, path.Base(x))
	}
	return short
}

var tierRank = map[string]int{"orphan_file": 0, "dead_cluster": 1, "unused_export": 2, "unused_function": 3}

func sortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if tierRank[cs[i].Tier] != tierRank[cs[j].Tier] {
			return tierRank[cs[i].Tier] < tierRank[cs[j].Tier]
		}
		if cs[i].Path != cs[j].Path {
			return cs[i].Path < cs[j].Path
		}
		return cs[i].Symbol < cs[j].Symbol
	})
}

func repoName(root string) string {
	r := strings.TrimRight(root, `/\`)
	if i := strings.LastIndexAny(r, `/\`); i >= 0 {
		return r[i+1:]
	}
	return r
}
