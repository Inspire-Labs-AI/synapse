package parser

import (
	"context"
	"testing"
)

func hasCall(calls []CallEdge, caller, callee string) bool {
	for _, c := range calls {
		if c.Caller == caller && c.Callee == callee {
			return true
		}
	}
	return false
}

const goCallSample = `package svc

type Store struct{ n int }

func helper() int { return 41 }

func New() *Store { return &Store{} }

func (s *Store) Run() int {
	x := helper()
	_ = New()
	return x + 1
}
`

func TestParseGoCalls(t *testing.T) {
	fa, err := parseGoFile("svc/store.go", []byte(goCallSample))
	if err != nil {
		t.Fatalf("parseGoFile: %v", err)
	}
	// New() constructs Store{}  -> New -> Store
	if !hasCall(fa.Calls, "New", "Store") {
		t.Errorf("missing New->Store, calls=%v", fa.Calls)
	}
	// Store.Run calls helper() and New() (intra-file).
	if !hasCall(fa.Calls, "Store.Run", "helper") {
		t.Errorf("missing Store.Run->helper, calls=%v", fa.Calls)
	}
	if !hasCall(fa.Calls, "Store.Run", "New") {
		t.Errorf("missing Store.Run->New, calls=%v", fa.Calls)
	}
}

const rustCallSample = `pub fn helper() -> u32 { 1 }
pub fn run() -> u32 { helper() + helper() }
`

func TestParseRustCalls(t *testing.T) {
	fa, err := parseRustFile("src/lib.rs", []byte(rustCallSample))
	if err != nil {
		t.Fatalf("parseRustFile: %v", err)
	}
	if !hasCall(fa.Calls, "run", "helper") {
		t.Errorf("missing run->helper, calls=%v", fa.Calls)
	}
}

func names(decls []Declaration) map[string]string {
	m := map[string]string{}
	for _, d := range decls {
		m[d.Name] = d.Kind
	}
	return m
}

func exportNames(exps []ExportRef) map[string]bool {
	m := map[string]bool{}
	for _, e := range exps {
		m[e.Name] = true
	}
	return m
}

const goSample = `package store

import (
	"fmt"
	"net/http"

	"project-synapse/backend/internal/embed"
)

// Store is the persistence layer.
type Store struct {
	pool int
}

const MaxConns = 10

func New() *Store { return &Store{} }

func (s *Store) Query(q string) error {
	return fmt.Errorf("nope")
}

func Map[T any](in []T) []T { return in }

func mount(mux *http.ServeMux) {
	mux.HandleFunc("/users", nil)
}

var _ = embed.Nothing
`

func TestParseGo(t *testing.T) {
	fa, err := parseGoFile("backend/internal/store/store.go", []byte(goSample))
	if err != nil {
		t.Fatalf("parseGoFile error: %v", err)
	}
	if fa.Language != "go" {
		t.Fatalf("language = %q, want go", fa.Language)
	}

	decls := names(fa.Declarations)
	if decls["Store"] != "struct" {
		t.Errorf("Store kind = %q, want struct", decls["Store"])
	}
	if decls["New"] != "function" {
		t.Errorf("New kind = %q, want function", decls["New"])
	}
	if decls["Store.Query"] != "method" {
		t.Errorf("expected method Store.Query, got %v", decls)
	}
	if decls["MaxConns"] != "const" {
		t.Errorf("MaxConns kind = %q, want const", decls["MaxConns"])
	}

	exps := exportNames(fa.Exports)
	for _, want := range []string{"Store", "New", "Store.Query", "Map", "MaxConns"} {
		if !exps[want] {
			t.Errorf("missing export %q (have %v)", want, exps)
		}
	}

	// Generic function flagged as a Dendrite pattern.
	var foundGeneric bool
	for _, e := range fa.Exports {
		if e.Name == "Map" {
			for _, p := range e.Patterns {
				if p == "generic_wrapper" {
					foundGeneric = true
				}
			}
		}
	}
	if !foundGeneric {
		t.Errorf("Map should carry generic_wrapper pattern")
	}

	// Endpoint from mux.HandleFunc.
	if len(fa.Endpoints) != 1 || fa.Endpoints[0].Path != "/users" {
		t.Errorf("expected one /users endpoint, got %+v", fa.Endpoints)
	}

	// Imports: 3 total, one internal (embed) resolvable.
	if len(fa.Imports) != 3 {
		t.Fatalf("imports = %d, want 3 (%+v)", len(fa.Imports), fa.Imports)
	}
	idx := BuildIndex([]string{
		"backend/internal/store/store.go",
		"backend/internal/embed/embed.go",
	})
	ResolveImports(fa, idx)
	var fmtExt, httpExt, embedInt *ImportRef
	for i := range fa.Imports {
		switch fa.Imports[i].Specifier {
		case "fmt":
			fmtExt = &fa.Imports[i]
		case "net/http":
			httpExt = &fa.Imports[i]
		case "project-synapse/backend/internal/embed":
			embedInt = &fa.Imports[i]
		}
	}
	if fmtExt == nil || !fmtExt.External {
		t.Errorf("fmt should be external: %+v", fmtExt)
	}
	if httpExt == nil || !httpExt.External {
		t.Errorf("net/http should be external: %+v", httpExt)
	}
	if embedInt == nil || embedInt.External || !embedInt.ResolvedOK ||
		embedInt.Resolved != "backend/internal/embed/embed.go" {
		t.Errorf("internal embed import resolved wrong: %+v", embedInt)
	}
}

const rustSample = `use std::collections::HashMap;
use crate::vault::Vault;
use super::util::{decode, encode};
mod crypto;

pub struct Session {
    id: u32,
}

pub enum State { Open, Closed }

pub trait Store {
    fn get(&self, k: &str) -> Option<String>;
}

impl Session {
    pub fn new(id: u32) -> Self {
        Session { id }
    }
    fn secret(&self) -> u32 { self.id }
}

pub fn login<T: Store>(s: &T) -> bool {
    // a brace in a string should not confuse us: "}"
    true
}

const VERSION: &str = "1.0";
`

func TestParseRust(t *testing.T) {
	fa, err := parseRustFile("src/auth/session.rs", []byte(rustSample))
	if err != nil {
		t.Fatalf("parseRustFile error: %v", err)
	}
	if fa.Language != "rust" {
		t.Fatalf("language = %q, want rust", fa.Language)
	}

	decls := names(fa.Declarations)
	checks := map[string]string{
		"Session": "struct",
		"State":   "enum",
		"Store":   "trait",
		"new":     "method",
		"secret":  "method",
		"login":   "function",
		"VERSION": "const",
	}
	for name, kind := range checks {
		if decls[name] != kind {
			t.Errorf("decl %q kind = %q, want %q (all: %v)", name, decls[name], kind, decls)
		}
	}

	// pub visibility => exported; private `secret` must NOT be exported.
	exps := exportNames(fa.Exports)
	for _, want := range []string{"Session", "State", "Store", "new", "login"} {
		if !exps[want] {
			t.Errorf("missing pub export %q (have %v)", want, exps)
		}
	}
	if exps["secret"] {
		t.Errorf("private fn secret should not be exported")
	}

	// login is generic -> generic_wrapper.
	var generic bool
	for _, e := range fa.Exports {
		if e.Name == "login" {
			for _, p := range e.Patterns {
				if p == "generic_wrapper" {
					generic = true
				}
			}
		}
	}
	if !generic {
		t.Errorf("login should carry generic_wrapper")
	}

	// Imports: HashMap (extern), crate::vault, super::util, mod:crypto.
	specs := map[string]bool{}
	for _, im := range fa.Imports {
		specs[im.Specifier] = true
	}
	for _, want := range []string{"std::collections::HashMap", "crate::vault::Vault", "super::util", "mod:crypto"} {
		if !specs[want] {
			t.Errorf("missing import %q (have %v)", want, specs)
		}
	}

	// Resolution: mod:crypto -> src/auth/crypto.rs ; crate::vault -> src/vault.rs.
	idx := BuildIndex([]string{
		"src/lib.rs",
		"src/auth/session.rs",
		"src/auth/crypto.rs",
		"src/vault.rs",
	})
	ResolveImports(fa, idx)
	for _, im := range fa.Imports {
		switch im.Specifier {
		case "mod:crypto":
			if im.Resolved != "src/auth/crypto.rs" || !im.ResolvedOK {
				t.Errorf("mod:crypto resolved wrong: %+v", im)
			}
		case "crate::vault::Vault":
			if im.Resolved != "src/vault.rs" || !im.ResolvedOK {
				t.Errorf("crate::vault resolved wrong: %+v", im)
			}
		case "std::collections::HashMap":
			if !im.External {
				t.Errorf("std import should be external: %+v", im)
			}
		}
	}
}

// Guard: the dispatcher routes by extension.
func TestMultiParserDispatch(t *testing.T) {
	m := NewMultiParser(nil) // Node parser unused for .go/.rs
	if fa, err := m.Parse(context.Background(), "x.go", []byte("package x\n")); err != nil || fa.Language != "go" {
		t.Errorf("go dispatch failed: fa=%+v err=%v", fa, err)
	}
	if fa, err := m.Parse(context.Background(), "x.rs", []byte("pub fn a() {}\n")); err != nil || fa.Language != "rust" {
		t.Errorf("rust dispatch failed: fa=%+v err=%v", fa, err)
	}
}
