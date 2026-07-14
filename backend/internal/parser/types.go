package parser

// This file defines the structural datasets extracted from each source file.
// They are produced by the NodeParser (a true TypeScript-compiler AST walk,
// see node_parser.go) and consumed by the persistence layer.

// ImportRef is a single module dependency edge discovered in a file.
//
// The Specifier is exactly what appeared in source ("react", "./db",
// "@scope/pkg/sub"). The Resolved/External/ResolvedOK fields are filled in by
// the import resolver (resolve.go) after the whole batch is known:
//   - External=true  -> Resolved holds the external package root ("react").
//   - External=false -> Resolved holds the relative path of the target file in
//     the ingest set (ResolvedOK=true), or "" when it points outside the set.
//
// Symbols holds the names as EXPORTED BY THE TARGET, not the local bindings:
// `import { A as B }` / `from m import a as b` record "A" / "a". The original
// name is what matches the target's exports (and, in Python, what may name a
// submodule); the local alias matches nothing.
type ImportRef struct {
	Specifier string   `json:"specifier"`
	Symbols   []string `json:"symbols"`
	Kind      string   `json:"kind"` // import | from | reexport | dynamic | require | use
	Line      int      `json:"line"`
	// Deferred marks an import that does not execute at module load time — a
	// Python function-body import, the standard cycle-breaking idiom. It is still
	// a real dependency (reachability counts it) but cannot cause an import-time
	// cycle, so cycle detection ignores it.
	Deferred   bool   `json:"deferred,omitempty"`
	Resolved   string `json:"-"`
	External   bool   `json:"-"`
	ResolvedOK bool   `json:"-"`
}

// ExportRef is a declared, exported symbol.
type ExportRef struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // function | class | interface | type | enum | variable | named | default
	IsDefault bool     `json:"isDefault"`
	Line      int      `json:"line"`
	Patterns  []string `json:"patterns"` // Dendrite Callouts: decorator | closure | generic_wrapper | ...
}

// Declaration is a top-level declaration with its source line span. It is the
// structural boundary the semantic chunker slices on.
type Declaration struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // function | class | interface | type | enum | variable
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Exported  bool   `json:"exported"`
}

// EndpointRef is an HTTP route signature.
type EndpointRef struct {
	Method  string `json:"method"` // GET | POST | PUT | DELETE | PATCH | ...
	Path    string `json:"path"`   // "/users" for router style; "" for next-app-router (derived later)
	Handler string `json:"handler"`
	Source  string `json:"source"` // router | next-app-router
	Line    int    `json:"line"`
}

// CallEdge is an intra-file call relationship: a top-level declaration (Caller)
// references/invokes another top-level declaration (Callee) in the same file.
// Drives the function-level call-flow graph in the UI.
type CallEdge struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
}

// FileAnalysis is the complete structural extraction for one source file.
type FileAnalysis struct {
	RelPath   string        // path relative to the ingestion root (forward slashes)
	Filename  string        // basename
	Language  string        // typescript | typescript-react | javascript | ...
	Content   string        // raw file contents
	Hash      string        // sha256 hex of contents
	SizeBytes int64         // byte length of contents
	Imports      []ImportRef   `json:"imports"`
	Exports      []ExportRef   `json:"exports"`
	Endpoints    []EndpointRef `json:"endpoints"`
	Declarations []Declaration `json:"declarations"`
	Calls        []CallEdge    `json:"calls"`
}
