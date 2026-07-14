// Python language support for the ingestion pipeline.
//
// Like Rust (and unlike TypeScript, which shells out to the Node tsc AST), Python
// is parsed IN-PROCESS with a deliberately lightweight LEXICAL extractor rather
// than a full parser: a Go-native Python AST would mean a new runtime dependency
// (a python3 subprocess) that could break ingestion on machines without Python,
// so we stay self-contained and dependency-free, consistent with the Rust choice.
//
// It masks out comments/strings (so `#`, `def`, quotes inside them don't fool it),
// then recovers the structure Synapse needs — `import`/`from` edges, top-level
// `def`/`class` + one level of class methods, module-level assignments, decorator
// idioms and Flask/FastAPI routes — into the same FileAnalysis contract as every
// other language. It is heuristic by design: it favours never crashing and
// producing useful structure over perfect fidelity on exotic syntax.
package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// A def/class header at any indentation: capture indent, optional async, the
	// keyword, and the name.
	rePyDefClass = regexp.MustCompile(`(?m)^([ \t]*)(?:(async)[ \t]+)?(def|class)[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)
	// A module-level (column 0) simple assignment: `NAME = ...` or `NAME: T = ...`.
	// The trailing negative lookahead-style `[^=]` avoids matching `==`/`>=` etc.;
	// we require a value after `=`.
	rePyAssign = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)[ \t]*(?::[^=\n]+)?=([^=].*)?$`)
	// `__all__ = [...]` / `(...)` — parsed from the ORIGINAL source (string
	// contents are blanked in the masked copy).
	rePyAll     = regexp.MustCompile(`(?ms)^__all__[ \t]*[:+]?=[ \t]*[\[(](.*?)[\])]`)
	rePyAllName = regexp.MustCompile(`["']([A-Za-z_][A-Za-z0-9_]*)["']`)
	// A route decorator: `@app.get("/x")`, `@router.post("/x")`, `@bp.route("/x", methods=[...])`.
	rePyRoute       = regexp.MustCompile(`^@[ \t]*[\w.]+\.(get|post|put|delete|patch|head|options|route|websocket)[ \t]*\([ \t]*(?:path[ \t]*=[ \t]*)?["']([^"']*)["']`)
	rePyRouteMethod = regexp.MustCompile(`methods[ \t]*=[ \t]*\[([^\]]*)\]`)
	rePyCall        = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`)
	pyKeywords      = map[string]bool{
		"if": true, "elif": true, "else": true, "for": true, "while": true,
		"with": true, "try": true, "except": true, "finally": true, "return": true,
		"yield": true, "raise": true, "assert": true, "pass": true, "break": true,
		"continue": true, "import": true, "from": true, "global": true, "del": true,
		"nonlocal": true, "lambda": true, "print": true, "and": true, "or": true,
		"not": true, "in": true, "is": true, "async": true, "await": true,
	}
)

func parsePythonFile(relPath string, content []byte) (*FileAnalysis, error) {
	rel := filepath.ToSlash(relPath)
	sum := sha256.Sum256(content)
	src := string(content)
	fa := &FileAnalysis{
		RelPath:      rel,
		Filename:     filepath.Base(rel),
		Language:     "python",
		Content:      src,
		Hash:         hex.EncodeToString(sum[:]),
		SizeBytes:    int64(len(content)),
		Imports:      []ImportRef{},
		Exports:      []ExportRef{},
		Endpoints:    []EndpointRef{},
		Declarations: []Declaration{},
		Calls:        []CallEdge{},
	}

	masked := pythonMask(src)
	lineStarts := lineStartOffsets(masked)
	nLines := len(lineStarts)
	lineAt := func(off int) int { return lineForOffset(lineStarts, off) }
	mLine := func(k int) string { // masked text of physical line k (1-based), no newline
		if k < 1 || k > nLines {
			return ""
		}
		s := lineStarts[k-1]
		e := len(masked)
		if k < nLines {
			e = lineStarts[k] - 1
		}
		if s > len(masked) {
			s = len(masked)
		}
		if e > len(masked) {
			e = len(masked)
		}
		return masked[s:e]
	}
	srcStarts := lineStartOffsets(src)
	srcLine := func(k int) string { // ORIGINAL text of physical line k
		if k < 1 || k > len(srcStarts) {
			return ""
		}
		s := srcStarts[k-1]
		e := len(src)
		if k < len(srcStarts) {
			e = srcStarts[k] - 1
		}
		return src[s:e]
	}

	// --- __all__ explicit public API (parsed from original source) -------------
	pyAll := map[string]bool{}
	hasAll := false
	if m := rePyAll.FindStringSubmatch(src); m != nil {
		hasAll = true
		for _, nm := range rePyAllName.FindAllStringSubmatch(m[1], -1) {
			pyAll[nm[1]] = true
		}
	}

	// --- Imports (import / from, over logical lines) --------------------------
	for _, ll := range pyLogicalLines(masked, lineStarts) {
		t := strings.TrimSpace(ll.text)
		switch {
		case strings.HasPrefix(t, "import ") || t == "import":
			for _, imp := range parsePyImport(strings.TrimSpace(t[len("import"):])) {
				imp.Line = ll.line
				fa.Imports = append(fa.Imports, imp)
			}
		case strings.HasPrefix(t, "from "):
			for _, imp := range parsePyFrom(t) {
				imp.Line = ll.line
				fa.Imports = append(fa.Imports, imp)
			}
		}
	}

	// --- Decorators: line -> original decorator text --------------------------
	decoratorLine := map[int]string{}
	for k := 1; k <= nLines; k++ {
		if strings.HasPrefix(strings.TrimSpace(mLine(k)), "@") {
			decoratorLine[k] = strings.TrimSpace(srcLine(k))
		}
	}
	// decoratorsAbove walks upward from a def/class line collecting contiguous
	// decorator lines (blank/comment lines are allowed between them and the def).
	decoratorsAbove := func(defLine int) []string {
		var out []string
		for k := defLine - 1; k >= 1; k-- {
			trimmed := strings.TrimSpace(mLine(k))
			if trimmed == "" {
				continue
			}
			if d, ok := decoratorLine[k]; ok {
				out = append(out, d)
				continue
			}
			break
		}
		return out
	}

	// blockEnd returns the last line of a suite whose header ends on `headerEnd`
	// (the line carrying the `:` that opens the body) at indentation `indent`.
	blockEnd := func(headerEnd, indent int) int {
		end := headerEnd
		for k := headerEnd + 1; k <= nLines; k++ {
			ls := mLine(k)
			if strings.TrimSpace(ls) == "" {
				continue // blank line: may be inside the block, don't terminate on it
			}
			if pyIndent(ls) > indent {
				end = k
			} else {
				break
			}
		}
		return end
	}

	// --- def / class declarations (top-level + one level of class methods) ----
	type scope struct {
		indent int
		kind   string // "class" | "def"
		name   string
	}
	var stack []scope
	matches := rePyDefClass.FindAllStringSubmatchIndex(masked, -1)
	for _, m := range matches {
		indent := m[3] - m[2] // len of leading-whitespace group
		keyword := masked[m[6]:m[7]]
		name := masked[m[8]:m[9]]
		startLine := lineAt(m[0])

		// Pop scopes we've dedented out of.
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		var enclosing *scope
		if len(stack) > 0 {
			enclosing = &stack[len(stack)-1]
		}

		// A def/class header can span several physical lines (a multi-line
		// signature); the body begins after the `:` that opens the suite.
		headerEnd := pySuiteColonLine(masked, lineStarts, m[0])
		endLine := blockEnd(headerEnd, indent)
		recordName := name
		recordKind := keyword // "def" -> normalized below; "class"
		record := true
		switch {
		case enclosing == nil:
			if keyword == "def" {
				recordKind = "function"
			} else {
				recordKind = "class"
			}
		case enclosing.kind == "class":
			if keyword == "def" {
				recordKind = "method"
				recordName = enclosing.name + "." + name
			} else {
				recordKind = "class"
				recordName = enclosing.name + "." + name
			}
		default: // nested inside a def -> a closure/local; not a top-level declaration
			record = false
		}

		if record {
			exported := false
			if enclosing == nil {
				exported = pyIsExported(name, hasAll, pyAll)
			}
			patterns := pyDeclPatterns(masked, lineStarts, srcLine(startLine), startLine, endLine, decoratorsAbove(startLine))
			fa.Declarations = append(fa.Declarations, Declaration{
				Name: recordName, Kind: recordKind, StartLine: startLine, EndLine: endLine, Exported: exported,
			})
			if exported {
				fa.Exports = append(fa.Exports, ExportRef{Name: recordName, Kind: recordKind, Line: startLine, Patterns: patterns})
			}
			// Endpoints from route decorators sit above this def.
			for _, dec := range decoratorsAbove(startLine) {
				pyCollectEndpoints(dec, name, lineAt(m[0]), &fa.Endpoints)
			}
		}

		// Push this def/class so deeper items are scoped correctly.
		pushKind := "def"
		pushName := recordName
		if keyword == "class" {
			pushKind = "class"
		}
		if !record { // nested def/class: still track for correct dedent scoping
			pushName = name
		}
		stack = append(stack, scope{indent: indent, kind: pushKind, name: pushName})
	}

	// --- Module-level assignments (column-0 constants / config) ----------------
	for _, m := range rePyAssign.FindAllStringSubmatchIndex(masked, -1) {
		name := masked[m[2]:m[3]]
		if pyKeywords[name] || name == "__all__" {
			continue // keywords never assign at col 0; __all__ is metadata, not a decl
		}
		startLine := lineAt(m[0])
		kind := "variable"
		if name == strings.ToUpper(name) && strings.ContainsAny(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			kind = "const"
		}
		exported := pyIsExported(name, hasAll, pyAll)
		if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
			exported = false // dunders aren't part of the public surface
		}
		fa.Declarations = append(fa.Declarations, Declaration{
			Name: name, Kind: kind, StartLine: startLine, EndLine: startLine, Exported: exported,
		})
		if exported {
			fa.Exports = append(fa.Exports, ExportRef{Name: name, Kind: kind, Line: startLine})
		}
	}

	sort.SliceStable(fa.Declarations, func(i, j int) bool {
		return fa.Declarations[i].StartLine < fa.Declarations[j].StartLine
	})

	// --- Deferred imports: an import inside a function/method body is Python's
	// standard cycle-breaking idiom — it runs lazily at call time, not at module
	// import time, so it must NOT be treated as a hard import-time dependency
	// (which would fabricate circular-dependency findings). Mark them distinctly.
	for i := range fa.Imports {
		ln := fa.Imports[i].Line
		for _, d := range fa.Declarations {
			if (d.Kind == "function" || d.Kind == "method") && ln > d.StartLine && ln <= d.EndLine {
				fa.Imports[i].Deferred = true
				break
			}
		}
	}

	// --- Intra-file call graph (best-effort lexical) --------------------------
	simpleToName := map[string]string{}
	for _, d := range fa.Declarations {
		if d.Kind == "function" || d.Kind == "method" {
			simple := d.Name
			if i := strings.LastIndexByte(simple, '.'); i >= 0 {
				simple = simple[i+1:]
			}
			if _, exists := simpleToName[simple]; !exists {
				simpleToName[simple] = d.Name
			}
		}
	}
	callSeen := map[string]bool{}
	for _, d := range fa.Declarations {
		if d.Kind != "function" && d.Kind != "method" {
			continue
		}
		selfSimple := d.Name
		if i := strings.LastIndexByte(selfSimple, '.'); i >= 0 {
			selfSimple = selfSimple[i+1:]
		}
		body := pyLineRange(masked, lineStarts, d.StartLine+1, d.EndLine)
		for _, cm := range rePyCall.FindAllStringSubmatch(body, -1) {
			callee, ok := simpleToName[cm[1]]
			if !ok || cm[1] == selfSimple || callee == d.Name {
				continue
			}
			k := d.Name + ">" + callee
			if callSeen[k] {
				continue
			}
			callSeen[k] = true
			fa.Calls = append(fa.Calls, CallEdge{Caller: d.Name, Callee: callee})
		}
	}

	return fa, nil
}

// pyIsExported applies Python's public-surface convention: if the module defines
// __all__, membership is authoritative; otherwise a leading underscore is private.
func pyIsExported(name string, hasAll bool, all map[string]bool) bool {
	if hasAll {
		return all[name]
	}
	return !strings.HasPrefix(name, "_")
}

// pyDeclPatterns flags Dendrite idioms on a def/class: decorators, generics
// (PEP 695 `def f[T]` / `class C[T]` / `Generic[...]` base) and closures
// (a function that nests another def).
func pyDeclPatterns(masked string, lineStarts []int, headerSrc string, start, end int, decorators []string) []string {
	var p []string
	if len(decorators) > 0 {
		p = append(p, "decorator")
	}
	// generics: `name[` in the header (PEP 695), or a Generic[...] base.
	if reHeaderGeneric.MatchString(headerSrc) {
		p = append(p, "generic_wrapper")
	}
	// closure: a nested `def` appears deeper than this header within its span.
	if end > start && reNestedDef.MatchString(pyLineRange(masked, lineStarts, start+1, end)) {
		p = append(p, "closure")
	}
	return p
}

var (
	reHeaderGeneric = regexp.MustCompile(`(?:def|class)[ \t]+[A-Za-z_]\w*[ \t]*\[|Generic\[`)
	reNestedDef     = regexp.MustCompile(`(?m)^[ \t]+(?:async[ \t]+)?def[ \t]`)
)

// pyCollectEndpoints extracts a Flask/FastAPI route from a decorator line.
func pyCollectEndpoints(decorator, handler string, line int, out *[]EndpointRef) {
	m := rePyRoute.FindStringSubmatch(decorator)
	if m == nil {
		return
	}
	verb, route := m[1], m[2]
	if !strings.HasPrefix(route, "/") {
		return
	}
	var methods []string
	switch verb {
	case "route":
		if mm := rePyRouteMethod.FindStringSubmatch(decorator); mm != nil {
			for _, part := range strings.Split(mm[1], ",") {
				p := strings.ToUpper(strings.Trim(strings.TrimSpace(part), `"'`))
				if p != "" {
					methods = append(methods, p)
				}
			}
		}
		if len(methods) == 0 {
			methods = []string{"GET"}
		}
	case "websocket":
		methods = []string{"WS"}
	default:
		methods = []string{strings.ToUpper(verb)}
	}
	for _, meth := range methods {
		*out = append(*out, EndpointRef{
			Method: meth, Path: route, Handler: handler, Source: "python-decorator", Line: line,
		})
	}
}

// parsePyImport handles the `import a, b.c as d` form (after the `import` keyword).
func parsePyImport(rest string) []ImportRef {
	var out []ImportRef
	for _, part := range strings.Split(rest, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		mod := p
		sym := ""
		if i := strings.Index(p, " as "); i >= 0 {
			mod = strings.TrimSpace(p[:i])
			sym = strings.TrimSpace(p[i+4:])
		}
		if mod == "" {
			continue
		}
		if sym == "" {
			sym = mod
			if j := strings.IndexByte(sym, '.'); j >= 0 {
				sym = sym[:j] // `import a.b.c` binds top name `a`
			}
		}
		out = append(out, ImportRef{Specifier: mod, Symbols: []string{sym}, Kind: "import"})
	}
	return out
}

// parsePyFrom handles `from <dots><mod> import <names>`. When the module part is
// empty (`from . import x, y`) each name is emitted as its own submodule edge.
func parsePyFrom(line string) []ImportRef {
	rest := strings.TrimSpace(line[len("from"):])
	dots := 0
	for dots < len(rest) && rest[dots] == '.' {
		dots++
	}
	after := rest[dots:]
	idx := strings.Index(after, "import")
	if idx < 0 {
		return nil
	}
	mod := strings.TrimSpace(after[:idx])
	names := strings.TrimSpace(after[idx+len("import"):])
	names = strings.TrimSpace(strings.Trim(strings.TrimSpace(names), "()"))
	prefix := strings.Repeat(".", dots)

	type nm struct{ raw, sym string }
	var parsed []nm
	for _, part := range strings.Split(names, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		raw, sym := p, p
		if i := strings.Index(p, " as "); i >= 0 {
			raw = strings.TrimSpace(p[:i])
			sym = strings.TrimSpace(p[i+4:])
		}
		parsed = append(parsed, nm{raw: raw, sym: sym})
	}

	if mod != "" {
		// Record the ORIGINAL names, not the local aliases: `from pkg import mod as m`
		// depends on `pkg.mod`, and `m` names nothing in the target.
		syms := make([]string, 0, len(parsed))
		for _, n := range parsed {
			if n.raw != "*" {
				syms = append(syms, n.raw)
			}
		}
		return []ImportRef{{Specifier: prefix + mod, Symbols: syms, Kind: "from"}}
	}
	// `from . import a, b` — each imported name is a submodule of the package.
	var out []ImportRef
	for _, n := range parsed {
		if n.raw == "*" {
			continue
		}
		out = append(out, ImportRef{Specifier: prefix + n.raw, Symbols: []string{n.sym}, Kind: "from"})
	}
	if len(out) == 0 { // `from . import *` — edge to the package itself
		out = append(out, ImportRef{Specifier: prefix, Symbols: []string{"*"}, Kind: "from"})
	}
	return out
}

// pyLogicalLine is a source line joined across `(`/`[`/`{` and backslash
// continuations, tagged with the physical line it starts on.
type pyLogicalLine struct {
	text string
	line int
}

// pyLogicalLines groups the masked source into logical lines (bracket + backslash
// continuations joined), so multiline `from x import (a, b, c)` is one unit.
func pyLogicalLines(masked string, lineStarts []int) []pyLogicalLine {
	var out []pyLogicalLine
	n := len(lineStarts)
	depth := 0
	cont := false
	var buf strings.Builder
	startLine := 0
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, pyLogicalLine{text: buf.String(), line: startLine})
			buf.Reset()
		}
	}
	for k := 1; k <= n; k++ {
		s := lineStarts[k-1]
		e := len(masked)
		if k < n {
			e = lineStarts[k] - 1
		}
		if s > len(masked) {
			s = len(masked)
		}
		if e > len(masked) {
			e = len(masked)
		}
		raw := masked[s:e]
		if depth == 0 && !cont {
			startLine = k
		}
		line := raw
		backslash := strings.HasSuffix(strings.TrimRight(line, " \t"), "\\")
		if backslash {
			line = strings.TrimRight(line, " \t")
			line = line[:len(line)-1]
		}
		buf.WriteString(line)
		buf.WriteByte(' ')
		for i := 0; i < len(raw); i++ {
			switch raw[i] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			}
		}
		cont = backslash
		if depth == 0 && !cont {
			flush()
		}
	}
	flush()
	return out
}

// pyLineRange returns the masked text spanning physical lines [from, to].
func pyLineRange(masked string, lineStarts []int, from, to int) string {
	n := len(lineStarts)
	if from < 1 {
		from = 1
	}
	if to > n {
		to = n
	}
	if from > to {
		return ""
	}
	s := lineStarts[from-1]
	e := len(masked)
	if to < n {
		e = lineStarts[to] - 1
	}
	if s > len(masked) {
		s = len(masked)
	}
	if e > len(masked) {
		e = len(masked)
	}
	return masked[s:e]
}

// pySuiteColonLine returns the physical line of the `:` that opens a def/class
// suite, scanning forward from the header at byte offset `from`. Colons inside
// brackets (parameter annotations, defaults, base lists) are skipped, so a
// multi-line signature is treated as part of the header — not the body. This is
// what keeps a function's span from being truncated at its signature.
func pySuiteColonLine(masked string, lineStarts []int, from int) int {
	depth := 0
	for i := from; i < len(masked); i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return lineForOffset(lineStarts, i)
			}
		}
	}
	return lineForOffset(lineStarts, from)
}

// pyIndent counts the leading whitespace (spaces/tabs) of a line.
func pyIndent(line string) int {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return i
}

// pythonMask returns a copy of src with the CONTENTS of `#` comments and string
// literals (single, double, and triple-quoted, incl. f/r/b prefixes) replaced by
// spaces. Newlines and byte offsets are preserved so the original can still be
// sliced for names and line numbers computed.
func pythonMask(src string) string {
	b := []byte(src)
	out := make([]byte, len(b))
	copy(out, b)
	blank := func(i int) {
		if b[i] != '\n' {
			out[i] = ' '
		}
	}
	isRaw := func(i int) bool { // is the quote at i part of a raw string?
		for j := i - 1; j >= 0 && i-j <= 2; j-- {
			c := b[j]
			if c == 'r' || c == 'R' {
				return true
			}
			if c == 'f' || c == 'F' || c == 'b' || c == 'B' || c == 'u' || c == 'U' {
				continue
			}
			break
		}
		return false
	}
	i, n := 0, len(b)
	for i < n {
		c := b[i]
		switch {
		case c == '#':
			for i < n && b[i] != '\n' {
				blank(i)
				i++
			}
		case c == '"' || c == '\'':
			raw := isRaw(i)
			triple := i+2 < n && b[i+1] == c && b[i+2] == c
			if triple {
				blank(i)
				blank(i + 1)
				blank(i + 2)
				i += 3
				for i < n {
					if !raw && b[i] == '\\' && i+1 < n {
						blank(i)
						blank(i + 1)
						i += 2
						continue
					}
					if b[i] == c && i+2 < n && b[i+1] == c && b[i+2] == c {
						blank(i)
						blank(i + 1)
						blank(i + 2)
						i += 3
						break
					}
					if b[i] == c && i+2 >= n { // closing triple at EOF
						blank(i)
						i++
						break
					}
					blank(i)
					i++
				}
			} else {
				blank(i)
				i++
				for i < n {
					if b[i] == '\n' {
						break // single-line string can't cross a newline (unmasked)
					}
					if !raw && b[i] == '\\' && i+1 < n {
						blank(i)
						blank(i + 1)
						i += 2
						continue
					}
					if b[i] == c {
						blank(i)
						i++
						break
					}
					blank(i)
					i++
				}
			}
		default:
			i++
		}
	}
	return string(out)
}
