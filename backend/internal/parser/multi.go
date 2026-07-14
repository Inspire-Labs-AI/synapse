package parser

import (
	"context"
	"path/filepath"
	"strings"
)

// MultiParser routes each file to the right language extractor:
//   - .ts/.tsx/.js/.jsx  -> the Node tsc-AST subprocess (delegated)
//   - .go                -> in-process go/parser AST
//   - .rs                -> in-process lexical Rust scanner
//   - .py                -> in-process lexical Python scanner
//
// It satisfies the same Parser interface, so the ingestion pipeline is unaware
// of the language split.
type MultiParser struct {
	Node Parser // TypeScript/JavaScript (and the fallback for anything else)
}

// NewMultiParser wraps the TS/JS parser with Go + Rust dispatch.
func NewMultiParser(node Parser) *MultiParser {
	return &MultiParser{Node: node}
}

// Parse dispatches by file extension.
func (m *MultiParser) Parse(ctx context.Context, relPath string, content []byte) (*FileAnalysis, error) {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".go":
		return parseGoFile(relPath, content)
	case ".rs":
		return parseRustFile(relPath, content)
	case ".py":
		return parsePythonFile(relPath, content)
	default:
		return m.Node.Parse(ctx, relPath, content)
	}
}
