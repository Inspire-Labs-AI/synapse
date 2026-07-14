// Rust language support for the ingestion pipeline.
//
// Rust has no Go-native AST library and tree-sitter would need a C toolchain, so
// this is a deliberately lightweight LEXICAL extractor rather than a full parser.
// It masks out comments/strings (so braces and keywords inside them don't fool
// it), then scans top-level + impl-member items by keyword, computing each
// item's line span with a brace/`;` matcher. It recovers the structure Synapse
// needs — items (fn/struct/enum/trait/impl/...), `use`/`mod` imports, and `pub`
// visibility — into the same FileAnalysis contract as every other language.
//
// It is heuristic by design: it favours never crashing and producing useful
// structure over perfect fidelity on exotic syntax.
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
	// An item declaration at the start of a line (any indentation): optional
	// visibility, optional modifiers, then a keyword and (usually) a name.
	reRustItem = regexp.MustCompile(
		`(?m)^[ \t]*(pub(?:\s*\([^)]*\))?\s+)?((?:async|unsafe|const|default|extern(?:\s+"[^"]*")?)\s+)*(fn|struct|enum|trait|impl|mod|type|const|static|union)\b[ \t]*([A-Za-z_][A-Za-z0-9_]*)?`)
	reRustUse      = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\s*\([^)]*\))?\s+)?use\s+([^;]+);`)
	reRustExtern   = regexp.MustCompile(`(?m)^[ \t]*(?:pub\s+)?extern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)`)
	reRustModDecl  = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\s*\([^)]*\))?\s+)?mod\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)
	rustSpaceRunRe = regexp.MustCompile(`\s+`)
	reRustCall     = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

func parseRustFile(relPath string, content []byte) (*FileAnalysis, error) {
	rel := filepath.ToSlash(relPath)
	sum := sha256.Sum256(content)
	src := string(content)
	fa := &FileAnalysis{
		RelPath:      rel,
		Filename:     filepath.Base(rel),
		Language:     "rust",
		Content:      src,
		Hash:         hex.EncodeToString(sum[:]),
		SizeBytes:    int64(len(content)),
		Imports:      []ImportRef{},
		Exports:      []ExportRef{},
		Endpoints:    []EndpointRef{},
		Declarations: []Declaration{},
		Calls:        []CallEdge{},
	}

	masked := rustMask(src)
	lineStarts := lineStartOffsets(masked)
	lineAt := func(off int) int { return lineForOffset(lineStarts, off) }
	depth := bracePrefixDepth(masked) // depth BEFORE each byte offset

	// --- Imports: use / extern crate / mod foo; -------------------------------
	for _, m := range reRustUse.FindAllStringSubmatchIndex(masked, -1) {
		raw := strings.TrimSpace(masked[m[2]:m[3]])
		base := rustUseBase(raw)
		if base == "" {
			continue
		}
		fa.Imports = append(fa.Imports, ImportRef{
			Specifier: base, Symbols: rustUseSymbols(raw), Kind: "use", Line: lineAt(m[0]),
		})
	}
	for _, m := range reRustExtern.FindAllStringSubmatchIndex(masked, -1) {
		name := masked[m[2]:m[3]]
		fa.Imports = append(fa.Imports, ImportRef{
			Specifier: name, Symbols: []string{name}, Kind: "extern", Line: lineAt(m[0]),
		})
	}
	for _, m := range reRustModDecl.FindAllStringSubmatchIndex(masked, -1) {
		name := masked[m[2]:m[3]]
		fa.Imports = append(fa.Imports, ImportRef{
			Specifier: "mod:" + name, Symbols: []string{name}, Kind: "mod", Line: lineAt(m[0]),
		})
	}

	// --- Items: fn / struct / enum / trait / impl / mod / type / const / ... ---
	seen := map[int]bool{}
	for _, m := range reRustItem.FindAllStringSubmatchIndex(masked, -1) {
		startOff := m[0]
		// Trim leading whitespace from the match so depth is measured at the keyword.
		kwOff := m[6]
		if kwOff < 0 || kwOff >= len(depth) {
			continue
		}
		d := depth[kwOff]
		if d > 1 {
			continue // skip bodies nested deeper than impl/trait members
		}
		if seen[kwOff] {
			continue
		}
		seen[kwOff] = true

		pub := m[2] >= 0
		keyword := masked[m[6]:m[7]]
		name := ""
		if m[8] >= 0 {
			name = masked[m[8]:m[9]]
		}

		// Find the item's span: body `{...}` or a terminating `;`.
		endOff := rustItemEnd(masked, m[7])
		startLine, endLine := lineAt(startOff), lineAt(endOff)

		kind := rustKind(keyword, d)
		header := masked[m[6]:bodyHeadEnd(masked, m[7], endOff)]
		if keyword == "impl" {
			name = "impl " + rustImplName(header)
		}
		if name == "" {
			name = keyword
		}

		patterns := []string{}
		if strings.Contains(header, "<") {
			patterns = append(patterns, "generic_wrapper")
		}

		fa.Declarations = append(fa.Declarations, Declaration{
			Name: name, Kind: kind, StartLine: startLine, EndLine: endLine, Exported: pub,
		})
		if pub {
			fa.Exports = append(fa.Exports, ExportRef{Name: name, Kind: kind, Line: startLine, Patterns: patterns})
		}
	}

	sort.SliceStable(fa.Declarations, func(i, j int) bool {
		return fa.Declarations[i].StartLine < fa.Declarations[j].StartLine
	})

	// --- Intra-file call graph (best-effort lexical) ---------------------------
	callable := map[string]bool{}
	for _, d := range fa.Declarations {
		if d.Kind == "function" || d.Kind == "method" {
			callable[d.Name] = true
		}
	}
	lineText := func(start, end int) string {
		if start < 1 {
			start = 1
		}
		s := lineStarts[start-1]
		e := len(masked)
		if end < len(lineStarts) {
			e = lineStarts[end]
		}
		if s > len(masked) {
			s = len(masked)
		}
		return masked[s:e]
	}
	callSeen := map[string]bool{}
	for _, d := range fa.Declarations {
		if d.Kind != "function" && d.Kind != "method" {
			continue
		}
		for _, m := range reRustCall.FindAllStringSubmatch(lineText(d.StartLine, d.EndLine), -1) {
			callee := m[1]
			if callee == d.Name || !callable[callee] {
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

// rustKind maps a keyword (+ nesting depth) to a chunk/declaration kind.
func rustKind(keyword string, depth int) string {
	switch keyword {
	case "fn":
		if depth > 0 {
			return "method"
		}
		return "function"
	case "mod":
		return "module"
	default:
		return keyword // struct | enum | trait | impl | type | const | static | union
	}
}

// rustItemEnd returns the byte offset of the end of an item that begins right
// after `from`: the matching `}` if the item has a body, else the terminating `;`.
func rustItemEnd(masked string, from int) int {
	i := from
	for i < len(masked) {
		switch masked[i] {
		case '{':
			return matchBrace(masked, i)
		case ';':
			return i
		}
		i++
	}
	return len(masked) - 1
}

// bodyHeadEnd returns the offset of the item's opening `{` (or its `;`/end),
// so the "header" slice excludes the body — used for impl-name + generics checks.
func bodyHeadEnd(masked string, from, end int) int {
	for i := from; i <= end && i < len(masked); i++ {
		if masked[i] == '{' || masked[i] == ';' {
			return i
		}
	}
	if end < len(masked) {
		return end
	}
	return len(masked)
}

// matchBrace returns the offset of the `}` matching the `{` at open.
func matchBrace(masked string, open int) int {
	depth := 0
	for i := open; i < len(masked); i++ {
		switch masked[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(masked) - 1
}

// rustImplName extracts a readable target from an impl header, e.g.
// "impl<T> Trait for Foo<T>" -> "Trait for Foo", "impl Bar" -> "Bar".
func rustImplName(header string) string {
	h := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "impl"))
	h = stripAngleGenerics(h)
	h = rustSpaceRunRe.ReplaceAllString(h, " ")
	h = strings.TrimSpace(h)
	if len(h) > 60 {
		h = h[:60]
	}
	return h
}

// stripAngleGenerics removes a leading <...> generic-parameter list.
func stripAngleGenerics(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "<") {
		return s
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[i+1:])
			}
		}
	}
	return s
}

// rustUseBase reduces a use-tree to a single module path:
//
//	"crate::a::b"            -> "crate::a::b"
//	"crate::a::{b, c}"       -> "crate::a"
//	"std::collections::HashMap as Map" -> "std::collections::HashMap"
func rustUseBase(use string) string {
	use = strings.TrimSpace(use)
	if i := strings.IndexByte(use, '{'); i >= 0 {
		use = use[:i]
	}
	if i := strings.Index(use, " as "); i >= 0 {
		use = use[:i]
	}
	use = strings.TrimSpace(use)
	use = strings.TrimRight(use, ": \t")
	return use
}

// rustUseSymbols lists the leaf symbols a use-statement brings in.
func rustUseSymbols(use string) []string {
	use = strings.TrimSpace(use)
	if i := strings.IndexByte(use, '{'); i >= 0 {
		inner := use[i+1:]
		if j := strings.LastIndexByte(inner, '}'); j >= 0 {
			inner = inner[:j]
		}
		var out []string
		for _, part := range strings.Split(inner, ",") {
			p := strings.TrimSpace(part)
			if p == "" || p == "self" {
				continue
			}
			if k := strings.Index(p, " as "); k >= 0 {
				p = strings.TrimSpace(p[k+4:])
			}
			if k := strings.LastIndex(p, "::"); k >= 0 {
				p = p[k+2:]
			}
			out = append(out, p)
		}
		return out
	}
	leaf := use
	if k := strings.Index(leaf, " as "); k >= 0 {
		leaf = strings.TrimSpace(leaf[k+4:])
	}
	if k := strings.LastIndex(leaf, "::"); k >= 0 {
		leaf = leaf[k+2:]
	}
	leaf = strings.TrimSpace(leaf)
	if leaf == "" || leaf == "*" {
		return []string{}
	}
	return []string{leaf}
}

// rustMask returns a copy of src with the CONTENTS of comments, strings, char
// literals and raw strings replaced by spaces (newlines preserved). Byte offsets
// and line numbers are unchanged, so the original can still be sliced for names.
func rustMask(src string) string {
	b := []byte(src)
	out := make([]byte, len(b))
	copy(out, b)
	blank := func(i int) {
		if b[i] != '\n' {
			out[i] = ' '
		}
	}
	i, n := 0, len(b)
	for i < n {
		c := b[i]
		switch {
		case c == '/' && i+1 < n && b[i+1] == '/':
			for i < n && b[i] != '\n' {
				blank(i)
				i++
			}
		case c == '/' && i+1 < n && b[i+1] == '*':
			depth := 1
			blank(i)
			blank(i + 1)
			i += 2
			for i < n && depth > 0 {
				if b[i] == '/' && i+1 < n && b[i+1] == '*' {
					depth++
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				if b[i] == '*' && i+1 < n && b[i+1] == '/' {
					depth--
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				blank(i)
				i++
			}
		case c == 'r' && (i+1 < n && (b[i+1] == '"' || b[i+1] == '#')):
			// Raw string: r"...", r#"..."#, r##"..."## ...
			j := i + 1
			hashes := 0
			for j < n && b[j] == '#' {
				hashes++
				j++
			}
			if j < n && b[j] == '"' {
				blank(i)
				for k := i + 1; k <= j; k++ {
					blank(k)
				}
				j++ // past opening quote
				for j < n {
					if b[j] == '"' {
						ok := true
						for h := 1; h <= hashes; h++ {
							if j+h >= n || b[j+h] != '#' {
								ok = false
								break
							}
						}
						if ok {
							for k := 0; k <= hashes; k++ {
								blank(j + k)
							}
							j += hashes + 1
							break
						}
					}
					blank(j)
					j++
				}
				i = j
			} else {
				i++ // just an identifier starting with r
			}
		case c == '"':
			blank(i)
			i++
			for i < n {
				if b[i] == '\\' && i+1 < n {
					blank(i)
					blank(i + 1)
					i += 2
					continue
				}
				if b[i] == '"' {
					blank(i)
					i++
					break
				}
				blank(i)
				i++
			}
		case c == '\'':
			// Char literal vs lifetime: 'x' / '\n' is a literal; 'a (no closing
			// quote soon) is a lifetime — leave lifetimes untouched.
			if isRustCharLiteral(b, i) {
				j := i + 1
				blank(i)
				for j < n {
					if b[j] == '\\' && j+1 < n {
						blank(j)
						blank(j + 1)
						j += 2
						continue
					}
					blank(j)
					if b[j] == '\'' {
						j++
						break
					}
					j++
				}
				i = j
			} else {
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}

func isRustCharLiteral(b []byte, i int) bool {
	n := len(b)
	if i+1 >= n {
		return false
	}
	if b[i+1] == '\\' {
		return true // escape => char literal
	}
	// 'x'  -> literal ; 'ab.. -> lifetime
	return i+2 < n && b[i+2] == '\''
}

// --- offset / depth helpers -------------------------------------------------

func lineStartOffsets(s string) []int {
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func lineForOffset(starts []int, off int) int {
	lo, hi := 0, len(starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// bracePrefixDepth returns depth[i] = brace nesting depth BEFORE byte i.
func bracePrefixDepth(masked string) []int {
	depth := make([]int, len(masked)+1)
	d := 0
	for i := 0; i < len(masked); i++ {
		depth[i] = d
		switch masked[i] {
		case '{':
			d++
		case '}':
			if d > 0 {
				d--
			}
		}
	}
	depth[len(masked)] = d
	return depth
}
