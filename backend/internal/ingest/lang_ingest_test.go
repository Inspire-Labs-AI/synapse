package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"project-synapse/backend/internal/parser"
)

// TestPipelineParsesGoRustPython drives the full discover→dispatch→parse→resolve
// path (dry-run, no DB) over a mixed Go + Rust + Python + TS tree, confirming the
// walker picks up .go/.rs/.py and the MultiParser routes them without errors.
func TestPipelineParsesGoRustPython(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("main.go", "package main\nimport \"app/store\"\nfunc main() { store.Init() }\n")
	write("store/store.go", "package store\nfunc Init() {}\ntype DB struct{}\n")
	write("src/lib.rs", "pub mod auth;\npub fn run() {}\n")
	write("src/auth.rs", "pub struct Cred { pub user: String }\nimpl Cred { pub fn new() -> Self { Cred { user: String::new() } } }\n")
	write("app.ts", "export function hello() { return 1; }\n")
	// Python: a package with a relative import between two modules.
	write("pkg/__init__.py", "")
	write("pkg/service.py", "def db():\n    return 1\n\ndef handler():\n    return db()\n")
	write("pkg/routes.py", "from .service import handler\n\ndef main():\n    return handler()\n")
	// Build artefacts that must be skipped: Cargo target/ and Python __pycache__/.
	write("target/debug/junk.rs", "fn should_not_be_seen() {}\n")
	write("__pycache__/ghost.py", "def should_not_be_seen():\n    pass\n")

	// Node parser is unused here (no .ts file would actually need it in this
	// dry-run dispatch test for Go/Rust); supply a stub that errors if called so
	// a mis-route is caught. The .ts file does need it, so use the real one path:
	// instead we just assert Go/Rust counts and tolerate the TS route.
	pl := &Pipeline{
		Parser:  parser.NewMultiParser(stubNode{t}),
		Workers: 3,
	}

	stats, err := pl.Run(context.Background(), root)
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}

	// 8 source files discovered (target/ and __pycache__/ are skipped).
	if stats.FilesDiscovered != 8 {
		t.Errorf("discovered = %d, want 8", stats.FilesDiscovered)
	}
	// Go (2) + Rust (2) + TS (1) + Python (3) all parse cleanly.
	if stats.FilesParsed != 8 {
		t.Errorf("parsed = %d, want 8", stats.FilesParsed)
	}
	if stats.Errors != 0 {
		t.Errorf("errors = %d, want 0", stats.Errors)
	}
}

// stubNode stands in for the Node TS parser: it returns a minimal analysis so
// the TS file parses without a Node subprocess in the unit test.
type stubNode struct{ t *testing.T }

func (s stubNode) Parse(_ context.Context, relPath string, content []byte) (*parser.FileAnalysis, error) {
	return &parser.FileAnalysis{
		RelPath:  filepath.ToSlash(relPath),
		Filename: filepath.Base(relPath),
		Language: "typescript",
		Content:  string(content),
	}, nil
}
