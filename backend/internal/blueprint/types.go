// Package blueprint implements the AI Capability Discovery engine: it turns a
// natural-language feature description into a structural reuse blueprint,
// scoring each required entity/action against the existing codebase topology
// as Green (reuse), Yellow (extend), or Red (build new).
package blueprint

// Category is the structural-confidence tier for a required capability.
type Category string

const (
	CategoryGreen  Category = "green"  // exact match / high confidence — reuse directly
	CategoryYellow Category = "yellow" // partial match — extend an existing structure
	CategoryRed    Category = "red"    // net-new gap — build from scratch
)

// Confidence thresholds for the matrix.
const (
	GreenThreshold  = 0.85
	YellowThreshold = 0.40
)

// Intent is one extracted technical intent (an entity or an action).
type Intent struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// IntentBreakdown is the structured decomposition of a feature description.
type IntentBreakdown struct {
	Entities []Intent `json:"entities"`
	Actions  []Intent `json:"actions"`
}

// Match is the reusability score for a single intent against the codebase.
type Match struct {
	Kind           string   `json:"kind"` // "entity" | "action"
	Name           string   `json:"name"`
	Category       Category `json:"category"`
	Confidence     float64  `json:"confidence"`
	Files          []string `json:"files"`     // existing file paths backing this match
	Symbols        []string `json:"symbols"`   // matched declared symbols
	Endpoints      []string `json:"endpoints"` // matched routes
	Recommendation string   `json:"recommendation"`

	// structural is the subset of Files that contain a *dedicated* structure
	// (a matching symbol/endpoint/filename) rather than a mere content mention.
	// Used to scope Green highlights tightly. Unexported (not serialised).
	structural []string
}

// Summary is the aggregate reuse picture.
type Summary struct {
	Green      int     `json:"green"`
	Yellow     int     `json:"yellow"`
	Red        int     `json:"red"`
	Total      int     `json:"total"`
	ReuseScore float64 `json:"reuse_score"` // 0..1, (green + 0.5*yellow)/total
}

// Highlights lists existing React Flow node ids to recolor on the canvas.
type Highlights struct {
	Green  []string `json:"green"`
	Yellow []string `json:"yellow"`
}

// GapNode is a speculative new node (Red) the engineer must build.
type GapNode struct {
	ID            string `json:"id"`    // "gap:<slug>"
	Label         string `json:"label"`
	Kind          string `json:"kind"`  // entity | action
	Reason        string `json:"reason"`
	SuggestedFile string `json:"suggested_file"`
}

// GapEdge is a dashed connector mapping a gap into the existing framework.
type GapEdge struct {
	Source string `json:"source"` // gap id
	Target string `json:"target"` // existing node id ("file:<path>") or another gap id
}

// DiffItem is one row of the engineer-facing diff summary.
type DiffItem struct {
	File       string   `json:"file"`
	ChangeType string   `json:"change_type"` // "extend" | "create"
	Category   Category `json:"category"`
	Detail     string   `json:"detail"`
}

// Response is the full discovery payload (API + canvas + side panel).
type Response struct {
	Description string          `json:"description"`
	Intents     IntentBreakdown `json:"intents"`
	Matches     []Match         `json:"matches"`
	Summary     Summary         `json:"summary"`
	Highlights  Highlights      `json:"highlights"`
	Gaps        []GapNode       `json:"gaps"`
	GapEdges    []GapEdge       `json:"gap_edges"`
	DiffSummary []DiffItem      `json:"diff_summary"`
}
