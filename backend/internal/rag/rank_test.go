package rag

import (
	"testing"

	"project-synapse/backend/internal/store"
)

func hit(file, sym, typ string, dist float64, code string) store.ChunkHit {
	return store.ChunkHit{FilePath: file, SymbolName: sym, ChunkType: typ, Distance: dist, Content: code}
}

// A one-line constant that is *closer* by raw cosine must still lose to a real
// function — this is the exact failure the user reported (citations full of
// vague constants and markdown blocks).
func TestRerankDemotesConstantsAndDocs(t *testing.T) {
	hits := []store.ChunkHit{
		hit("cfg/limits.py", "MAX_ITEMS", "const", 0.10, "MAX_ITEMS = 100"),
		hit("docs/intro.md", "Overview", "myelin_doc", 0.12, "# Overview\nThis project does things.\nMore prose."),
		hit("svc/auth.py", "login", "function", 0.20, "def login(u, p):\n    tok = mint(u)\n    check(p)\n    return tok\n"),
	}
	scored := rerankChunks(hits, "how does login work")
	if got := scored[0].hit.SymbolName; got != "login" {
		t.Fatalf("expected `login` ranked first, got %q (order: %v)", got, order(scored))
	}
}

// Citations must only contain real code symbols.
func TestSelectChunksCitationsExcludeNonCode(t *testing.T) {
	hits := []store.ChunkHit{
		hit("svc/auth.py", "login", "function", 0.20, "def login():\n    a=1\n    b=2\n    return a+b\n"),
		hit("cfg/limits.py", "MAX_ITEMS", "const", 0.05, "MAX_ITEMS = 100"),
		hit("docs/intro.md", "Overview", "myelin_doc", 0.06, "# Overview\nprose\nprose\nprose"),
	}
	scored := rerankChunks(hits, "login")
	cites := selectChunks(scored, 5, func(h store.ChunkHit) bool { return citableTypes[h.ChunkType] })
	if len(cites) != 1 || cites[0].SymbolName != "login" {
		t.Fatalf("citations should be exactly [login], got %v", names(cites))
	}
	// The single-line const is filtered out of the model context too.
	ctxc := selectChunks(scored, 5, func(h store.ChunkHit) bool { return !isLowValue(h) })
	for _, c := range ctxc {
		if c.SymbolName == "MAX_ITEMS" {
			t.Errorf("single-line const leaked into the context window")
		}
	}
}

// Diversity is a preference: a comparable hit from another file should be
// pulled in early, but one file may still contribute several symbols (up to the
// backstop) when it is genuinely where the answer lives.
func TestSelectChunksPrefersDiversityWithoutStarvingTheBestFile(t *testing.T) {
	body := "def f():\n    x=1\n    y=2\n    return x+y\n"
	var hits []store.ChunkHit
	for i := 0; i < 6; i++ {
		hits = append(hits, hit("big.py", "f", "function", 0.10, body))
	}
	hits = append(hits, hit("other.py", "g", "function", 0.11, body))
	got := selectChunks(rerankChunks(hits, "f"), 6, nil)

	var fromBig, fromOther int
	for _, h := range got {
		if h.FilePath == "big.py" {
			fromBig++
		} else {
			fromOther++
		}
	}
	if fromBig > maxChunksPerFile {
		t.Errorf("backstop violated: %d chunks from big.py (cap %d)", fromBig, maxChunksPerFile)
	}
	if fromOther == 0 {
		t.Errorf("the near-equal hit from other.py should have been pulled in for diversity")
	}
	if fromBig < 2 {
		t.Errorf("the answering file should still contribute several symbols, got %d", fromBig)
	}
}

// Implementation beats test fixture when both mention the queried symbol.
func TestRerankDemotesTestFiles(t *testing.T) {
	body := "def session_scope():\n    s = Session()\n    yield s\n    s.close()\n"
	hits := []store.ChunkHit{
		hit("tests/test_db.py", "session_scope", "function", 0.10, body),
		hit("db/session.py", "session_scope", "function", 0.12, body),
	}
	scored := rerankChunks(hits, "how is session_scope used")
	if got := scored[0].hit.FilePath; got != "db/session.py" {
		t.Errorf("implementation should outrank the test fixture, got %q first", got)
	}
}

// Python/Go/Rust import targets must be recognised as files (they previously
// weren't, so they never reached highlighted_files or the execution flow).
func TestLooksLikeFileCoversAllLanguages(t *testing.T) {
	for _, f := range []string{"a.ts", "b.tsx", "c.js", "d.go", "e.rs", "f.py", "g.md"} {
		if !looksLikeFile(f) {
			t.Errorf("%s should be recognised as a file", f)
		}
	}
	if looksLikeFile("react") || looksLikeFile("django.db") {
		t.Errorf("bare package specifiers must not look like files")
	}
}

func order(s []scoredChunk) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.hit.SymbolName
	}
	return out
}

func names(h []store.ChunkHit) []string {
	out := make([]string, len(h))
	for i, x := range h {
		out[i] = x.SymbolName
	}
	return out
}
