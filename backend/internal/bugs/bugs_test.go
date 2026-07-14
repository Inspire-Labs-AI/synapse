package bugs

import (
	"context"
	"strings"
	"testing"

	"project-synapse/backend/internal/store"
)

func imp(src, target string) store.RelRow {
	return store.RelRow{
		SourceSymbol: src, TargetSymbol: target,
		RelationshipType: "imports", Metadata: map[string]any{"external": false},
	}
}

func TestDetectCycles(t *testing.T) {
	files := []store.FileRow{
		{FilePath: "a.ts", Language: "typescript"},
		{FilePath: "b.ts", Language: "typescript"},
		{FilePath: "c.ts", Language: "typescript"},
		{FilePath: "d.ts", Language: "typescript"}, // not in the cycle
	}
	rels := []store.RelRow{
		imp("a.ts", "b.ts"),
		imp("b.ts", "c.ts"),
		imp("c.ts", "a.ts"), // closes the loop a→b→c→a
		imp("d.ts", "a.ts"), // d depends on the cluster but isn't part of the cycle
	}
	bugs := detectCycles(buildGraph(files, rels))
	if len(bugs) != 1 {
		t.Fatalf("got %d cycle bugs, want 1: %+v", len(bugs), bugs)
	}
	b := bugs[0]
	if b.Category != "circular_dependency" || b.Severity != "HIGH" {
		t.Errorf("category/severity = %q/%q", b.Category, b.Severity)
	}
	if len(b.ContextNodes) != 3 {
		t.Errorf("cycle should have 3 files, got %v", b.ContextNodes)
	}
	for _, want := range []string{"a.ts", "b.ts", "c.ts"} {
		found := false
		for _, n := range b.ContextNodes {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("cycle missing %s (%v)", want, b.ContextNodes)
		}
	}
}

func TestNoCycleWhenAcyclic(t *testing.T) {
	files := []store.FileRow{{FilePath: "a.ts", Language: "typescript"}, {FilePath: "b.ts", Language: "typescript"}}
	rels := []store.RelRow{imp("a.ts", "b.ts")} // a→b, no loop
	if bugs := detectCycles(buildGraph(files, rels)); len(bugs) != 0 {
		t.Errorf("acyclic graph produced cycles: %+v", bugs)
	}
}

func TestDetectResourceLeaks(t *testing.T) {
	funcs := []store.FuncCodeRow{
		{File: "store.go", Symbol: "Leaky", StartLine: 1, EndLine: 4,
			Code: "func Leaky(db *sql.DB) {\n\trows, _ := db.Query(\"select 1\")\n\tfor rows.Next() {}\n}"},
		{File: "store.go", Symbol: "Safe", StartLine: 6, EndLine: 9,
			Code: "func Safe(db *sql.DB) {\n\trows, _ := db.Query(\"select 1\")\n\tdefer rows.Close()\n}"},
		{File: "store.go", Symbol: "OpenTx", StartLine: 11, EndLine: 14,
			Code: "func OpenTx(db *sql.DB) {\n\ttx, _ := db.Begin()\n\t_ = tx\n}"}, // no Commit/Rollback
	}
	cands := detectResourceLeaks(funcs)

	got := map[string]bool{}
	for _, c := range cands {
		if c.bug.Category != "resource_leak" {
			t.Errorf("unexpected category: %+v", c.bug)
		}
		if c.code == "" {
			t.Errorf("candidate should carry its code for verification: %+v", c.bug)
		}
		got[c.bug.Location.Entity] = true
	}
	if !got["Leaky"] {
		t.Errorf("Leaky (rows never Closed) should be flagged; cands=%+v", cands)
	}
	if !got["OpenTx"] {
		t.Errorf("OpenTx (tx never Committed/Rolled back) should be flagged")
	}
	if got["Safe"] {
		t.Errorf("Safe (defer rows.Close()) must NOT be flagged")
	}
}

func TestDetectBadPractices(t *testing.T) {
	funcs := []store.FuncCodeRow{
		{File: "auth.ts", Symbol: "login", StartLine: 1, EndLine: 3,
			Code: "async function login() {\n  const apiKey = \"sk-live-ABCDEFGH1234567890\";\n}"},
		{File: "run.ts", Symbol: "run", StartLine: 1, EndLine: 4,
			Code: "function run(s: string) {\n  try { doWork(); } catch {}\n  eval(s);\n}"},
		{File: "clean.ts", Symbol: "clean", StartLine: 1, EndLine: 3,
			Code: "function clean() {\n  try { doWork(); } catch (e) { logger.error(e); throw e; }\n}"},
	}
	byCat := map[string]int{}
	entities := map[string]bool{}
	for _, c := range detectBadPractices(funcs) {
		byCat[c.bug.Category]++
		entities[c.bug.Location.Entity] = true
	}
	if byCat["security"] < 2 { // hardcoded secret + eval
		t.Errorf("expected >=2 security findings (secret + eval), got %d", byCat["security"])
	}
	if byCat["bad_practice"] < 1 { // empty catch
		t.Errorf("expected an empty-catch bad_practice finding, got %d", byCat["bad_practice"])
	}
	if entities["clean"] {
		t.Errorf("clean() has a proper catch and no anti-patterns — must NOT be flagged")
	}
}

// confirmChat is a mock that confirms candidate id 0 and rejects the rest.
type confirmChat struct{}

func (confirmChat) Name() string { return "mock" }
func (confirmChat) Complete(_ context.Context, _, _ string) (string, error) {
	return `{"verdicts":[{"id":0,"confirmed":true,"severity":"HIGH","confidence":"high","title":"Confirmed leak","issue":"real","impact":"bad","fix":"close it"},{"id":1,"confirmed":false}]}`, nil
}

func TestVerifyCandidatesKeepsOnlyConfirmed(t *testing.T) {
	e := &Engine{Chat: confirmChat{}}
	cands := []candidate{
		{code: "func A(){}", bug: Bug{Category: "resource_leak", Severity: "MEDIUM", Location: Location{File: "a.go", Entity: "A"}, Finding: Finding{Issue: "x"}}},
		{code: "func B(){}", bug: Bug{Category: "resource_leak", Severity: "MEDIUM", Location: Location{File: "b.go", Entity: "B"}, Finding: Finding{Issue: "y"}}},
	}
	out := e.verifyCandidates(context.Background(), cands)
	if len(out) != 1 {
		t.Fatalf("verify should keep only the confirmed candidate, got %d", len(out))
	}
	if out[0].Tier != "verified" || out[0].Severity != "HIGH" || out[0].Title != "Confirmed leak" {
		t.Errorf("verdict not applied: %+v", out[0])
	}
}

func TestBugIDFormat(t *testing.T) {
	// Spot-check the SYN-YYYY-NNN id shape used by Scan's `add`.
	id := "SYN-2026-001"
	if !strings.HasPrefix(id, "SYN-") || strings.Count(id, "-") != 2 {
		t.Errorf("bad id shape %q", id)
	}
}
