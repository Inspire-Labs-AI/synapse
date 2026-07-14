// Go language support for the ingestion pipeline.
//
// Unlike TypeScript (which is parsed out-of-process by the Node tsc AST), Go is
// parsed IN-PROCESS using the standard library's go/parser — a true,
// compiler-grade AST with zero extra dependencies. It produces the same
// FileAnalysis contract (imports / exports / declarations / endpoints) every
// other language flows through.
package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// parseGoFile extracts structure from a single Go source file.
func parseGoFile(relPath string, content []byte) (*FileAnalysis, error) {
	rel := filepath.ToSlash(relPath)
	sum := sha256.Sum256(content)
	fa := &FileAnalysis{
		RelPath:      rel,
		Filename:     filepath.Base(rel),
		Language:     "go",
		Content:      string(content),
		Hash:         hex.EncodeToString(sum[:]),
		SizeBytes:    int64(len(content)),
		Imports:      []ImportRef{},
		Exports:      []ExportRef{},
		Endpoints:    []EndpointRef{},
		Declarations: []Declaration{},
		Calls:        []CallEdge{},
	}

	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, rel, content, goparser.ParseComments)
	if file == nil {
		// Total parse failure: keep the file node, drop its edges (non-fatal).
		return fa, fmt.Errorf("parse go %s: %w", rel, err)
	}
	lineOf := func(p token.Pos) int { return fset.Position(p).Line }

	// --- Imports (package paths; resolved to internal dirs later) -------------
	for _, imp := range file.Imports {
		spec := strings.Trim(imp.Path.Value, "`\"")
		if spec == "" {
			continue
		}
		sym := pkgBase(spec)
		if imp.Name != nil && imp.Name.Name != "" {
			sym = imp.Name.Name // aliased / dot / underscore import
		}
		fa.Imports = append(fa.Imports, ImportRef{
			Specifier: spec,
			Symbols:   []string{sym},
			Kind:      "import",
			Line:      lineOf(imp.Pos()),
		})
	}

	// --- Declarations + exports + endpoints -----------------------------------
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := "function"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				if rt := recvTypeName(d.Recv.List[0].Type); rt != "" {
					name = rt + "." + d.Name.Name
				}
			}
			start, end := lineOf(d.Pos()), lineOf(d.End())
			patterns := goFuncPatterns(d)
			fa.Declarations = append(fa.Declarations, Declaration{
				Name: name, Kind: kind, StartLine: start, EndLine: end, Exported: d.Name.IsExported(),
			})
			if d.Name.IsExported() {
				fa.Exports = append(fa.Exports, ExportRef{Name: name, Kind: kind, Line: start, Patterns: patterns})
			}
			collectGoEndpoints(fset, d, &fa.Endpoints)

		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue // handled via file.Imports
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					patterns := []string{}
					if s.TypeParams != nil && len(s.TypeParams.List) > 0 {
						patterns = append(patterns, "generic_wrapper")
					}
					start, end := lineOf(s.Pos()), lineOf(s.End())
					fa.Declarations = append(fa.Declarations, Declaration{
						Name: s.Name.Name, Kind: kind, StartLine: start, EndLine: end, Exported: s.Name.IsExported(),
					})
					if s.Name.IsExported() {
						fa.Exports = append(fa.Exports, ExportRef{Name: s.Name.Name, Kind: kind, Line: start, Patterns: patterns})
					}
				case *ast.ValueSpec:
					kind := "variable"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if n.Name == "_" {
							continue
						}
						start, end := lineOf(s.Pos()), lineOf(s.End())
						fa.Declarations = append(fa.Declarations, Declaration{
							Name: n.Name, Kind: kind, StartLine: start, EndLine: end, Exported: n.IsExported(),
						})
						if n.IsExported() {
							fa.Exports = append(fa.Exports, ExportRef{Name: n.Name, Kind: kind, Line: start})
						}
					}
				}
			}
		}
	}

	// --- Intra-file call graph -------------------------------------------------
	callable := map[string]bool{}
	for _, d := range fa.Declarations {
		if !strings.Contains(d.Name, ".") { // functions + types (methods are qualified)
			callable[d.Name] = true
		}
	}
	callSeen := map[string]bool{}
	addCall := func(caller, callee string) {
		if callee == "" || callee == caller || !callable[callee] {
			return
		}
		k := caller + ">" + callee
		if callSeen[k] {
			return
		}
		callSeen[k] = true
		fa.Calls = append(fa.Calls, CallEdge{Caller: caller, Callee: callee})
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		caller := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			if rt := recvTypeName(fn.Recv.List[0].Type); rt != "" {
				caller = rt + "." + fn.Name.Name
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok {
					addCall(caller, id.Name) // direct call foo()
				}
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); ok {
					addCall(caller, id.Name) // construction Foo{...}
				}
			}
			return true
		})
	}

	return fa, nil
}

// recvTypeName extracts the base type name from a method receiver, unwrapping
// pointers and generic instantiations: (s *Store) -> "Store", (l List[T]) -> "List".
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// goFuncPatterns flags Dendrite idioms on a Go function: generics and closures.
func goFuncPatterns(d *ast.FuncDecl) []string {
	p := []string{}
	if d.Type != nil && d.Type.TypeParams != nil && len(d.Type.TypeParams.List) > 0 {
		p = append(p, "generic_wrapper")
	}
	if d.Body != nil && returnsFuncLit(d.Body) {
		p = append(p, "closure")
	}
	return p
}

func returnsFuncLit(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for _, r := range ret.Results {
				if _, ok := r.(*ast.FuncLit); ok {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// goRouterVerbs maps the method names of common Go HTTP routers to a verb.
var goRouterVerbs = map[string]string{
	"GET": "GET", "POST": "POST", "PUT": "PUT", "DELETE": "DELETE",
	"PATCH": "PATCH", "HEAD": "HEAD", "OPTIONS": "OPTIONS",
	"Get": "GET", "Post": "POST", "Put": "PUT", "Delete": "DELETE",
	"Patch": "PATCH", "Head": "HEAD", "Options": "OPTIONS",
}

// collectGoEndpoints finds HTTP route registrations inside a function body:
// net/http's HandleFunc/Handle and chi/gin/echo-style .GET("/x", ...) calls.
func collectGoEndpoints(fset *token.FileSet, d *ast.FuncDecl, out *[]EndpointRef) {
	if d.Body == nil {
		return
	}
	ast.Inspect(d.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method := ""
		switch sel.Sel.Name {
		case "HandleFunc", "Handle":
			method = "ANY"
		default:
			method = goRouterVerbs[sel.Sel.Name]
		}
		if method == "" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		route := strings.Trim(lit.Value, "`\"")
		if !strings.HasPrefix(route, "/") {
			return true
		}
		*out = append(*out, EndpointRef{
			Method:  method,
			Path:    route,
			Handler: d.Name.Name,
			Source:  "go-router",
			Line:    fset.Position(call.Pos()).Line,
		})
		return true
	})
}

// pkgBase returns the importable package name (last path segment).
func pkgBase(importPath string) string {
	importPath = strings.TrimSuffix(importPath, "/")
	if i := strings.LastIndex(importPath, "/"); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}
