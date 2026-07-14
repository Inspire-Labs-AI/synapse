package docs

import (
	"context"
	"strings"
	"testing"

	"project-synapse/backend/internal/store"
)

// mockChat returns a fixed completion — here, JSON whose markdown newlines are
// DOUBLE-escaped (\\n), reproducing the model behaviour that rendered visible
// "\n" and unparsed "## Heading" in the docs UI.
type mockChat struct{ resp string }

func (m mockChat) Complete(_ context.Context, _, _ string) (string, error) { return m.resp, nil }
func (m mockChat) Name() string                                            { return "mock" }

func TestNarrativeRepairsDoubleEscapedNewlines(t *testing.T) {
	// In this raw Go string, `\\n` is backslash-backslash-n; as JSON that decodes
	// to a literal backslash-n in the field value — exactly the reported bug.
	raw := `{"introduction":"GrabOn_App is a mobile app.\n\n## Capabilities\n- Retrieve deals\n- Update itself","architecture":"## Layers\n- a","concepts":"### Concept\nText.","data_flow":"1. step one\n2. step two"}`

	e := &Engine{Chat: mockChat{resp: raw}}
	nd := e.narrative(context.Background(), "GrabOn_App", nil, nil)

	if strings.Contains(nd.Introduction, `\n`) {
		t.Errorf("introduction still contains literal \\n: %q", nd.Introduction)
	}
	if !strings.Contains(nd.Introduction, "\n## Capabilities") {
		t.Errorf("heading is not on its own line:\n%s", nd.Introduction)
	}
	for name, got := range map[string]string{
		"architecture": nd.Architecture,
		"concepts":     nd.Concepts,
		"data_flow":    nd.DataFlow,
	} {
		if strings.Contains(got, `\n`) {
			t.Errorf("%s still contains literal \\n: %q", name, got)
		}
		if !strings.Contains(got, "\n") {
			t.Errorf("%s has no real newline: %q", name, got)
		}
	}
}

func TestDepKey(t *testing.T) {
	cases := map[string]string{
		"react":                           "react",
		"react-dom/client":                "react-dom",
		"@reduxjs/toolkit/query":          "@reduxjs/toolkit",
		"@scope/pkg":                      "@scope/pkg",
		"github.com/jackc/pgx/v5/pgxpool": "github.com/jackc/pgx/v5/pgxpool", // path-like kept whole
		"./relative":                      "./relative",
	}
	for in, want := range cases {
		if got := depKey(in); got != want {
			t.Errorf("depKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A dependency imported only by an unreachable (dead) file must rank below, and
// be flagged distinctly from, one used across live files — this is what keeps an
// unused `redux` out of the "tech stack" when the app really uses `zustand`.
func TestBuildSummaryFlagsUnusedDeps(t *testing.T) {
	extImp := func(src, spec string) store.RelRow {
		return store.RelRow{SourceSymbol: src, TargetSymbol: spec, RelationshipType: "imports",
			Metadata: map[string]any{"external": true, "specifier": spec}}
	}
	intImp := func(src, dst string) store.RelRow {
		return store.RelRow{SourceSymbol: src, TargetSymbol: dst, RelationshipType: "imports",
			Metadata: map[string]any{"external": false}}
	}
	files := []store.FileRow{
		{FilePath: "index.ts", Language: "typescript"},             // entry (isEntryLike)
		{FilePath: "store.ts", Language: "typescript"},             // reachable via index
		{FilePath: "legacy/reduxStore.ts", Language: "typescript"}, // orphan → unreachable
	}
	rels := []store.RelRow{
		intImp("index.ts", "store.ts"), // index → store makes store reachable
		extImp("index.ts", "zustand"),  // zustand used by 2 reachable files
		extImp("store.ts", "zustand"),
		extImp("legacy/reduxStore.ts", "redux"), // redux only in the dead file
		extImp("legacy/reduxStore.ts", "react-redux"),
	}
	sum := buildSummary("demo", files, rels)

	if !strings.Contains(sum, "zustand (2 files)") {
		t.Errorf("zustand should show as used by 2 live files:\n%s", sum)
	}
	if !strings.Contains(sum, "redux (1 file(s), all in unused/unreachable code") {
		t.Errorf("redux should be flagged as unused/legacy:\n%s", sum)
	}
	if strings.Index(sum, "zustand") > strings.Index(sum, "- redux ") {
		t.Errorf("zustand (live) should rank above redux (dead):\n%s", sum)
	}
}
