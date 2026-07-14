// Package agentctx assembles a compact, portable description of an ingested
// repository — everything another AI agent needs to understand the project
// without reading it — into one JSON document.
//
// It is deterministic and derived from the AST graph already in the store
// (languages, entry points, HTTP endpoints, module layout, hub files, external
// dependencies). The generated documentation pages are folded in when available,
// giving the narrative alongside the structural facts.
package agentctx

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"project-synapse/backend/internal/docs"
	"project-synapse/backend/internal/store"
)

// Schema identifies the payload shape so consumers can version against it.
const Schema = "synapse.agent-context/v1"

// Context is the exported project brief.
type Context struct {
	Schema      string `json:"schema"`
	Repo        string `json:"repo"`
	Name        string `json:"name"`
	GeneratedAt string `json:"generated_at"`

	// HowToUse is addressed to the agent reading this file.
	HowToUse string `json:"how_to_use"`

	Stats        Stats        `json:"stats"`
	Languages    []LangStat   `json:"languages"`
	EntryPoints  []string     `json:"entry_points"`
	Endpoints    []Endpoint   `json:"endpoints"`
	Modules      []Module     `json:"modules"`
	KeyFiles     []KeyFile    `json:"key_files"`
	Dependencies []Dependency `json:"dependencies"`
	Docs         []DocPage    `json:"docs,omitempty"`
}

// Stats are headline counts for the repo.
type Stats struct {
	TotalFiles    int `json:"total_files"`
	CodeFiles     int `json:"code_files"`
	DocFiles      int `json:"doc_files"`
	Endpoints     int `json:"endpoints"`
	InternalEdges int `json:"internal_import_edges"`
	ExternalDeps  int `json:"external_dependencies"`
}

// LangStat is the file count for one language.
type LangStat struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
}

// Endpoint is one HTTP route the graph found.
type Endpoint struct {
	Method  string `json:"method,omitempty"`
	Path    string `json:"path"`
	File    string `json:"file"`
	Handler string `json:"handler,omitempty"`
}

// Module is a top-level source directory with what it exposes.
type Module struct {
	Path    string   `json:"path"`
	Files   int      `json:"files"`
	Exports []string `json:"key_exports,omitempty"`
}

// KeyFile is a hub: a file many others import. `ImportedBy` is its fan-in.
type KeyFile struct {
	Path       string   `json:"path"`
	Language   string   `json:"language"`
	ImportedBy int      `json:"imported_by"`
	Exports    []string `json:"exports,omitempty"`
}

// Dependency is a THIRD-PARTY package and how widely it is used. Standard-library
// and runtime-builtin imports (`os`, `datetime`, `net/http`, `fs`, `std`) are
// excluded — they describe the language, not the project's tech stack.
type Dependency struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
}

// DocPage is one generated documentation section (markdown).
type DocPage struct {
	Title   string `json:"title"`
	Group   string `json:"group,omitempty"`
	Content string `json:"content"`
}

// Engine builds the export. Docs may be nil (the structural facts are still
// produced); Now may be nil (defaults to time.Now).
type Engine struct {
	Store *store.Store
	Docs  *docs.Engine
	Now   func() time.Time
}

const howToUse = "This file is a complete, self-contained brief of one software project, generated from its real AST dependency graph. " +
	"Read `docs` for the narrative (what it is, its architecture, core concepts, and data flow), then use `entry_points`, `endpoints`, and `key_files` to locate behaviour, " +
	"`modules` for the layout, and `dependencies` for the live tech stack. Every path, symbol, and route here was extracted from the source — none of it is inferred. " +
	"Treat it as ground truth about the codebase and cite exact paths from it."

// Build assembles the context document for one ingested repo root.
func (e *Engine) Build(ctx context.Context, root string, includeDocs bool) (*Context, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repo is required")
	}
	files, err := e.Store.FilesByRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found for repo")
	}
	rels, _ := e.Store.RelationshipsByRoot(ctx, root)

	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	c := &Context{
		Schema:      Schema,
		Repo:        root,
		Name:        repoName(root),
		GeneratedAt: now().UTC().Format(time.RFC3339),
		HowToUse:    howToUse,
	}

	langOf := map[string]string{}
	for _, f := range files {
		langOf[f.FilePath] = f.Language
	}

	c.Languages = languages(files)
	c.Stats = stats(files, rels)
	c.Endpoints = endpoints(rels)
	c.Stats.Endpoints = len(c.Endpoints)

	importers, exportsBy, extDeps, internalEdges := graphFacts(rels, langOf)
	c.Stats.InternalEdges = internalEdges
	c.Stats.ExternalDeps = len(extDeps) // the true total, not the truncated list below
	c.Dependencies = dependencies(extDeps)

	c.EntryPoints = entryPoints(files, c.Endpoints)
	c.KeyFiles = keyFiles(files, importers, exportsBy)
	c.Modules = modules(files, exportsBy)

	if includeDocs && e.Docs != nil {
		if d, derr := e.Docs.Generate(ctx, root, false); derr == nil && d != nil {
			for _, s := range d.Sections {
				c.Docs = append(c.Docs, DocPage{Title: s.Title, Group: s.Group, Content: s.Content})
			}
		}
	}
	return c, nil
}

func isDoc(lang string) bool { return lang == "markdown" }

func languages(files []store.FileRow) []LangStat {
	counts := map[string]int{}
	for _, f := range files {
		counts[f.Language]++
	}
	out := make([]LangStat, 0, len(counts))
	for l, n := range counts {
		out = append(out, LangStat{Language: l, Files: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Language < out[j].Language
	})
	return out
}

func stats(files []store.FileRow, rels []store.RelRow) Stats {
	s := Stats{TotalFiles: len(files)}
	for _, f := range files {
		if isDoc(f.Language) {
			s.DocFiles++
		} else {
			s.CodeFiles++
		}
	}
	return s
}

func endpoints(rels []store.RelRow) []Endpoint {
	var out []Endpoint
	seen := map[string]bool{}
	for _, r := range rels {
		if r.RelationshipType != "endpoint" {
			continue
		}
		method, _ := r.Metadata["method"].(string)
		p, _ := r.Metadata["path"].(string)
		handler, _ := r.Metadata["handler"].(string)
		if p == "" {
			p = r.TargetSymbol
		}
		k := method + " " + p + " " + r.SourceSymbol
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Endpoint{Method: method, Path: p, File: r.SourceSymbol, Handler: handler})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// graphFacts derives fan-in, exports per file, third-party dep usage, and the
// internal edge count in one pass. Standard-library imports are dropped: the
// language of the IMPORTING file decides what counts as stdlib.
func graphFacts(rels []store.RelRow, langOf map[string]string) (importers map[string]int, exportsBy map[string][]string, extDeps map[string]map[string]bool, internalEdges int) {
	importers = map[string]int{}
	exportsBy = map[string][]string{}
	extDeps = map[string]map[string]bool{}
	seenEdge := map[string]bool{}

	for _, r := range rels {
		switch r.RelationshipType {
		case "exports":
			exportsBy[r.SourceSymbol] = append(exportsBy[r.SourceSymbol], r.TargetSymbol)
		case "imports":
			if ext, _ := r.Metadata["external"].(bool); ext {
				spec, _ := r.Metadata["specifier"].(string)
				if spec == "" {
					spec = r.TargetSymbol
				}
				k, thirdParty := classifyDep(spec, langOf[r.SourceSymbol])
				if !thirdParty {
					continue // stdlib / runtime builtin: not part of the tech stack
				}
				if extDeps[k] == nil {
					extDeps[k] = map[string]bool{}
				}
				extDeps[k][r.SourceSymbol] = true
				continue
			}
			edge := r.SourceSymbol + ">" + r.TargetSymbol
			if seenEdge[edge] || r.SourceSymbol == r.TargetSymbol {
				continue
			}
			seenEdge[edge] = true
			internalEdges++
			importers[r.TargetSymbol]++
		}
	}
	return importers, exportsBy, extDeps, internalEdges
}

func dependencies(extDeps map[string]map[string]bool) []Dependency {
	out := make([]Dependency, 0, len(extDeps))
	for name, fs := range extDeps {
		out = append(out, Dependency{Name: name, Files: len(fs)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func entryPoints(files []store.FileRow, eps []Endpoint) []string {
	set := map[string]bool{}
	for _, e := range eps {
		set[e.File] = true
	}
	for _, f := range files {
		if !isDoc(f.Language) && isEntryLike(f.FilePath) {
			set[f.FilePath] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// isEntryLike matches the framework/CLI conventions for a process or route entry.
func isEntryLike(p string) bool {
	b := path.Base(p)
	name := strings.TrimSuffix(b, path.Ext(b))
	switch b {
	case "main.go", "main.rs", "lib.rs", "__main__.py", "manage.py",
		"wsgi.py", "asgi.py", "app.py", "main.py", "conftest.py":
		return true
	}
	switch name {
	case "index", "main", "server", "bootstrap", "page", "layout", "route", "middleware":
		return true
	}
	return strings.Contains(p, "/cmd/")
}

func keyFiles(files []store.FileRow, importers map[string]int, exportsBy map[string][]string) []KeyFile {
	lang := map[string]string{}
	for _, f := range files {
		lang[f.FilePath] = f.Language
	}
	out := make([]KeyFile, 0, len(importers))
	for p, n := range importers {
		if n < 2 || isDoc(lang[p]) {
			continue // a hub is a file many things depend on
		}
		out = append(out, KeyFile{Path: p, Language: lang[p], ImportedBy: n, Exports: capStrings(exportsBy[p], 8)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ImportedBy != out[j].ImportedBy {
			return out[i].ImportedBy > out[j].ImportedBy
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func modules(files []store.FileRow, exportsBy map[string][]string) []Module {
	byDir := map[string]int{}
	exp := map[string][]string{}
	for _, f := range files {
		if isDoc(f.Language) {
			continue
		}
		d := path.Dir(f.FilePath)
		if d == "." {
			d = "(root)"
		}
		byDir[d]++
		exp[d] = append(exp[d], exportsBy[f.FilePath]...)
	}
	out := make([]Module, 0, len(byDir))
	for d, n := range byDir {
		out = append(out, Module{Path: d, Files: n, Exports: capStrings(dedupe(exp[d]), 10)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// pythonStdlib covers the standard-library modules that realistically show up in
// application code. `__future__` in particular is a compiler directive, not a
// dependency, and would otherwise top the usage ranking.
var pythonStdlib = map[string]bool{
	"__future__": true, "abc": true, "argparse": true, "ast": true, "asyncio": true,
	"base64": true, "binascii": true, "bisect": true, "calendar": true, "collections": true,
	"concurrent": true, "configparser": true, "contextlib": true, "copy": true, "csv": true,
	"ctypes": true, "dataclasses": true, "datetime": true, "decimal": true, "difflib": true,
	"enum": true, "errno": true, "fnmatch": true, "functools": true, "glob": true,
	"gzip": true, "hashlib": true, "heapq": true, "hmac": true, "html": true,
	"http": true, "importlib": true, "inspect": true, "io": true, "ipaddress": true,
	"itertools": true, "json": true, "logging": true, "math": true, "mimetypes": true,
	"multiprocessing": true, "operator": true, "os": true, "pathlib": true, "pickle": true,
	"platform": true, "pprint": true, "queue": true, "random": true, "re": true,
	"secrets": true, "shlex": true, "shutil": true, "signal": true, "site": true,
	"socket": true, "sqlite3": true, "ssl": true, "statistics": true, "string": true,
	"struct": true, "subprocess": true, "sys": true, "tempfile": true, "textwrap": true,
	"threading": true, "time": true, "timeit": true, "tomllib": true, "traceback": true,
	"types": true, "typing": true, "unicodedata": true, "unittest": true, "urllib": true,
	"uuid": true, "warnings": true, "weakref": true, "xml": true, "zipfile": true,
	"zoneinfo": true,
}

// nodeBuiltins are Node.js runtime modules (with or without the `node:` prefix).
var nodeBuiltins = map[string]bool{
	"assert": true, "buffer": true, "child_process": true, "cluster": true, "console": true,
	"crypto": true, "dns": true, "events": true, "fs": true, "http": true, "http2": true,
	"https": true, "module": true, "net": true, "os": true, "path": true, "perf_hooks": true,
	"process": true, "querystring": true, "readline": true, "stream": true,
	"string_decoder": true, "timers": true, "tls": true, "tty": true, "url": true,
	"util": true, "v8": true, "vm": true, "worker_threads": true, "zlib": true,
}

var rustStd = map[string]bool{"std": true, "core": true, "alloc": true, "proc_macro": true}

// classifyDep reduces an import specifier to its installable package name and
// reports whether it is a third-party dependency (rather than stdlib/builtin).
// The importing file's language decides: a bare `os` is Python stdlib in a .py
// file but could be an npm package name elsewhere.
func classifyDep(spec, lang string) (name string, thirdParty bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", false
	}
	switch lang {
	case "python":
		head := spec
		if i := strings.IndexByte(head, '.'); i > 0 {
			head = head[:i]
		}
		return head, !pythonStdlib[head]

	case "rust":
		head := spec
		if i := strings.Index(head, "::"); i > 0 {
			head = head[:i]
		}
		head = strings.TrimPrefix(head, "mod:")
		return head, !rustStd[head]

	case "go":
		parts := strings.Split(spec, "/")
		// Go module paths start with a domain; stdlib never contains a dot.
		if !strings.Contains(parts[0], ".") {
			return parts[0], false
		}
		if len(parts) >= 3 {
			return strings.Join(parts[:3], "/"), true // host/owner/repo
		}
		return spec, true

	default: // typescript / javascript
		s := strings.TrimPrefix(spec, "node:")
		parts := strings.Split(s, "/")
		if strings.HasPrefix(s, "@") && len(parts) >= 2 {
			return parts[0] + "/" + parts[1], true
		}
		return parts[0], !nodeBuiltins[parts[0]]
	}
}

func repoName(root string) string {
	r := strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/")
	if i := strings.LastIndexByte(r, '/'); i >= 0 {
		return r[i+1:]
	}
	return r
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0:0]
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func capStrings(xs []string, n int) []string {
	xs = dedupe(xs)
	if len(xs) > n {
		xs = xs[:n]
	}
	if len(xs) == 0 {
		return nil
	}
	return xs
}
