package parser

import "testing"

func TestTSConfigAliasResolution(t *testing.T) {
	// JSONC: a line comment + a trailing comma, both of which must be tolerated.
	tsconfig := []byte(`{
        // editor settings
        "compilerOptions": {
            "baseUrl": ".",
            "paths": { "@/*": ["./*"] },
        }
    }`)
	rules := ParseTSConfigPaths("frontend", tsconfig)
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1 (%+v)", len(rules), rules)
	}

	idx := BuildIndexWithAliases([]string{
		"frontend/auth.ts",
		"frontend/app/page.tsx",
		"frontend/lib/api.ts",
	}, rules)

	fa := &FileAnalysis{Language: "typescript", RelPath: "frontend/app/page.tsx", Imports: []ImportRef{
		{Specifier: "@/auth"},
		{Specifier: "@/lib/api"},
		{Specifier: "react"},
	}}
	ResolveImports(fa, idx)

	if fa.Imports[0].Resolved != "frontend/auth.ts" || !fa.Imports[0].ResolvedOK || fa.Imports[0].External {
		t.Errorf("@/auth resolved wrong: %+v", fa.Imports[0])
	}
	if fa.Imports[1].Resolved != "frontend/lib/api.ts" || !fa.Imports[1].ResolvedOK {
		t.Errorf("@/lib/api resolved wrong: %+v", fa.Imports[1])
	}
	if !fa.Imports[2].External {
		t.Errorf("react should still be external: %+v", fa.Imports[2])
	}
}

func TestTSConfigBaseURLResolution(t *testing.T) {
	// NestJS-style tsconfig: baseUrl "." makes `src/...` imports resolve from the
	// repo root — the pattern that was previously mis-classified as external.
	tsconfig := []byte(`{"compilerOptions": {"baseUrl": "./"}}`)
	cfg := ParseTSConfig("", tsconfig)
	if !cfg.HasBaseDir || cfg.BaseDir != "" {
		t.Fatalf("baseDir = %q (has=%v), want \"\" (true)", cfg.BaseDir, cfg.HasBaseDir)
	}

	idx := BuildIndexWithConfig([]string{
		"src/common/utils/encryption.util.ts",
		"src/modules/admin/admin.module.ts",
	}, cfg.Aliases, []string{cfg.BaseDir})

	fa := &FileAnalysis{Language: "typescript", RelPath: "src/modules/admin/admin.module.ts", Imports: []ImportRef{
		{Specifier: "src/common/utils/encryption.util"}, // baseUrl-relative -> internal
		{Specifier: "@nestjs/common"},                   // npm package -> external
		{Specifier: "./local"},                          // relative miss -> not external, unresolved
	}}
	ResolveImports(fa, idx)

	if fa.Imports[0].Resolved != "src/common/utils/encryption.util.ts" || !fa.Imports[0].ResolvedOK || fa.Imports[0].External {
		t.Errorf("src/... baseUrl import resolved wrong: %+v", fa.Imports[0])
	}
	if !fa.Imports[1].External {
		t.Errorf("@nestjs/common should stay external: %+v", fa.Imports[1])
	}
	if fa.Imports[2].External {
		t.Errorf("relative ./local should not be external: %+v", fa.Imports[2])
	}
}
