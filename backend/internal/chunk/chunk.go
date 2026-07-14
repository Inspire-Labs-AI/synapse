// Package chunk turns a parsed file into semantic, structurally-bounded chunks
// ready for embedding. Instead of splitting on arbitrary character counts, it
// groups code by the structural boundaries discovered during parsing
// (functions, classes, interfaces, ...). Each chunk is prefixed with a plain
// text structural header so the embedding captures file + symbol + dependency
// context, not just the raw code.
package chunk

import (
	"fmt"
	"strings"

	"project-synapse/backend/internal/parser"
)

const (
	// maxLines bounds a single chunk; larger declarations are windowed. Sized to
	// keep whole functions/classes intact for modern embedders (OpenAI 8k tokens,
	// Jina/Voyage code models 8k–32k) — a single coherent symbol retrieves far
	// better than fragments. Only genuinely huge declarations split. (The
	// embedders apply their own per-input char cap on top of this.)
	maxLines = 200
	// overlapLines preserves context across split windows of a large decl.
	overlapLines = 20
	// maxChunksPerFile bounds a single file's embedding cost (a slip-through guard
	// for large files that survive the walker's size/minified filter).
	maxChunksPerFile = 120
)

// Chunk is one embeddable unit.
type Chunk struct {
	SymbolName string // e.g. "UserController" or "UserController#part2"
	ChunkType  string // function | class | interface | type | enum | variable | file
	StartLine  int
	EndLine    int
	Code       string // raw source slice
	Text       string // structural header + code — this is what gets embedded
}

// File produces the chunks for a single analysed file.
func File(fa *parser.FileAnalysis) []Chunk {
	lines := strings.Split(fa.Content, "\n")
	deps := importDescriptors(fa)

	var chunks []Chunk

	if len(fa.Declarations) == 0 {
		// No top-level declarations — embed the whole file as one chunk so it
		// still participates in semantic search.
		end := len(lines)
		chunks = append(chunks, makeChunk(fa.RelPath, fa.Filename, "file", 1, end, lines, deps))
		return chunks
	}

	for _, d := range fa.Declarations {
		start, end := d.StartLine, d.EndLine
		if start < 1 {
			start = 1
		}
		if end < start {
			end = start
		}

		span := end - start + 1
		if span <= maxLines {
			chunks = append(chunks, makeChunk(fa.RelPath, d.Name, d.Kind, start, end, lines, deps))
			continue
		}

		// Window large declarations into <= maxLines slices with light overlap.
		part := 1
		for s := start; s <= end; s += (maxLines - overlapLines) {
			e := s + maxLines - 1
			if e > end {
				e = end
			}
			name := fmt.Sprintf("%s#part%d", d.Name, part)
			chunks = append(chunks, makeChunk(fa.RelPath, name, d.Kind, s, e, lines, deps))
			part++
			if e == end {
				break
			}
		}
	}

	if len(chunks) > maxChunksPerFile {
		chunks = chunks[:maxChunksPerFile]
	}
	return chunks
}

// makeChunk slices the [start,end] line range (1-based, inclusive) and builds
// the chunk with its structural header.
func makeChunk(relPath, symbol, kind string, start, end int, lines []string, deps []string) Chunk {
	code := sliceLines(lines, start, end)
	header := buildHeader(relPath, kind, symbol, deps)
	return Chunk{
		SymbolName: symbol,
		ChunkType:  kind,
		StartLine:  start,
		EndLine:    end,
		Code:       code,
		Text:       header + "\n" + code,
	}
}

// WithPurpose injects an LLM-written one-line "Purpose" into a chunk's structural
// header (before the closing delimiter), so it becomes part of the embedded text
// while still being stripped from the displayed code (store.StripChunkHeader).
func WithPurpose(text, purpose string) string {
	purpose = strings.Join(strings.Fields(purpose), " ")
	if purpose == "" {
		return text
	}
	const marker = "\n---\n" // closing header delimiter, just before the code
	if i := strings.Index(text, marker); i >= 0 {
		return text[:i] + "\nPurpose: " + purpose + marker + text[i+len(marker):]
	}
	return "Purpose: " + purpose + "\n" + text
}

// buildHeader renders the mandated structural header.
func buildHeader(relPath, kind, symbol string, deps []string) string {
	depStr := "none"
	if len(deps) > 0 {
		depStr = strings.Join(deps, ", ")
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("File: " + relPath + "\n")
	b.WriteString("Context: " + kind + " " + symbol + "\n")
	b.WriteString("Imports/Dependencies: " + depStr + "\n")
	b.WriteString("---")
	return b.String()
}

// sliceLines returns the inclusive 1-based line range from lines.
func sliceLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// importDescriptors collects a unique, human-readable list of the file's
// dependencies for the header: resolved relative paths for internal imports,
// package names for external ones.
func importDescriptors(fa *parser.FileAnalysis) []string {
	seen := make(map[string]bool)
	var out []string
	for _, imp := range fa.Imports {
		d := imp.Specifier
		if imp.Resolved != "" {
			d = imp.Resolved
		}
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
