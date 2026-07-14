package parser

import "testing"

// Regression: `from <package> import <submodule>` must create an edge to the
// SUBMODULE file, not just to the package's __init__.py. Missing this made every
// submodule look unimported and cascaded ~20 live files into a phantom
// "dead cluster" on a real repo.
func TestPythonFromPackageImportSubmodule(t *testing.T) {
	src := "from src.controllers import service\n" +
		"from src.services.classification import features as F\n" +
		"from src.shared.response import fail, ok\n" // symbols, not modules
	fa, err := parsePythonFile("be/src/routers/data.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}

	// The alias must NOT be recorded — `features`, not `F`.
	for _, im := range fa.Imports {
		if im.Specifier == "src.services.classification" {
			if len(im.Symbols) != 1 || im.Symbols[0] != "features" {
				t.Errorf("aliased import should record the original name `features`, got %v", im.Symbols)
			}
		}
	}

	idx := BuildIndex([]string{
		"be/src/__init__.py",
		"be/src/routers/data.py",
		"be/src/controllers/__init__.py",
		"be/src/controllers/service.py",
		"be/src/services/classification/__init__.py",
		"be/src/services/classification/features.py",
		"be/src/shared/__init__.py",
		"be/src/shared/response.py",
	})
	ResolveImports(fa, idx)

	targets := map[string]bool{}
	for _, im := range fa.Imports {
		if im.ResolvedOK {
			targets[im.Resolved] = true
		}
	}
	for _, want := range []string{
		"be/src/controllers/service.py",              // submodule via `from pkg import mod`
		"be/src/services/classification/features.py", // submodule, aliased
		"be/src/controllers/__init__.py",             // the package itself still runs
		"be/src/shared/response.py",                  // plain module import
	} {
		if !targets[want] {
			t.Errorf("missing resolved edge to %s (got %v)", want, keys(targets))
		}
	}
	// `fail`/`ok` are functions, not modules — no phantom edges for them.
	for tgt := range targets {
		if tgt == "be/src/shared/response/fail.py" || tgt == "be/src/shared/fail.py" {
			t.Errorf("symbol %q must not resolve as a module", tgt)
		}
	}
}

// A function-body import is deferred but still a real dependency, and its kind
// must survive (the resolver needs `from` to find submodules).
func TestPythonDeferredKeepsKind(t *testing.T) {
	src := "def handler():\n    from src.analytics import comparison\n    return comparison\n"
	fa, err := parsePythonFile("be/src/controllers/service.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(fa.Imports) == 0 {
		t.Fatal("no imports parsed")
	}
	im := fa.Imports[0]
	if !im.Deferred {
		t.Errorf("function-body import should be Deferred")
	}
	if im.Kind != "from" {
		t.Errorf("Kind must stay %q, got %q", "from", im.Kind)
	}

	idx := BuildIndex([]string{
		"be/src/__init__.py",
		"be/src/controllers/service.py",
		"be/src/analytics/__init__.py",
		"be/src/analytics/comparison.py",
	})
	ResolveImports(fa, idx)
	var found bool
	for _, x := range fa.Imports {
		if x.Resolved == "be/src/analytics/comparison.py" && x.ResolvedOK {
			found = true
			if !x.Deferred {
				t.Errorf("the derived submodule edge should inherit Deferred")
			}
		}
	}
	if !found {
		t.Errorf("lazy `from src.analytics import comparison` must still reach comparison.py")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
