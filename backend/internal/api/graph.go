package api

import (
	"context"
	"fmt"

	"project-synapse/backend/internal/store"
)

// React Flow node/edge shapes. The JSON tags match what React Flow's <ReactFlow>
// component consumes directly: an array of nodes (each with id/position/data)
// and an array of edges (each with id/source/target).

// Position is a React Flow node coordinate.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node is a React Flow node. Type is left empty (the default renderer); the
// semantic kind ("file" | "module" | "endpoint") lives in Data so the frontend
// can style by it.
type Node struct {
	ID       string         `json:"id"`
	Position Position       `json:"position"`
	Data     map[string]any `json:"data"`
}

// Edge is a React Flow edge.
type Edge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Label    string `json:"label,omitempty"`
	Animated bool   `json:"animated,omitempty"`
}

// GraphData is the full React-Flow-ready payload.
type GraphData struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// graphReader is the read surface the builder needs (satisfied by *store.Store).
// A non-empty root scopes the graph to a single ingested repo.
type graphReader interface {
	FilesByRoot(ctx context.Context, root string) ([]store.FileRow, error)
	RelationshipsByRoot(ctx context.Context, root string) ([]store.RelRow, error)
}

// Column x-coordinates for the simple deterministic layout: external modules on
// the left, source files in the middle, endpoints on the right. The frontend is
// free to re-run its own layout; these give a sensible default.
const (
	colModuleX   = 0.0
	colFileX     = 360.0
	colEndpointX = 760.0
	rowGap       = 90.0
	rowTop       = 40.0
)

// BuildGraph reads the code_files + ast_relationships tables and assembles a
// React-Flow-ready node/edge graph. A non-empty root scopes it to one repo.
func BuildGraph(ctx context.Context, r graphReader, root string) (*GraphData, error) {
	files, err := r.FilesByRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	rels, err := r.RelationshipsByRoot(ctx, root)
	if err != nil {
		return nil, err
	}

	g := &GraphData{Nodes: []Node{}, Edges: []Edge{}}

	// --- File nodes (and a lookup of known file paths) --------------------
	fileNodeIdx := make(map[string]int) // path -> index into g.Nodes
	knownFile := make(map[string]bool, len(files))
	fileRow := 0
	for _, f := range files {
		knownFile[f.FilePath] = true
		node := Node{
			ID:       fileID(f.FilePath),
			Position: Position{X: colFileX, Y: rowTop + float64(fileRow)*rowGap},
			Data: map[string]any{
				"label":             f.Filename,
				"kind":              "file",
				"path":              f.FilePath,
				"language":          f.Language,
				"size":              f.SizeBytes,
				"exports":           0,
				"is_myelin_node":    f.Language == "markdown", // markdown "myelin" doc node
				"dendrite_patterns": []string{},               // structural idioms (Dendrite Callouts)
			},
		}
		fileNodeIdx[f.FilePath] = len(g.Nodes)
		g.Nodes = append(g.Nodes, node)
		fileRow++
	}

	moduleNodeID := make(map[string]bool)
	endpointNodeID := make(map[string]bool)
	edgeSeen := make(map[string]bool)
	moduleRow, endpointRow := 0, 0

	addEdge := func(e Edge) {
		if e.ID == "" {
			e.ID = "e:" + e.Source + "->" + e.Target
		}
		if edgeSeen[e.ID] {
			return
		}
		edgeSeen[e.ID] = true
		g.Edges = append(g.Edges, e)
	}

	for _, rel := range rels {
		switch rel.RelationshipType {
		case "exports":
			// Fold export count + dendrite patterns into the owning file node.
			if idx, ok := fileNodeIdx[rel.SourceSymbol]; ok {
				n, _ := g.Nodes[idx].Data["exports"].(int)
				g.Nodes[idx].Data["exports"] = n + 1
				if pats := stringSlice(rel.Metadata["dendrite_patterns"]); len(pats) > 0 {
					existing, _ := g.Nodes[idx].Data["dendrite_patterns"].([]string)
					g.Nodes[idx].Data["dendrite_patterns"] = unionStrings(existing, pats)
				}
			}

		case "imports":
			src := fileID(rel.SourceSymbol)
			external, _ := rel.Metadata["external"].(bool)
			if external {
				modID := moduleID(rel.TargetSymbol)
				if !moduleNodeID[modID] {
					moduleNodeID[modID] = true
					g.Nodes = append(g.Nodes, Node{
						ID:       modID,
						Position: Position{X: colModuleX, Y: rowTop + float64(moduleRow)*rowGap},
						Data:     map[string]any{"label": rel.TargetSymbol, "kind": "module"},
					})
					moduleRow++
				}
				addEdge(Edge{Source: src, Target: modID, Label: "imports"})
			} else if knownFile[rel.TargetSymbol] {
				addEdge(Edge{Source: src, Target: fileID(rel.TargetSymbol), Label: "imports", Animated: true})
			}
			// Unresolved internal imports (target outside the ingest set) are
			// recorded in the DB but intentionally not drawn.

		case "endpoint":
			src := fileID(rel.SourceSymbol)
			epID := endpointID(rel.SourceSymbol, rel.TargetSymbol)
			method, _ := rel.Metadata["method"].(string)
			routePath, _ := rel.Metadata["path"].(string)
			if !endpointNodeID[epID] {
				endpointNodeID[epID] = true
				g.Nodes = append(g.Nodes, Node{
					ID:       epID,
					Position: Position{X: colEndpointX, Y: rowTop + float64(endpointRow)*rowGap},
					Data: map[string]any{
						"label":  rel.TargetSymbol,
						"kind":   "endpoint",
						"method": method,
						"path":   routePath,
					},
				})
				endpointRow++
			}
			label := method
			if label == "" {
				label = "endpoint"
			}
			addEdge(Edge{Source: src, Target: epID, Label: label})
		}
	}

	return g, nil
}

// stringSlice coerces a JSONB array value (decoded as []any) to []string.
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// unionStrings merges two string slices preserving order, de-duplicated.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func fileID(path string) string     { return "file:" + path }
func moduleID(name string) string   { return "module:" + name }
func endpointID(src, t string) string {
	return fmt.Sprintf("endpoint:%s:%s", src, t)
}
