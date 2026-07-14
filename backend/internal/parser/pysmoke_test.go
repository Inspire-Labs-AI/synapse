package parser

import (
	"testing"
)

const pyMessy = "import sys\n" +
	"try:\n" +
	"    import ujson as json\n" +
	"except ImportError:\n" +
	"    import json\n" +
	"\n" +
	"if sys.version_info >= (3, 10):\n" +
	"    from typing import TypeAlias\n" +
	"\n" +
	"async def fetch(url: str) -> dict:\n" +
	"    async with session.get(url) as resp:\n" +
	"        return await resp.json()\n" +
	"\n" +
	"class Base:\n" +
	"    class Meta:\n" +
	"        ordering = [\"-created\"]\n" +
	"\n" +
	"    async def save(self):\n" +
	"        return fetch(self.url)\n" +
	"\n" +
	"def f_string_trap():\n" +
	"    x = f\"value is {compute()} and def not_a_def(): trap\"\n" +
	"    return x\n" +
	"\n" +
	"def compute():\n" +
	"    return 42\n"

func TestPythonSmoke(t *testing.T) {
	fa, err := parsePythonFile("app/messy.py", []byte(pyMessy))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	decls := names(fa.Declarations)
	// f-string content must be masked — no phantom declaration.
	if _, bad := decls["not_a_def"]; bad {
		t.Errorf("f-string `def not_a_def` leaked: %v", decls)
	}
	for name, kind := range map[string]string{
		"fetch": "function", "compute": "function", "f_string_trap": "function",
		"Base": "class", "Base.Meta": "class", "Base.save": "method",
	} {
		if decls[name] != kind {
			t.Errorf("decl %q = %q, want %q", name, decls[name], kind)
		}
	}
	// Conditional / try-guarded imports are still captured.
	specs := map[string]bool{}
	for _, im := range fa.Imports {
		specs[im.Specifier] = true
	}
	for _, want := range []string{"sys", "ujson", "json", "typing"} {
		if !specs[want] {
			t.Errorf("missing import %q (have %v)", want, specs)
		}
	}
	// f-string call is masked, so no compute() edge from f_string_trap; but the
	// real call Base.save -> fetch should be present.
	if !hasCall(fa.Calls, "Base.save", "fetch") {
		t.Errorf("expected Base.save -> fetch, got %v", fa.Calls)
	}
	t.Logf("decls=%v", decls)
	t.Logf("imports=%d endpoints=%d calls=%v", len(fa.Imports), len(fa.Endpoints), fa.Calls)
}

// Regression: a multi-line function signature must NOT truncate the body span.
// (Previously the closing `) -> dict:` line, at the same indent as `def`, ended
// the span at the signature, so the LLM saw a bodyless function and reported a
// bogus "missing body / syntax error".)
const pyMultilineSig = "import os\n" + // 1
	"\n" + // 2
	"def get_all_values(\n" + // 3
	"    self,\n" + // 4
	"    config: Config,\n" + // 5
	") -> dict:\n" + // 6
	"    settings = {}\n" + // 7
	"    for k in config:\n" + // 8
	"        settings[k] = mask(k)\n" + // 9
	"    return settings\n" // 10

func TestPythonMultilineSignatureSpan(t *testing.T) {
	fa, err := parsePythonFile("admin_config.py", []byte(pyMultilineSig))
	if err != nil {
		t.Fatal(err)
	}
	var got *Declaration
	for i := range fa.Declarations {
		if fa.Declarations[i].Name == "get_all_values" {
			got = &fa.Declarations[i]
		}
	}
	if got == nil {
		t.Fatalf("get_all_values not found in %v", fa.Declarations)
	}
	if got.StartLine != 3 || got.EndLine != 10 {
		t.Errorf("span = %d-%d, want 3-10 (body must not be truncated at the signature)", got.StartLine, got.EndLine)
	}
}

// Regression: an import inside a function body is marked Deferred (so it cannot
// cause an import-time cycle) while KEEPING its kind — a module-level import is
// not deferred.
func TestPythonDeferredImport(t *testing.T) {
	src := "import os\n" + // module-level
		"\n" +
		"def handler():\n" +
		"    from .models import User\n" + // deferred
		"    return User\n"
	fa, err := parsePythonFile("routes.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ImportRef{}
	for _, im := range fa.Imports {
		got[im.Specifier] = im
	}
	if got["os"].Deferred {
		t.Errorf("module-level `import os` must not be deferred")
	}
	if !got[".models"].Deferred {
		t.Errorf("function-body `from .models` must be deferred (got %+v)", got[".models"])
	}
	if k := got[".models"].Kind; k != "from" {
		t.Errorf("deferred import must keep Kind=%q, got %q", "from", k)
	}
}
