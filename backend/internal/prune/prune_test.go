package prune

import (
	"testing"

	"project-synapse/backend/internal/store"
)

func imp(src, target string, external bool, symbols ...string) store.RelRow {
	syms := make([]any, len(symbols))
	for i, s := range symbols {
		syms[i] = s
	}
	return store.RelRow{
		SourceSymbol:     src,
		TargetSymbol:     target,
		RelationshipType: "imports",
		Metadata:         map[string]any{"external": external, "symbols": syms},
	}
}

func find(cs []Candidate, tier, path, symbol string) *Candidate {
	for i := range cs {
		if cs[i].Tier == tier && cs[i].Path == path && cs[i].Symbol == symbol {
			return &cs[i]
		}
	}
	return nil
}

func TestAnalyzeTiers(t *testing.T) {
	files := []store.FileRow{
		{FilePath: "src/index.ts", Language: "typescript"}, // entry (index)
		{FilePath: "src/used.ts", Language: "typescript"},  // imported by index → live
		{FilePath: "src/orphan.ts", Language: "typescript"}, // nobody imports → Tier A
		{FilePath: "src/deadA.ts", Language: "typescript"},  // only deadB imports → Tier B
		{FilePath: "src/deadB.ts", Language: "typescript"},  // imports deadA, itself unreachable → Tier B
		{FilePath: "README.md", Language: "markdown"},       // doc → ignored
	}
	rels := []store.RelRow{
		// index imports used (by name "helper") → used.ts live, helper used.
		imp("src/index.ts", "src/used.ts", false, "helper"),
		// deadB imports deadA (cluster, unreachable from entry).
		imp("src/deadB.ts", "src/deadA.ts", false, "x"),
		// exports
		{SourceSymbol: "src/used.ts", TargetSymbol: "helper", RelationshipType: "exports"},
		{SourceSymbol: "src/used.ts", TargetSymbol: "neverImported", RelationshipType: "exports"}, // Tier C
		{SourceSymbol: "src/index.ts", TargetSymbol: "default", RelationshipType: "exports"},
	}
	calls := []store.CallRow{
		// used.ts: helper calls usedPrivate; deadHelper is declared but never called.
		{File: "src/used.ts", Caller: "helper", Callee: "usedPrivate"},
	}
	decls := []store.DeclRow{
		{File: "src/used.ts", Symbol: "helper", ChunkType: "function"},
		{File: "src/used.ts", Symbol: "usedPrivate", ChunkType: "function"},
		{File: "src/used.ts", Symbol: "deadHelper", ChunkType: "function"}, // Tier D
	}

	rep := analyze("/repo/proj", files, rels, calls, decls)

	if rep.CodeFiles != 5 {
		t.Errorf("code files = %d, want 5 (markdown excluded)", rep.CodeFiles)
	}
	// Tier A: orphan.ts
	if c := find(rep.Candidates, "orphan_file", "src/orphan.ts", ""); c == nil || c.Confidence != "high" {
		t.Errorf("expected high-confidence orphan for orphan.ts, got %+v", c)
	}
	// deadB has no importers → orphan; deadA is imported only by the dead deadB → cluster.
	if find(rep.Candidates, "orphan_file", "src/deadB.ts", "") == nil {
		t.Errorf("expected orphan_file for deadB.ts (no importers)")
	}
	if find(rep.Candidates, "dead_cluster", "src/deadA.ts", "") == nil {
		t.Errorf("expected dead_cluster for deadA.ts (imported only by dead deadB)")
	}
	// Tier C: neverImported export of the live used.ts; helper IS imported so not flagged.
	if find(rep.Candidates, "unused_export", "src/used.ts", "neverImported") == nil {
		t.Errorf("expected unused_export for neverImported")
	}
	if find(rep.Candidates, "unused_export", "src/used.ts", "helper") != nil {
		t.Errorf("helper is imported by name — must NOT be flagged")
	}
	// Tier D: deadHelper private + uncalled; usedPrivate is called so not flagged.
	if find(rep.Candidates, "unused_function", "src/used.ts", "deadHelper") == nil {
		t.Errorf("expected unused_function for deadHelper")
	}
	if find(rep.Candidates, "unused_function", "src/used.ts", "usedPrivate") != nil {
		t.Errorf("usedPrivate is called — must NOT be flagged")
	}
	// Live + entry files must never be flagged.
	for _, c := range rep.Candidates {
		if c.Path == "src/index.ts" || (c.Path == "src/used.ts" && c.Kind == "file") {
			t.Errorf("live/entry file wrongly flagged: %+v", c)
		}
	}
}

func TestAnalyzeGoPackageReachability(t *testing.T) {
	files := []store.FileRow{
		{FilePath: "cmd/server/main.go", Language: "go"},      // entry (cmd/)
		{FilePath: "internal/store/store.go", Language: "go"}, // imported pkg → live
		{FilePath: "internal/store/query.go", Language: "go"}, // sibling in live pkg → live
		{FilePath: "internal/dead/dead.go", Language: "go"},   // orphan package
	}
	rels := []store.RelRow{
		// main imports the store package (resolver points at one representative file).
		imp("cmd/server/main.go", "internal/store/store.go", false, "store"),
		{SourceSymbol: "internal/store/store.go", TargetSymbol: "New", RelationshipType: "exports"},
	}
	decls := []store.DeclRow{{File: "internal/store/store.go", Symbol: "helper", ChunkType: "function"}}

	rep := analyze("/repo/g", files, rels, nil, decls)

	// dead package → orphan.
	if find(rep.Candidates, "orphan_file", "internal/dead/dead.go", "") == nil {
		t.Errorf("expected orphan_file for internal/dead/dead.go")
	}
	// query.go is a sibling of the imported store.go — its package is live, so it
	// must NOT be flagged (Go package-level reachability, the key fix).
	for _, c := range rep.Candidates {
		if c.Path == "internal/store/query.go" || c.Path == "internal/store/store.go" {
			t.Errorf("live Go package file wrongly flagged: %+v", c)
		}
	}
	// Symbol-level tiers are skipped for Go (package scoping).
	for _, c := range rep.Candidates {
		if c.Tier == "unused_export" || c.Tier == "unused_function" {
			t.Errorf("Go must not produce symbol-level candidates, got %+v", c)
		}
	}
}
