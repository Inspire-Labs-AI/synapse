package parser

import (
	"context"
	"testing"
)

// A realistic module exercising: a docstring that must be masked, absolute +
// relative + multiline imports, __all__, decorators/routes, a class with methods,
// a nested-def closure, a PEP 695 generic, module constants, and privacy.
const pySample = `"""Route handlers.

A docstring that mentions def ghost(): and a "quote" and a # hash —
none of which must be parsed as code.
"""
from __future__ import annotations
import os
import os.path as osp
from ..models import User, Post
from . import helpers
from myapp.services import billing
from .schema import (
    CreateReq,
    UpdateReq,
)

__all__ = ["Handler", "healthcheck", "first"]

VERSION = "1.2.0"
MAX_ITEMS = 100
_secret = "internal-only"


@app.get("/health")
def healthcheck():
    return {"ok": True}


def _private():
    return 1


def make_adder(n):
    def add(x):
        return x + n
    return add


def first[T](xs: list[T]) -> T:
    return xs[0]


class Handler:
    @app.post("/users")
    def create(self, req):
        return helpers.build(req)

    def _helper(self):
        return _private()
`

func exportPatternSet(exps []ExportRef, name string) map[string]bool {
	out := map[string]bool{}
	for _, e := range exps {
		if e.Name == name {
			for _, p := range e.Patterns {
				out[p] = true
			}
		}
	}
	return out
}

func TestParsePython(t *testing.T) {
	fa, err := parsePythonFile("src/myapp/api/routes.py", []byte(pySample))
	if err != nil {
		t.Fatalf("parsePythonFile error: %v", err)
	}
	if fa.Language != "python" {
		t.Fatalf("language = %q, want python", fa.Language)
	}

	decls := names(fa.Declarations)
	// Docstring content must never surface as a declaration.
	if _, bad := decls["ghost"]; bad {
		t.Errorf("docstring `def ghost` leaked into declarations: %v", decls)
	}
	// Nested def is a closure local, not a top-level declaration.
	if _, bad := decls["add"]; bad {
		t.Errorf("nested def `add` should not be a declaration: %v", decls)
	}
	checks := map[string]string{
		"healthcheck":     "function",
		"_private":        "function",
		"make_adder":      "function",
		"first":           "function",
		"Handler":         "class",
		"Handler.create":  "method",
		"Handler._helper": "method",
		"VERSION":         "const",
		"MAX_ITEMS":       "const",
		"_secret":         "variable",
	}
	for name, kind := range checks {
		if decls[name] != kind {
			t.Errorf("decl %q kind = %q, want %q (all: %v)", name, decls[name], kind, decls)
		}
	}

	// Exports come from __all__ exactly.
	exps := exportNames(fa.Exports)
	for _, want := range []string{"Handler", "healthcheck", "first"} {
		if !exps[want] {
			t.Errorf("missing export %q (have %v)", want, exps)
		}
	}
	for _, no := range []string{"VERSION", "MAX_ITEMS", "_secret", "_private", "make_adder", "Handler.create"} {
		if exps[no] {
			t.Errorf("%q should not be exported (have %v)", no, exps)
		}
	}

	// Dendrite patterns: decorator on healthcheck, generic on first.
	if !exportPatternSet(fa.Exports, "healthcheck")["decorator"] {
		t.Errorf("healthcheck should carry the decorator pattern")
	}
	if !exportPatternSet(fa.Exports, "first")["generic_wrapper"] {
		t.Errorf("first should carry generic_wrapper (PEP 695)")
	}

	// Imports: specifiers present.
	specs := map[string][]string{}
	for _, im := range fa.Imports {
		specs[im.Specifier] = im.Symbols
	}
	for _, want := range []string{
		"os", "os.path", "__future__", "..models", ".helpers",
		"myapp.services", ".schema",
	} {
		if _, ok := specs[want]; !ok {
			t.Errorf("missing import specifier %q (have %v)", want, specs)
		}
	}
	// Multiline `from .schema import (A, B)` keeps both symbols.
	if got := specs[".schema"]; len(got) != 2 {
		t.Errorf(".schema symbols = %v, want two (CreateReq, UpdateReq)", got)
	}

	// Endpoints from decorators.
	gotEp := map[string]string{} // "METHOD PATH" -> handler
	for _, ep := range fa.Endpoints {
		gotEp[ep.Method+" "+ep.Path] = ep.Handler
	}
	if gotEp["GET /health"] != "healthcheck" {
		t.Errorf("missing GET /health -> healthcheck (have %v)", gotEp)
	}
	if gotEp["POST /users"] != "create" {
		t.Errorf("missing POST /users -> create (have %v)", gotEp)
	}

	// Intra-file call graph: _helper -> _private.
	if !hasCall(fa.Calls, "Handler._helper", "_private") {
		t.Errorf("expected call edge Handler._helper -> _private, got %v", fa.Calls)
	}
}

func TestPythonResolution(t *testing.T) {
	fa, err := parsePythonFile("src/myapp/api/routes.py", []byte(pySample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	idx := BuildIndex([]string{
		"src/myapp/__init__.py",
		"src/myapp/models.py",
		"src/myapp/api/__init__.py",
		"src/myapp/api/routes.py",
		"src/myapp/api/helpers.py",
		"src/myapp/api/schema.py",
		"src/myapp/services/__init__.py",
	})
	ResolveImports(fa, idx)

	want := map[string]struct {
		resolved string
		ok       bool
		external bool
	}{
		"..models":       {"src/myapp/models.py", true, false},            // relative parent package
		".helpers":       {"src/myapp/api/helpers.py", true, false},       // relative sibling
		".schema":        {"src/myapp/api/schema.py", true, false},        // relative sibling (multiline)
		"myapp.services": {"src/myapp/services/__init__.py", true, false}, // absolute via src root
		"os":             {"os", false, true},                             // stdlib -> external
		"os.path":        {"os", false, true},                             // stdlib submodule -> external "os"
		"__future__":     {"__future__", false, true},                     // external
	}
	for _, im := range fa.Imports {
		w, tracked := want[im.Specifier]
		if !tracked {
			continue
		}
		if im.Resolved != w.resolved || im.ResolvedOK != w.ok || im.External != w.external {
			t.Errorf("import %q resolved=%q ok=%v external=%v; want resolved=%q ok=%v external=%v",
				im.Specifier, im.Resolved, im.ResolvedOK, im.External, w.resolved, w.ok, w.external)
		}
	}
}

// Dispatcher routes .py to the Python parser.
func TestMultiParserPythonDispatch(t *testing.T) {
	m := NewMultiParser(nil)
	fa, err := m.Parse(context.Background(), "x.py", []byte("def a():\n    return 1\n"))
	if err != nil || fa.Language != "python" {
		t.Errorf("python dispatch failed: fa=%+v err=%v", fa, err)
	}
}
