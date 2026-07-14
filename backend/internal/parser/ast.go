package parser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Parser is the structural-extraction contract for the ingestion pipeline.
type Parser interface {
	Parse(ctx context.Context, relPath string, content []byte) (*FileAnalysis, error)
}

// NodeParser shells out to the Node.js TypeScript-AST extractor (tools/tsparser/
// parse.mjs). It sends one file's source over stdin as JSON and reads back the
// structured imports/exports/endpoints — a true compiler-grade parse, not a
// regex approximation. Running per file lets the ingestion worker pool parse
// many files concurrently.
type NodeParser struct {
	NodeBin    string // node executable (default "node")
	ScriptPath string // absolute path to parse.mjs
	WorkDir    string // cwd for node so it resolves its local typescript install
}

// NewNodeParser builds a parser rooted at parserDir (the tools/tsparser folder).
func NewNodeParser(parserDir, nodeBin string) *NodeParser {
	if nodeBin == "" {
		nodeBin = "node"
	}
	absDir, err := filepath.Abs(parserDir)
	if err != nil {
		absDir = parserDir
	}
	return &NodeParser{
		NodeBin:    nodeBin,
		ScriptPath: filepath.Join(absDir, "parse.mjs"),
		WorkDir:    absDir,
	}
}

// --- wire format mirrored from parse.mjs ------------------------------------

type nodeRequest struct {
	Files []nodeFile `json:"files"`
}

type nodeFile struct {
	RelPath string `json:"relPath"`
	Source  string `json:"source"`
}

type nodeResponse struct {
	Results []nodeFileResult `json:"results"`
	Error   string           `json:"error,omitempty"`
}

type nodeFileResult struct {
	RelPath      string        `json:"relPath"`
	Imports      []ImportRef   `json:"imports"`
	Exports      []ExportRef   `json:"exports"`
	Endpoints    []EndpointRef `json:"endpoints"`
	Declarations []Declaration `json:"declarations"`
	Calls        []CallEdge    `json:"calls"`
	Error        string        `json:"error,omitempty"`
}

// Parse extracts structure for a single file.
func (p *NodeParser) Parse(ctx context.Context, relPath string, content []byte) (*FileAnalysis, error) {
	rel := filepath.ToSlash(relPath)

	reqBytes, err := json.Marshal(nodeRequest{
		Files: []nodeFile{{RelPath: rel, Source: string(content)}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	cmd := exec.CommandContext(ctx, p.NodeBin, p.ScriptPath)
	cmd.Dir = p.WorkDir
	cmd.Stdin = bytes.NewReader(reqBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run node parser: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	var resp nodeResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("decode node output: %w (raw: %.200s)", err, stdout.String())
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("node parser error: %s", resp.Error)
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("node parser returned no results for %s", rel)
	}

	res := resp.Results[0]
	sum := sha256.Sum256(content)

	fa := &FileAnalysis{
		RelPath:   rel,
		Filename:  filepath.Base(rel),
		Language:  detectLanguage(rel),
		Content:   string(content),
		Hash:      hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)),
		Imports:      res.Imports,
		Exports:      res.Exports,
		Endpoints:    res.Endpoints,
		Declarations: res.Declarations,
		Calls:        res.Calls,
	}
	// A per-file parse error is non-fatal: keep the file node, drop its edges.
	if res.Error != "" {
		fa.Imports, fa.Exports, fa.Endpoints, fa.Declarations, fa.Calls = nil, nil, nil, nil, nil
		return fa, fmt.Errorf("parse %s: %s", rel, res.Error)
	}
	return fa, nil
}

// Verify checks that the node binary and parser script are usable. Call once at
// startup to fail fast with a clear message instead of per-file errors.
func (p *NodeParser) Verify(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.NodeBin, "--version")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("node not runnable (%q): %w (%s)", p.NodeBin, err, strings.TrimSpace(string(out)))
	}
	// A trivial round-trip also proves the typescript dependency resolves.
	if _, err := p.Parse(ctx, "verify.ts", []byte("export const ok = true;")); err != nil {
		return fmt.Errorf("parser self-check failed (is %q present with typescript installed?): %w", p.ScriptPath, err)
	}
	return nil
}

// detectLanguage maps a file extension to a coarse language label.
func detectLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescript-react"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascript-react"
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	default:
		return "unknown"
	}
}
