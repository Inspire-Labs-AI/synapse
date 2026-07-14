package parser

import "testing"

func TestResolveSpecifier(t *testing.T) {
	known := BuildKnownSet([]string{
		"db.ts",
		"userController.ts",
		"models/index.ts",
		"app/api/users/route.ts",
	})

	cases := []struct {
		name         string
		fromDir      string
		specifier    string
		wantResolved string
		wantExternal bool
		wantOK       bool
	}{
		{"bare package", ".", "express", "express", true, false},
		{"scoped package", ".", "@scope/pkg/sub", "@scope/pkg", true, false},
		{"subpath package", ".", "react/jsx-runtime", "react", true, false},
		{"node builtin", ".", "node:fs", "node:fs", true, false},
		{"relative omitted ext", ".", "./db", "db.ts", false, true},
		{"relative to sibling", ".", "./userController", "userController.ts", false, true},
		{"relative up three", "app/api/users", "../../../userController", "userController.ts", false, true},
		{"directory index", ".", "./models", "models/index.ts", false, true},
		{"unresolved internal", ".", "./missing", "missing", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotResolved, gotExternal, gotOK := resolveSpecifier(tc.fromDir, tc.specifier, known)
			if gotResolved != tc.wantResolved || gotExternal != tc.wantExternal || gotOK != tc.wantOK {
				t.Errorf("resolveSpecifier(%q, %q) = (%q, %v, %v); want (%q, %v, %v)",
					tc.fromDir, tc.specifier, gotResolved, gotExternal, gotOK,
					tc.wantResolved, tc.wantExternal, tc.wantOK)
			}
		})
	}
}

func TestBuildKnownSetNormalizesSlashes(t *testing.T) {
	set := BuildKnownSet([]string{`app\api\users\route.ts`})
	if !set["app/api/users/route.ts"] {
		t.Fatalf("expected backslash path to be normalised to forward slashes; got %v", set)
	}
}

func TestResolveImportsMutatesFile(t *testing.T) {
	idx := BuildIndex([]string{"db.ts", "userController.ts"})
	fa := &FileAnalysis{
		RelPath:  "userController.ts",
		Language: "typescript",
		Imports: []ImportRef{
			{Specifier: "./db"},
			{Specifier: "express"},
		},
	}
	ResolveImports(fa, idx)

	if got := fa.Imports[0]; got.Resolved != "db.ts" || got.External || !got.ResolvedOK {
		t.Errorf("internal import resolved wrong: %+v", got)
	}
	if got := fa.Imports[1]; got.Resolved != "express" || !got.External {
		t.Errorf("external import resolved wrong: %+v", got)
	}
}
