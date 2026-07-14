// Package architecture generates a high-level system-architecture view of a
// repo: the LLM clusters files into architectural components (with a layer and
// description) and draws data-flow edges between them. A deterministic
// folder/dependency clustering is used as a fallback when no LLM is configured.
package architecture

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// Component is one architectural building block / subsystem.
type Component struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Layer       string   `json:"layer"`       // frontend | backend | data | external | shared
	Description string   `json:"description"` // its responsibility, in one precise sentence
	Tech        []string `json:"tech"`        // key technologies/libraries it uses
	Files       []string `json:"files"`
}

// Edge is a directed data/control-flow relationship between components: Label is
// the mechanism (HTTP, SQL, embeds…) and Description says what data flows and why.
type Edge struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Architecture is the full generated system-design view for one repo.
type Architecture struct {
	Repo       string      `json:"repo"`
	Name       string      `json:"name"`
	Summary    string      `json:"summary"`
	Pattern    string      `json:"pattern,omitempty"` // the architectural style, in a few words
	Design     string      `json:"design,omitempty"`  // markdown: the system-design narrative
	Components []Component `json:"components"`
	Edges      []Edge      `json:"edges"`
}

// Engine builds the architecture view from the store + an (optional) LLM.
type Engine struct {
	Store *store.Store
	Chat  llm.ChatClient

	mu    sync.Mutex
	cache map[string]*Architecture
}

const archSystem = `You are a principal software architect. From a summary of a repository (file tree, external dependencies, the import edges between top-level modules, and HTTP endpoints), reconstruct and EXPLAIN the system design — not individual files.

Group the code into a small number (typically 4-8) of architectural components/subsystems. For each: an id, display name, a "layer" (one of frontend | backend | data | external | shared), a precise one-sentence RESPONSIBILITY (what it owns), the key technologies it uses, and the real folders/paths it covers. Then draw directed edges for the actual data/control flow between components — each edge gets a short label (the MECHANISM: "HTTP/JSON", "SQL", "embeds", "spawns", "imports", "SSE") and a one-line description of WHAT data flows across it and WHY. Finally identify the overall architectural PATTERN and write a DESIGN narrative.

Respond with ONE JSON object, nothing else:
{
  "summary": "one or two sentence overview of what the system does",
  "pattern": "the architectural style in a few words, e.g. 'Layered monolith with an async ingestion + RAG pipeline'",
  "design": "markdown, 2-4 short paragraphs: explain the system design — trace the request/data lifecycle end to end, say why the components are split this way, and call out the key cross-cutting flows. Use code spans for real paths and **bold** for component names.",
  "components": [
    { "id": "kebab-id", "name": "Display Name", "layer": "backend", "description": "the responsibility in one precise sentence", "tech": ["lib","lib"], "files": ["path/a","path/b"] }
  ],
  "edges": [ { "source": "id-a", "target": "id-b", "label": "SQL", "description": "what data flows here and why" } ]
}

Rules: use ONLY paths/dependencies present in the summary. Every edge source/target MUST be a component id you defined. Be specific and technical — never use filler like "handles logic" or "manages data". Output valid JSON only — no prose, no code fences.`

// Generate returns the architecture for one repo root, building (and caching) it
// on first request. refresh forces regeneration.
func (e *Engine) Generate(ctx context.Context, root string, refresh bool) (*Architecture, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repo is required")
	}
	e.mu.Lock()
	if e.cache == nil {
		e.cache = map[string]*Architecture{}
	}
	if !refresh {
		if a, ok := e.cache[root]; ok {
			e.mu.Unlock()
			return a, nil
		}
	}
	e.mu.Unlock()

	files, err := e.Store.FilesByRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found for repo")
	}
	rels, _ := e.Store.RelationshipsByRoot(ctx, root)
	name := repoName(root)

	var arch *Architecture
	if e.Chat != nil {
		arch = e.llmArchitecture(ctx, name, files, rels)
	}
	if arch == nil || len(arch.Components) == 0 {
		arch = fallbackArchitecture(name, files, rels)
	}
	arch.Repo = root
	arch.Name = name

	// Never marshal nil slices as JSON null — the UI maps over these.
	if arch.Components == nil {
		arch.Components = []Component{}
	}
	for i := range arch.Components {
		if arch.Components[i].Files == nil {
			arch.Components[i].Files = []string{}
		}
		if arch.Components[i].Tech == nil {
			arch.Components[i].Tech = []string{}
		}
	}
	if arch.Edges == nil {
		arch.Edges = []Edge{}
	}

	e.mu.Lock()
	e.cache[root] = arch
	e.mu.Unlock()
	return arch, nil
}

func (e *Engine) llmArchitecture(ctx context.Context, name string, files []store.FileRow, rels []store.RelRow) *Architecture {
	raw, err := e.Chat.Complete(ctx, archSystem, buildSummary(name, files, rels))
	if err != nil {
		return nil
	}
	var parsed Architecture
	if err := json.Unmarshal([]byte(extractJSON(raw)), &parsed); err != nil {
		return nil
	}
	// Repair models that double-escape newlines inside the JSON strings.
	parsed.Summary = llm.CleanMarkdown(parsed.Summary)
	parsed.Design = llm.CleanMarkdown(parsed.Design)
	for i := range parsed.Components {
		parsed.Components[i].Description = llm.CleanMarkdown(parsed.Components[i].Description)
	}
	// Drop edges that reference unknown components.
	ids := map[string]bool{}
	for _, c := range parsed.Components {
		ids[c.ID] = true
	}
	var edges []Edge
	for _, ed := range parsed.Edges {
		if ids[ed.Source] && ids[ed.Target] && ed.Source != ed.Target {
			ed.Description = llm.CleanMarkdown(ed.Description)
			edges = append(edges, ed)
		}
	}
	parsed.Edges = edges
	return &parsed
}

// buildSummary renders a compact, bounded summary for the LLM: file tree +
// inter-folder import edges + endpoints.
func buildSummary(name string, files []store.FileRow, rels []store.RelRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n\nFiles (%d):\n", name, len(files))
	for i, f := range files {
		if i >= 150 {
			fmt.Fprintf(&b, "- …and %d more\n", len(files)-i)
			break
		}
		fmt.Fprintf(&b, "- %s\n", f.FilePath)
	}

	externals := map[string]bool{}
	folderEdges := map[string]bool{}
	var endpoints []string
	for _, r := range rels {
		switch r.RelationshipType {
		case "imports":
			if ext, _ := r.Metadata["external"].(bool); ext {
				if spec, _ := r.Metadata["specifier"].(string); spec != "" {
					externals[spec] = true
				} else {
					externals[r.TargetSymbol] = true
				}
				continue
			}
			// Internal import → an inter-module coupling, the real structure.
			sf, tf := topFolder(r.SourceSymbol), topFolder(r.TargetSymbol)
			if sf != "" && tf != "" && sf != tf {
				folderEdges[sf+" → "+tf] = true
			}
		case "endpoint":
			endpoints = append(endpoints, r.TargetSymbol)
		}
	}
	if len(externals) > 0 {
		ext := make([]string, 0, len(externals))
		for x := range externals {
			ext = append(ext, x)
		}
		sort.Strings(ext)
		if len(ext) > 40 {
			ext = ext[:40]
		}
		fmt.Fprintf(&b, "\nExternal dependencies (tech stack): %s\n", strings.Join(ext, ", "))
	}
	if len(folderEdges) > 0 {
		keys := make([]string, 0, len(folderEdges))
		for k := range folderEdges {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\nInternal module dependencies (which top-level module imports which):\n")
		for i, k := range keys {
			if i >= 60 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", k)
		}
	}
	if len(endpoints) > 0 {
		b.WriteString("\nHTTP endpoints (METHOD /path):\n")
		for i, ep := range endpoints {
			if i >= 50 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", ep)
		}
	}
	return b.String()
}

// fallbackArchitecture clusters by top-level folder with import edges between
// folders — a structural approximation used when no LLM is available.
func fallbackArchitecture(name string, files []store.FileRow, rels []store.RelRow) *Architecture {
	byFolder := map[string][]string{}
	var order []string
	for _, f := range files {
		top := topFolder(f.FilePath)
		if _, ok := byFolder[top]; !ok {
			order = append(order, top)
		}
		byFolder[top] = append(byFolder[top], f.FilePath)
	}
	sort.Strings(order)

	comps := make([]Component, 0, len(order))
	for _, folder := range order {
		comps = append(comps, Component{
			ID:          slug(folder),
			Name:        folder,
			Layer:       guessLayer(folder),
			Description: fmt.Sprintf("%d files under %s", len(byFolder[folder]), folder),
			Tech:        []string{},
			Files:       byFolder[folder],
		})
	}

	// Edges: an import whose source + target fall in different top-level folders.
	seen := map[string]bool{}
	var edges []Edge
	for _, r := range rels {
		if r.RelationshipType != "imports" {
			continue
		}
		if ext, _ := r.Metadata["external"].(bool); ext {
			continue
		}
		sFolder, tFolder := topFolder(r.SourceSymbol), topFolder(r.TargetSymbol)
		sf, tf := slug(sFolder), slug(tFolder)
		if sf == "" || tf == "" || sf == tf {
			continue
		}
		key := sf + "->" + tf
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, Edge{
			Source:      sf,
			Target:      tf,
			Label:       "imports",
			Description: fmt.Sprintf("%s/ imports modules from %s/", sFolder, tFolder),
		})
	}

	return &Architecture{
		Name:       name,
		Summary:    fmt.Sprintf("Structural view of %s, grouped by top-level module with import edges between them.", name),
		Pattern:    "Module structure (derived from the folder layout + import graph)",
		Design:     fmt.Sprintf("This is a deterministic structural view of **%s** — its top-level modules and how they import one another. Configure an LLM provider (Anthropic / OpenAI / OpenRouter / Ollama) for a written system-design narrative that names the data flows and the architectural pattern.", name),
		Components: comps,
		Edges:      edges,
	}
}

func guessLayer(folder string) string {
	switch strings.ToLower(folder) {
	case "client", "frontend", "web", "ui", "app", "components", "pages":
		return "frontend"
	case "server", "backend", "api", "routes", "controllers", "services":
		return "backend"
	case "db", "database", "models", "store", "data", "migrations":
		return "data"
	default:
		return "shared"
	}
}

// --- helpers ----------------------------------------------------------------

const rootFolder = "(root)"

func topFolder(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return rootFolder
}

func repoName(root string) string {
	r := strings.TrimRight(root, `/\`)
	if i := strings.LastIndexAny(r, `/\`); i >= 0 {
		return r[i+1:]
	}
	return r
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
