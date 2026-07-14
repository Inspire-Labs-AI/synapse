package parser

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
)

// AliasRule is one tsconfig `paths` mapping, pre-split around its `*` wildcard:
// e.g. `"@/*": ["./*"]` under baseUrl "." in dir "frontend" becomes
// {BaseDir:"frontend", Prefix:"@/", Suffix:"", Targets:["./*"]}.
type AliasRule struct {
	BaseDir string
	Prefix  string
	Suffix  string
	Targets []string
}

// resolveExtensions are tried, in order, when a relative import omits its
// extension (the common TS/JS case: import './db').
var resolveExtensions = []string{
	".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
	// Markdown fallbacks let "myelin" doc links (e.g. [x](./guide)) resolve.
	".md", ".markdown", ".mdx",
}

// indexFiles are tried when a relative import points at a directory.
var indexFiles = []string{
	"index.ts", "index.tsx", "index.js", "index.jsx", "README.md", "index.md",
}

// BuildKnownSet indexes the ingest batch's relative paths (forward-slashed) for
// O(1) import-resolution lookups.
func BuildKnownSet(relPaths []string) map[string]bool {
	set := make(map[string]bool, len(relPaths))
	for _, p := range relPaths {
		set[path.Clean(strings.ReplaceAll(p, "\\", "/"))] = true
	}
	return set
}

// ResolveIndex is the cross-file index used to resolve imports. It carries the
// known file set plus a directory→files map (needed for Go package imports and
// Rust module resolution, which point at directories/modules, not single files).
type ResolveIndex struct {
	Files    map[string]bool     // relpath -> true
	dirs     map[string][]string // dir relpath ("" = root) -> sorted files in it
	aliases  []AliasRule         // tsconfig path aliases (e.g. @/* -> ./*)
	baseDirs []string            // tsconfig baseUrl roots (e.g. "" for baseUrl ".")
	pyRoots  []string            // Python sys.path-style import roots ("" , "src", package parents)
}

// BuildIndex builds a ResolveIndex from the ingest batch's relative paths.
func BuildIndex(relPaths []string) *ResolveIndex {
	return BuildIndexWithConfig(relPaths, nil, nil)
}

// BuildIndexWithAliases builds a ResolveIndex with tsconfig path aliases, so
// imports like `@/lib/x` resolve to real files instead of looking external.
func BuildIndexWithAliases(relPaths []string, aliases []AliasRule) *ResolveIndex {
	return BuildIndexWithConfig(relPaths, aliases, nil)
}

// BuildIndexWithConfig builds a ResolveIndex with tsconfig path aliases AND
// baseUrl roots, so both `@/lib/x` (alias) and `src/common/x` (baseUrl-relative,
// the NestJS convention) resolve to real files instead of looking external.
func BuildIndexWithConfig(relPaths []string, aliases []AliasRule, baseDirs []string) *ResolveIndex {
	files := BuildKnownSet(relPaths)
	dirs := map[string][]string{}
	for p := range files {
		d := path.Dir(p)
		if d == "." {
			d = ""
		}
		dirs[d] = append(dirs[d], p)
	}
	for k := range dirs {
		sort.Strings(dirs[k])
	}
	ix := &ResolveIndex{Files: files, dirs: dirs, aliases: aliases, baseDirs: dedupeStrings(baseDirs)}
	ix.pyRoots = computePyRoots(files)
	return ix
}

// computePyRoots derives the Python import roots (sys.path entries) for the batch:
// always the repo root (""), a top-level `src/` when present, and the parent
// directory of every top-level package (a dir with an `__init__.py` whose own
// parent is not itself a package). Absolute imports like `import app.services.x`
// resolve against these roots.
func computePyRoots(files map[string]bool) []string {
	roots := map[string]bool{"": true}
	for f := range files {
		if path.Base(f) != "__init__.py" {
			continue
		}
		pkgDir := path.Dir(f)
		if pkgDir == "." {
			pkgDir = ""
		}
		parent := path.Dir(pkgDir)
		if parent == "." {
			parent = ""
		}
		// A top-level package's parent is a source root (its parent isn't a package).
		if !files[joinRel(parent, "__init__.py")] {
			roots[parent] = true
		}
	}
	for f := range files {
		if strings.HasPrefix(f, "src/") {
			roots["src"] = true
			break
		}
	}
	out := make([]string, 0, len(roots))
	for r := range roots {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// ResolveImports fills the Resolved / External / ResolvedOK fields on every
// import of fa, dispatching on the file's language (Go packages and Rust
// modules resolve differently from TS/JS relative paths).
func ResolveImports(fa *FileAnalysis, ix *ResolveIndex) {
	switch fa.Language {
	case "go":
		resolveGoImports(fa, ix)
	case "rust":
		resolveRustImports(fa, ix)
	case "python":
		resolvePyImports(fa, ix)
	default:
		dir := path.Dir(fa.RelPath)
		for i := range fa.Imports {
			spec := fa.Imports[i].Specifier
			// tsconfig path aliases first (e.g. `@/lib/x`), then baseUrl-relative
			// bare imports (e.g. `src/common/x`), then relative resolution.
			if resolved, ok := ix.resolveAlias(spec); ok {
				fa.Imports[i].Resolved = resolved
				fa.Imports[i].External = false
				fa.Imports[i].ResolvedOK = true
				continue
			}
			if resolved, ok := ix.resolveBaseURL(spec); ok {
				fa.Imports[i].Resolved = resolved
				fa.Imports[i].External = false
				fa.Imports[i].ResolvedOK = true
				continue
			}
			resolved, external, ok := resolveSpecifier(dir, spec, ix.Files)
			fa.Imports[i].Resolved = resolved
			fa.Imports[i].External = external
			fa.Imports[i].ResolvedOK = ok
		}
	}
}

// resolveBaseURL resolves a non-relative ("bare") specifier against the tsconfig
// baseUrl root(s) — e.g. a NestJS `import { X } from 'src/common/x'` under
// baseUrl ".". npm packages (react, @nestjs/common, mongoose) simply won't match
// a repo file and fall through to the external classification.
func (ix *ResolveIndex) resolveBaseURL(specifier string) (string, bool) {
	if specifier == "" || specifier[0] == '.' || specifier[0] == '/' {
		return "", false // relative imports resolve the usual way
	}
	for _, base := range ix.baseDirs {
		if r, ok := ix.resolveRelPath(joinRel(base, specifier)); ok {
			return r, true
		}
	}
	return "", false
}

// resolveAlias rewrites a tsconfig-aliased specifier (e.g. `@/lib/x`) to a real
// file using the loaded path rules, trying each rule's targets + the usual
// extension/index fallbacks.
func (ix *ResolveIndex) resolveAlias(specifier string) (string, bool) {
	for _, rule := range ix.aliases {
		if len(specifier) < len(rule.Prefix)+len(rule.Suffix) ||
			!strings.HasPrefix(specifier, rule.Prefix) || !strings.HasSuffix(specifier, rule.Suffix) {
			continue
		}
		star := specifier[len(rule.Prefix) : len(specifier)-len(rule.Suffix)]
		for _, target := range rule.Targets {
			rel := strings.TrimPrefix(strings.Replace(target, "*", star, 1), "./")
			if r, ok := ix.resolveRelPath(joinRel(rule.BaseDir, rel)); ok {
				return r, true
			}
		}
	}
	return "", false
}

// stripJSONC removes // and /* */ comments and trailing commas from JSONC, while
// respecting string literals (so glob patterns like "**/*.ts" survive intact).
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) { // keep escaped char (e.g. \")
				out = append(out, b[i+1])
				i++
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/': // line comment
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*': // block comment
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++ // skip the closing '/'
		default:
			out = append(out, c)
		}
	}
	return jsoncTrailingComma.ReplaceAll(out, []byte("$1"))
}

// resolveRelPath resolves a repo-relative base path against the file set, trying
// the exact path, known source extensions, then directory index files.
func (ix *ResolveIndex) resolveRelPath(rel string) (string, bool) {
	rel = path.Clean(rel)
	if ix.Files[rel] {
		return rel, true
	}
	for _, ext := range resolveExtensions {
		if ix.Files[rel+ext] {
			return rel + ext, true
		}
	}
	for _, idx := range indexFiles {
		if c := path.Join(rel, idx); ix.Files[c] {
			return c, true
		}
	}
	return "", false
}

var jsoncTrailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// TSConfig holds the import-resolution settings extracted from a tsconfig.
type TSConfig struct {
	Aliases    []AliasRule // path-alias rules (e.g. @/* -> ./*)
	BaseDir    string      // baseUrl directory relative to the repo root ("" = root)
	HasBaseDir bool        // whether the tsconfig sets a baseUrl at all
}

// ParseTSConfig extracts both the path-alias rules AND the baseUrl root from a
// tsconfig/jsconfig file. tsconfigDir is the file's directory relative to the
// repo root (""=root). Tolerates JSONC (comments + trailing commas).
func ParseTSConfig(tsconfigDir string, content []byte) TSConfig {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF}) // strip a UTF-8 BOM

	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	// Most tsconfigs are valid JSON; only fall back to JSONC stripping if not, so
	// glob patterns like "**/*.ts" (which contain /* and */) are never mangled.
	if json.Unmarshal(content, &cfg) != nil {
		if json.Unmarshal(stripJSONC(content), &cfg) != nil {
			return TSConfig{}
		}
	}

	var out TSConfig
	base := tsconfigDir
	if b := strings.TrimSpace(cfg.CompilerOptions.BaseURL); b != "" {
		base = joinRel(tsconfigDir, b)
		if base == "." {
			base = ""
		}
		out.BaseDir = base
		out.HasBaseDir = true
	}
	for pattern, targets := range cfg.CompilerOptions.Paths {
		i := strings.IndexByte(pattern, '*')
		if i < 0 {
			continue // exact (non-wildcard) aliases are rare; skip for now
		}
		out.Aliases = append(out.Aliases, AliasRule{
			BaseDir: base,
			Prefix:  pattern[:i],
			Suffix:  pattern[i+1:],
			Targets: targets,
		})
	}
	return out
}

// ParseTSConfigPaths extracts just the path-alias rules from a tsconfig/jsconfig.
func ParseTSConfigPaths(tsconfigDir string, content []byte) []AliasRule {
	return ParseTSConfig(tsconfigDir, content).Aliases
}

// resolveGoImports maps Go package paths to internal directories. A Go import is
// a full module path (e.g. "project-synapse/backend/internal/store"); we match
// the longest trailing segment-run that is a known directory and point the edge
// at a representative .go file in that package. Anything unmatched is external
// (stdlib or third-party).
func resolveGoImports(fa *FileAnalysis, ix *ResolveIndex) {
	for i := range fa.Imports {
		if file, ok := ix.matchGoPackage(fa.Imports[i].Specifier); ok {
			fa.Imports[i].Resolved = file
			fa.Imports[i].External = false
			fa.Imports[i].ResolvedOK = true
		} else {
			fa.Imports[i].Resolved = pkgBase(fa.Imports[i].Specifier)
			fa.Imports[i].External = true
			fa.Imports[i].ResolvedOK = false
		}
	}
}

func (ix *ResolveIndex) matchGoPackage(importPath string) (file string, ok bool) {
	segs := strings.Split(strings.Trim(importPath, "/"), "/")
	for i := 0; i < len(segs); i++ {
		cand := strings.Join(segs[i:], "/")
		if files, exists := ix.dirs[cand]; exists {
			if rep := pickGoRep(files); rep != "" {
				return rep, true
			}
		}
	}
	return "", false
}

func pickGoRep(files []string) string {
	for _, f := range files {
		if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			return f
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			return f
		}
	}
	return ""
}

// resolveRustImports resolves `mod foo;` (sibling module file) and crate/self/
// super-relative `use` paths to files; external crates are marked external.
func resolveRustImports(fa *FileAnalysis, ix *ResolveIndex) {
	dir := path.Dir(fa.RelPath)
	if dir == "." {
		dir = ""
	}
	crateRoot := ix.rustCrateRoot()
	for i := range fa.Imports {
		if file, ok := ix.resolveRustPath(fa.Imports[i].Specifier, dir, crateRoot); ok {
			fa.Imports[i].Resolved = file
			fa.Imports[i].External = false
			fa.Imports[i].ResolvedOK = true
		} else {
			fa.Imports[i].Resolved = rustCrateName(fa.Imports[i].Specifier)
			fa.Imports[i].External = true
			fa.Imports[i].ResolvedOK = false
		}
	}
}

// rustCrateRoot returns the crate's source root directory ("src" when a
// src/lib.rs or src/main.rs exists, else "").
func (ix *ResolveIndex) rustCrateRoot() string {
	if ix.Files["src/lib.rs"] || ix.Files["src/main.rs"] {
		return "src"
	}
	if ix.Files["lib.rs"] || ix.Files["main.rs"] {
		return ""
	}
	return "src"
}

func (ix *ResolveIndex) resolveRustPath(spec, dir, crateRoot string) (string, bool) {
	// `mod foo;` declares a sibling/child module file.
	if strings.HasPrefix(spec, "mod:") {
		name := strings.TrimPrefix(spec, "mod:")
		for _, c := range []string{joinRel(dir, name+".rs"), joinRel(dir, name+"/mod.rs")} {
			if ix.Files[c] {
				return c, true
			}
		}
		return "", false
	}

	segs := strings.Split(spec, "::")
	if len(segs) == 0 {
		return "", false
	}
	var base string
	switch segs[0] {
	case "crate":
		base = crateRoot
	case "self":
		base = dir
	case "super":
		base = path.Dir(dir)
		if base == "." {
			base = ""
		}
	default:
		return "", false // external crate
	}
	rest := segs[1:]
	// Try progressively shorter module paths, dropping the trailing item name.
	for k := len(rest); k >= 1; k-- {
		mp := strings.Join(rest[:k], "/")
		for _, c := range []string{joinRel(base, mp+".rs"), joinRel(base, mp+"/mod.rs")} {
			if ix.Files[c] {
				return c, true
			}
		}
	}
	// `crate::Item` referencing the crate root file directly.
	for _, c := range []string{joinRel(base, "mod.rs"), joinRel(base, "lib.rs"), joinRel(base, "main.rs")} {
		if ix.Files[c] {
			return c, true
		}
	}
	return "", false
}

// rustCrateName returns the external crate name a path belongs to (its head
// segment), used as the external-edge label.
func rustCrateName(spec string) string {
	spec = strings.TrimPrefix(spec, "mod:")
	if i := strings.Index(spec, "::"); i >= 0 {
		return spec[:i]
	}
	return spec
}

// resolvePyImports resolves Python `import`/`from` specifiers. Relative specifiers
// (leading dots) resolve against the importing file's package; absolute dotted
// paths resolve against the discovered source roots. Anything unmatched is an
// external dependency (stdlib or a pip package).
func resolvePyImports(fa *FileAnalysis, ix *ResolveIndex) {
	dir := path.Dir(fa.RelPath)
	if dir == "." {
		dir = ""
	}
	for i := range fa.Imports {
		spec := fa.Imports[i].Specifier
		if strings.HasPrefix(spec, ".") {
			if file, ok := ix.resolvePyRelative(spec, dir); ok {
				fa.Imports[i].Resolved = file
				fa.Imports[i].External = false
				fa.Imports[i].ResolvedOK = true
			} else {
				// A relative import that points outside the ingested set: internal
				// but unresolved (mirrors the TS/JS internal-unresolved classification).
				fa.Imports[i].Resolved = strings.TrimLeft(spec, ".")
				fa.Imports[i].External = false
				fa.Imports[i].ResolvedOK = false
			}
			continue
		}
		if file, ok := ix.resolvePyAbsolute(spec); ok {
			fa.Imports[i].Resolved = file
			fa.Imports[i].External = false
			fa.Imports[i].ResolvedOK = true
		} else {
			fa.Imports[i].Resolved = pyTopName(spec)
			fa.Imports[i].External = true
			fa.Imports[i].ResolvedOK = false
		}
	}
	fa.Imports = append(fa.Imports, ix.pySubmoduleEdges(fa, dir)...)
}

// pySubmoduleEdges recovers the dependency hidden inside `from <package> import
// <name>`: in Python the imported name may be a SUBMODULE rather than a symbol,
// e.g. `from src.controllers import service` depends on src/controllers/service.py,
// not merely on the package's __init__.py. Statically the two are indistinguishable
// without the file set, so we resolve each imported name against it and emit an
// extra edge for every one that turns out to be a real module.
//
// Without this, a package's submodules look unimported and cascade into large
// phantom "dead cluster" findings.
func (ix *ResolveIndex) pySubmoduleEdges(fa *FileAnalysis, dir string) []ImportRef {
	var extra []ImportRef
	seen := map[string]bool{}
	for _, imp := range fa.Imports {
		// Only `from X import a, b` can hide a submodule. The parser already
		// expands `from . import a` into `.a`, so those carry no symbols to test.
		if imp.Kind != "from" || imp.Specifier == "" {
			continue
		}
		base := strings.TrimLeft(imp.Specifier, ".")
		if base == "" {
			continue // `from . import x` — already a submodule specifier
		}
		for _, sym := range imp.Symbols {
			if sym == "" || sym == "*" {
				continue
			}
			sub := imp.Specifier + "." + sym
			file, ok := ix.resolvePySpec(sub, dir)
			if !ok || file == fa.RelPath || file == imp.Resolved || seen[file] {
				continue
			}
			seen[file] = true
			extra = append(extra, ImportRef{
				Specifier: sub, Symbols: []string{sym}, Kind: "from", Line: imp.Line,
				Deferred: imp.Deferred, Resolved: file, External: false, ResolvedOK: true,
			})
		}
	}
	return extra
}

// resolvePySpec resolves a specifier that may be relative (leading dots) or an
// absolute dotted module path.
func (ix *ResolveIndex) resolvePySpec(spec, dir string) (string, bool) {
	if strings.HasPrefix(spec, ".") {
		return ix.resolvePyRelative(spec, dir)
	}
	return ix.resolvePyAbsolute(spec)
}

// resolvePyRelative resolves a leading-dot specifier (`.mod`, `..pkg.mod`, `.`)
// against the importing file's directory.
func (ix *ResolveIndex) resolvePyRelative(spec, dir string) (string, bool) {
	dots := 0
	for dots < len(spec) && spec[dots] == '.' {
		dots++
	}
	base := dir
	// One leading dot = current package; each extra dot climbs one level.
	for k := 1; k < dots; k++ {
		base = path.Dir(base)
		if base == "." {
			base = ""
		}
	}
	rest := spec[dots:]
	if rest == "" {
		// `from . import x` collapsed to the package itself.
		if c := joinRel(base, "__init__.py"); ix.Files[c] {
			return c, true
		}
		return "", false
	}
	return ix.matchPyModule(base, rest)
}

// resolvePyAbsolute resolves a dotted module path against every source root.
func (ix *ResolveIndex) resolvePyAbsolute(spec string) (string, bool) {
	for _, root := range ix.pyRoots {
		if file, ok := ix.matchPyModule(root, spec); ok {
			return file, true
		}
	}
	return "", false
}

// matchPyModule maps a dotted module path (relative to base) to a module file or
// package __init__.py.
func (ix *ResolveIndex) matchPyModule(base, dotted string) (string, bool) {
	mp := strings.ReplaceAll(dotted, ".", "/")
	for _, c := range []string{joinRel(base, mp+".py"), joinRel(base, mp+"/__init__.py")} {
		if ix.Files[c] {
			return c, true
		}
	}
	return "", false
}

// pyTopName returns the top-level package a specifier belongs to, used as the
// external-edge label (`import os.path` -> "os"; `from django.db import x` -> "django").
func pyTopName(spec string) string {
	spec = strings.TrimLeft(spec, ".")
	if i := strings.IndexByte(spec, '.'); i >= 0 {
		return spec[:i]
	}
	if spec == "" {
		return "."
	}
	return spec
}

// dedupeStrings returns the unique values of xs, preserving first-seen order.
func dedupeStrings(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// joinRel joins relative path parts with forward slashes, treating "" as root.
func joinRel(dir, rest string) string {
	if dir == "" {
		return path.Clean(rest)
	}
	return path.Clean(dir + "/" + rest)
}

// resolveSpecifier classifies a single import specifier.
//
//	external=true               -> bare package; resolved = package root.
//	external=false, ok=true     -> relative import; resolved = target relpath.
//	external=false, ok=false    -> relative import that points outside the
//	                               ingest set; resolved = best-effort joined path.
func resolveSpecifier(fromDir, specifier string, known map[string]bool) (resolved string, external bool, ok bool) {
	if specifier == "" {
		return "", false, false
	}

	// Bare specifier => external module (npm package / Node builtin).
	if !strings.HasPrefix(specifier, ".") && !strings.HasPrefix(specifier, "/") {
		return packageRoot(specifier), true, false
	}

	joined := path.Clean(path.Join(fromDir, specifier))

	// 1. Exact match (specifier already carried an extension).
	if known[joined] {
		return joined, false, true
	}
	// 2. Append known source extensions.
	for _, ext := range resolveExtensions {
		if cand := joined + ext; known[cand] {
			return cand, false, true
		}
	}
	// 3. Directory import -> index file.
	for _, idx := range indexFiles {
		if cand := path.Join(joined, idx); known[cand] {
			return cand, false, true
		}
	}
	// Internal but unresolved (points outside the ingested set).
	return joined, false, false
}

// packageRoot reduces a bare specifier to its installable package name:
//
//	"react"             -> "react"
//	"react/jsx-runtime" -> "react"
//	"@scope/pkg/sub"    -> "@scope/pkg"
//	"node:fs"           -> "node:fs"
func packageRoot(specifier string) string {
	if strings.HasPrefix(specifier, "node:") {
		return specifier
	}
	parts := strings.Split(specifier, "/")
	if strings.HasPrefix(specifier, "@") {
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
		return specifier
	}
	return parts[0]
}
