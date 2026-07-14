package agentctx

import "testing"

// The exported "tech stack" must be third-party packages only. `__future__`,
// `datetime`, `os`, `net/http`, `fs`, and `std` describe the language, not the
// project — and `__future__` would otherwise top the usage ranking.
func TestClassifyDepSeparatesStdlibFromTechStack(t *testing.T) {
	cases := []struct {
		spec, lang string
		wantName   string
		wantThird  bool
	}{
		// Python
		{"__future__", "python", "__future__", false},
		{"os.path", "python", "os", false},
		{"datetime", "python", "datetime", false},
		{"sqlalchemy.orm", "python", "sqlalchemy", true},
		{"fastapi", "python", "fastapi", true},
		// Go
		{"net/http", "go", "net", false},
		{"fmt", "go", "fmt", false},
		{"github.com/jackc/pgx/v5", "go", "github.com/jackc/pgx", true},
		// Rust
		{"std::collections::HashMap", "rust", "std", false},
		{"serde::Serialize", "rust", "serde", true},
		// TS / JS
		{"node:fs", "typescript", "fs", false},
		{"path", "javascript", "path", false},
		{"react", "typescript", "react", true},
		{"@nestjs/common", "typescript", "@nestjs/common", true},
		{"react/jsx-runtime", "typescript-react", "react", true},
	}
	for _, c := range cases {
		name, third := classifyDep(c.spec, c.lang)
		if name != c.wantName || third != c.wantThird {
			t.Errorf("classifyDep(%q, %q) = (%q, %v); want (%q, %v)",
				c.spec, c.lang, name, third, c.wantName, c.wantThird)
		}
	}
}
