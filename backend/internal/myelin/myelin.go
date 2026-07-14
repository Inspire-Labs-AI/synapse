// Package myelin ingests markdown ("human docs") into the same model as code:
// each markdown file becomes a code_files row (language "markdown") with its
// internal links as edges, and each header section becomes a "myelin_doc"
// vector chunk — so human text blends into hybrid-RAG search (Myelin Insulation).
package myelin

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"

	"project-synapse/backend/internal/chunk"
	"project-synapse/backend/internal/parser"
)

var (
	headerRe = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+?)\s*#*$`)
	linkRe   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// IsMarkdown reports whether a path is a markdown file.
func IsMarkdown(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".md", ".markdown", ".mdx":
		return true
	}
	return false
}

// Analyze builds a FileAnalysis for a markdown file: language "markdown",
// internal links as import edges, and header sections recorded as declarations.
func Analyze(relPath, content string) *parser.FileAnalysis {
	rel := filepath.ToSlash(relPath)
	sum := sha256.Sum256([]byte(content))
	fa := &parser.FileAnalysis{
		RelPath:   rel,
		Filename:  filepath.Base(rel),
		Language:  "markdown",
		Content:   content,
		Hash:      hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)),
	}

	// Internal links → "markdown-link" import edges (skip external / anchors).
	seen := map[string]bool{}
	for _, m := range linkRe.FindAllStringSubmatch(content, -1) {
		text, href := m[1], strings.TrimSpace(m[2])
		lower := strings.ToLower(href)
		if href == "" || strings.HasPrefix(href, "#") ||
			strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "mailto:") {
			continue
		}
		clean := href
		if i := strings.IndexAny(clean, "#?"); i >= 0 {
			clean = clean[:i]
		}
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		fa.Imports = append(fa.Imports, parser.ImportRef{
			Specifier: clean,
			Symbols:   []string{text},
			Kind:      "markdown-link",
		})
	}

	for _, s := range sections(content) {
		fa.Declarations = append(fa.Declarations, parser.Declaration{
			Name:      s.title,
			Kind:      "myelin_doc",
			StartLine: s.startLine,
			EndLine:   s.endLine,
			Exported:  true,
		})
	}
	return fa
}

// Chunk turns a markdown file into myelin_doc chunks — one per header section,
// each embedding its document + section context plus the section body.
func Chunk(fa *parser.FileAnalysis) []chunk.Chunk {
	secs := sections(fa.Content)
	out := make([]chunk.Chunk, 0, len(secs))
	for _, s := range secs {
		body := strings.TrimSpace(s.body)
		if body == "" {
			continue
		}
		text := "Document: " + fa.RelPath + "\nSection: " + s.title + "\n\n" + body
		out = append(out, chunk.Chunk{
			SymbolName: s.title,
			ChunkType:  "myelin_doc",
			StartLine:  s.startLine,
			EndLine:    s.endLine,
			Code:       body,
			Text:       text,
		})
	}
	return out
}

type section struct {
	title     string
	startLine int
	endLine   int
	body      string
}

// sections splits markdown by ATX headers; each section runs from a header to
// the next header (or EOF). Content before the first header is a "Preamble".
func sections(content string) []section {
	lines := strings.Split(content, "\n")
	var out []section
	var cur *section
	flush := func(end int) {
		if cur != nil && end >= cur.startLine {
			cur.endLine = end
			cur.body = strings.Join(lines[cur.startLine-1:end], "\n")
			out = append(out, *cur)
		}
	}
	for i, line := range lines {
		ln := i + 1
		if m := headerRe.FindStringSubmatch(line); m != nil {
			flush(i) // previous section ends on the line before this header
			cur = &section{title: strings.TrimSpace(m[2]), startLine: ln}
		} else if cur == nil {
			cur = &section{title: "Preamble", startLine: ln}
		}
	}
	flush(len(lines))
	return out
}
