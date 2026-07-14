// Package rag implements the hybrid search + context orchestration layer.
//
// A query runs two retrieval strategies in parallel-by-design:
//   - Vector search: the question is embedded and matched (cosine) against
//     vector_chunks for the top-K most semantically similar code blocks.
//   - Keyword/graph match: literal symbols/routes mentioned in the question
//     (e.g. "/category", "fetchCategories") are matched against code_files and
//     ast_relationships.
//
// Results are de-duplicated and merged into a single context window that pairs
// absolute architectural facts (AST edges) with semantic code fragments, then
// handed to the LLM (or the offline template responder) which must answer in a
// strict JSON contract.
package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	"project-synapse/backend/internal/embed"
	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// QueryAnswer is the mandatory response contract consumed by the frontend.
type QueryAnswer struct {
	Answer           string        `json:"answer"`
	HighlightedFiles []string      `json:"highlighted_files"`
	ExecutionFlow    []string      `json:"execution_flow"`
	Functions        []FunctionHit `json:"functions"` // retrieved symbol-level code
}

// FunctionHit is a symbol-level chunk surfaced to the UI: the function/class the
// query matched, with its source code (header stripped) for the expandable view.
type FunctionHit struct {
	File      string `json:"file"`
	Symbol    string `json:"symbol"`
	ChunkType string `json:"chunk_type"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Code      string `json:"code"`
}

// buildFunctions maps the cited code symbols into UI function hits.
func buildFunctions(rc retrieved) []FunctionHit {
	out := make([]FunctionHit, 0, len(rc.Cites))
	for _, h := range rc.Cites {
		out = append(out, FunctionHit{
			File:      h.FilePath,
			Symbol:    h.SymbolName,
			ChunkType: h.ChunkType,
			StartLine: h.StartLine,
			EndLine:   h.EndLine,
			Code:      store.StripChunkHeader(h.Content),
		})
	}
	return out
}

// --- retrieval ranking -------------------------------------------------------
//
// Raw cosine order alone makes poor citations: roughly a quarter of a repo's
// embedded chunks are one-line constants/variables and markdown doc blocks,
// which are semantically "near" almost any question but explain nothing. So we
// over-fetch a wide candidate pool and rerank it on top of similarity, then split
// the result into the LLM's context window and the symbols we cite to the user.
const (
	candidateFactor  = 6    // over-fetch topK*factor candidates before reranking
	minCandidates    = 30   // ...but never fewer than this
	relevanceDropoff = 0.30 // a hit this far below the best is noise, not an answer

	// Diversity is a preference, not a rule. When a question is genuinely about
	// one module, its file SHOULD supply several symbols — a hard cap would evict
	// the best answers to make room for weaker ones from elsewhere. So each extra
	// chunk from an already-represented file costs a little score, with a generous
	// hard cap only as a backstop.
	maxChunksPerFile = 4
	sameFilePenalty  = 0.05
)

// citableTypes are chunk kinds that represent a real code symbol worth showing to
// the user as evidence. Constants, plain variables, whole-file blobs and markdown
// sections are poor citations for a question about code.
var citableTypes = map[string]bool{
	"function": true, "method": true, "class": true, "struct": true,
	"interface": true, "type": true, "enum": true, "trait": true, "impl": true,
}

// chunkTypePrior nudges ranking toward chunks that actually explain behaviour.
func chunkTypePrior(t string) float64 {
	switch t {
	case "function", "method":
		return 0.10
	case "class", "struct", "impl", "trait":
		return 0.08
	case "interface", "type", "enum":
		return 0.04
	case "myelin_doc":
		return -0.04 // useful background, but rarely the answer to a code question
	case "const", "variable":
		return -0.14
	}
	return 0
}

// substancePrior penalises chunks too small to carry an explanation.
func substancePrior(code string) float64 {
	code = strings.TrimSpace(code)
	if code == "" {
		return -0.25
	}
	switch lines := strings.Count(code, "\n") + 1; {
	case lines <= 1 || len(code) < 60:
		return -0.18 // e.g. `MAX_ITEMS = 100`
	case lines <= 3:
		return -0.06
	}
	return 0
}

// lexicalBoost rewards a chunk whose symbol or filename literally contains a
// term from the question — the strongest cheap signal that it is on-topic.
func lexicalBoost(h store.ChunkHit, toks map[string]bool) float64 {
	if len(toks) == 0 {
		return 0
	}
	var b float64
	if sym := strings.ToLower(h.SymbolName); sym != "" {
		for t := range toks {
			if strings.Contains(sym, t) {
				b += 0.12
				break
			}
		}
	}
	base := strings.ToLower(path.Base(h.FilePath))
	for t := range toks {
		if strings.Contains(base, t) {
			b += 0.06
			break
		}
	}
	return b
}

// testFilePrior mildly demotes test files: they mention the symbols a question
// asks about, but "how does X work" is answered by the implementation, not by a
// fixture. Demoted, not excluded — sometimes a test is the clearest example.
func testFilePrior(p string) float64 {
	lp := strings.ToLower(p)
	b := path.Base(lp)
	if strings.HasSuffix(lp, "_test.go") ||
		strings.HasPrefix(b, "test_") || strings.HasSuffix(b, "_test.py") ||
		strings.Contains(b, ".test.") || strings.Contains(b, ".spec.") ||
		strings.Contains(lp, "/tests/") || strings.Contains(lp, "/__tests__/") ||
		strings.Contains(lp, "/testdata/") {
		return -0.07
	}
	return 0
}

// isLowValue reports a single-line constant/variable declaration — semantically
// close to everything, informative about nothing.
func isLowValue(h store.ChunkHit) bool {
	if h.ChunkType != "const" && h.ChunkType != "variable" {
		return false
	}
	code := strings.TrimSpace(store.StripChunkHeader(h.Content))
	return !strings.Contains(code, "\n")
}

type scoredChunk struct {
	hit   store.ChunkHit
	score float64
}

// rerankChunks scores candidates on similarity + type + substance + lexical
// overlap, returning them best-first.
func rerankChunks(hits []store.ChunkHit, question string) []scoredChunk {
	toks := map[string]bool{}
	for _, t := range extractTokens(question) {
		toks[strings.ToLower(t)] = true
	}
	out := make([]scoredChunk, 0, len(hits))
	for _, h := range hits {
		s := 1 - h.Distance // cosine distance -> similarity
		s += chunkTypePrior(h.ChunkType)
		s += substancePrior(store.StripChunkHeader(h.Content))
		s += lexicalBoost(h, toks)
		s += testFilePrior(h.FilePath)
		out = append(out, scoredChunk{hit: h, score: s})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// selectChunks greedily takes the best `limit` chunks that pass `keep`, applying
// a per-file diversity penalty so no single file crowds out the rest — while
// still letting the file that actually answers the question contribute several
// symbols. Hits far below the best are dropped rather than used as padding.
func selectChunks(scored []scoredChunk, limit int, keep func(store.ChunkHit) bool) []store.ChunkHit {
	pool := scored
	if keep != nil {
		pool = pool[:0:0]
		for _, sc := range scored {
			if keep(sc.hit) {
				pool = append(pool, sc)
			}
		}
	}
	if len(pool) == 0 || limit <= 0 {
		return nil
	}
	floor := pool[0].score - relevanceDropoff
	used := map[string]int{}
	taken := make([]bool, len(pool))
	out := make([]store.ChunkHit, 0, limit)

	for len(out) < limit {
		best, bestScore := -1, math.Inf(-1)
		for i, sc := range pool {
			if taken[i] || sc.score < floor || used[sc.hit.FilePath] >= maxChunksPerFile {
				continue
			}
			eff := sc.score - sameFilePenalty*float64(used[sc.hit.FilePath])
			if eff > bestScore {
				best, bestScore = i, eff
			}
		}
		if best < 0 {
			break
		}
		taken[best] = true
		used[pool[best].hit.FilePath]++
		out = append(out, pool[best].hit)
	}
	return out
}

// retrieve embeds the question, over-fetches candidates, and reranks them into
// (context chunks for the model, cited code symbols for the UI).
func (o *Orchestrator) retrieve(ctx context.Context, question, root string, topK int) (ctxChunks, cites []store.ChunkHit) {
	vecs, err := o.Embedder.Embed(ctx, []string{question})
	if err != nil || len(vecs) == 0 {
		return nil, nil
	}
	n := topK * candidateFactor
	if n < minCandidates {
		n = minCandidates
	}
	hits, err := o.Store.VectorSearch(ctx, vecs[0], n, root)
	if err != nil || len(hits) == 0 {
		return nil, nil
	}
	scored := rerankChunks(hits, question)
	ctxChunks = selectChunks(scored, topK, func(h store.ChunkHit) bool { return !isLowValue(h) })
	cites = selectChunks(scored, topK, func(h store.ChunkHit) bool { return citableTypes[h.ChunkType] })
	return ctxChunks, cites
}

// Orchestrator wires retrieval + generation.
type Orchestrator struct {
	Store    *store.Store
	Embedder embed.Embedder
	Chat     llm.ChatClient // nil => offline template responder
	TopK     int
}

// retrieved holds the merged, de-duplicated context for one query.
type retrieved struct {
	Chunks    []store.ChunkHit // context fragments handed to the model
	Cites     []store.ChunkHit // code symbols surfaced to the user as evidence
	Endpoints []store.RelRow
	Imports   []store.RelRow
	Exports   []store.RelRow
	Files     []string // ordered, de-duplicated candidate file paths
	Facts     []string // human-readable architectural facts
}

const systemPrompt = `You are Project Synapse, a codebase intelligence engine.
Answer the user's question using ONLY the provided context, which combines absolute architectural facts (from an AST dependency graph) with semantic code fragments.

Respond with a SINGLE JSON object and nothing else, matching exactly this shape:
{
  "answer": "A precise markdown explanation answering the question.",
  "highlighted_files": ["array", "of", "exact", "file_paths", "involved"],
  "execution_flow": ["step-by-step", "file", "execution", "path"]
}

Rules:
- Use exact file paths exactly as they appear in the context.
- Do not invent files, routes, or symbols that are not in the context.
- If you cannot determine a field, use an empty array (or a short note for "answer").
- Output valid JSON only — no prose before or after, no markdown code fences.`

// Query runs the hybrid search + generation pipeline, scoped to one repo
// (root == "" searches across every ingested repo).
func (o *Orchestrator) Query(ctx context.Context, question, root string) (*QueryAnswer, error) {
	topK := o.TopK
	if topK <= 0 {
		topK = 5
	}

	// (a) Vector search, over-fetched and reranked.
	hits, cites := o.retrieve(ctx, question, root, topK)

	// (b) Keyword / graph match.
	patterns := toPatterns(extractTokens(question))
	kwFiles, _ := o.Store.KeywordSearchFiles(ctx, patterns, root)
	kwRels, _ := o.Store.KeywordSearchRelationships(ctx, patterns, root)

	rc := mergeContext(hits, cites, kwFiles, kwRels)

	var ans *QueryAnswer
	if o.Chat == nil {
		ans = templateAnswer(question, rc)
	} else {
		var err error
		ans, err = o.llmAnswer(ctx, question, rc)
		if err != nil {
			return nil, err
		}
	}
	// Attach the retrieved symbol-level code regardless of responder, so the UI
	// can show the responsible functions with expandable source.
	ans.Functions = buildFunctions(rc)
	return ans, nil
}

// systemPromptStream asks for plain markdown prose (not JSON) so the answer can
// be streamed token-by-token directly to the UI. The structured fields
// (highlighted_files / execution_flow / functions) are derived from retrieval
// and sent ahead of the stream, so the model only produces the explanation.
const systemPromptStream = `You are Project Synapse, a codebase intelligence engine answering an engineer's question about THEIR specific codebase. You are given architectural facts from an AST dependency graph plus the most relevant real code fragments retrieved from the repo.

Write a precise, actionable answer in GitHub-flavored markdown that an engineer working in this repo could use immediately:
- Open with a direct one or two sentence answer — no preamble, no restating the question.
- Then explain concretely, citing the EXACT file paths and symbols from the context (wrap each in backticks, e.g. ` + "`internal/api/server.go`" + ` or ` + "`handleQuery`" + `). When the question is about behaviour ("how does X work"), trace the actual control/data flow file-by-file using the cited code.
- Lead with specifics visible in the fragments — function names, types, routes, struct fields, SQL — not generic descriptions. Use a short heading, bullets, or a numbered flow only when it genuinely improves clarity.
- Cite the functions, methods, classes, and routes that carry the behaviour. Do NOT present a bare constant, a config value, or a markdown heading as if it explained the logic — if a fragment is just a value, use it only as a supporting detail.
- If the context does not contain the answer, say so in one line and point at the closest relevant file rather than guessing. Never invent files, routes, or symbols that are not in the context.

Do not pad with generic software-engineering advice. Output the markdown answer only — no JSON, no surrounding code fences.`

// QueryStream runs the hybrid retrieval, emits the structured metadata
// (highlighted files / execution flow / retrieved functions) via onMeta, then
// streams the natural-language answer token-by-token via onToken.
func (o *Orchestrator) QueryStream(
	ctx context.Context,
	question, root string,
	onMeta func(*QueryAnswer),
	onToken func(string),
) error {
	topK := o.TopK
	if topK <= 0 {
		topK = 5
	}

	hits, cites := o.retrieve(ctx, question, root, topK)
	patterns := toPatterns(extractTokens(question))
	kwFiles, _ := o.Store.KeywordSearchFiles(ctx, patterns, root)
	kwRels, _ := o.Store.KeywordSearchRelationships(ctx, patterns, root)
	rc := mergeContext(hits, cites, kwFiles, kwRels)

	meta := &QueryAnswer{
		HighlightedFiles: nonNil(rc.Files),
		ExecutionFlow:    nonNil(executionFlow(rc)),
		Functions:        buildFunctions(rc),
	}
	if onMeta != nil {
		onMeta(meta)
	}

	// Offline template responder: emit the deterministic answer in one piece.
	if o.Chat == nil {
		onToken(templateAnswer(question, rc).Answer)
		return nil
	}

	user := assembleContext(rc) + "\n\nQuestion: " + question
	if sc, ok := o.Chat.(llm.StreamingChatClient); ok {
		_, err := sc.Stream(ctx, systemPromptStream, user, onToken)
		return err
	}
	// Non-streaming provider: complete then emit whole.
	raw, err := o.Chat.Complete(ctx, systemPromptStream, user)
	if err != nil {
		return err
	}
	onToken(raw)
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// llmAnswer assembles the context window, calls the model, and parses its JSON.
func (o *Orchestrator) llmAnswer(ctx context.Context, question string, rc retrieved) (*QueryAnswer, error) {
	user := assembleContext(rc) + "\n\nQuestion: " + question
	raw, err := o.Chat.Complete(ctx, systemPrompt, user)
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	ans, perr := parseAnswer(raw)
	if perr != nil {
		// The model didn't return clean JSON — degrade gracefully rather than
		// failing the request, preserving the contract for the frontend.
		return &QueryAnswer{
			Answer:           strings.TrimSpace(raw),
			HighlightedFiles: rc.Files,
			ExecutionFlow:    executionFlow(rc),
		}, nil
	}
	if len(ans.HighlightedFiles) == 0 {
		ans.HighlightedFiles = rc.Files
	}
	if len(ans.ExecutionFlow) == 0 {
		ans.ExecutionFlow = executionFlow(rc)
	}
	return ans, nil
}

// parseAnswer extracts the first JSON object from the model output.
func parseAnswer(raw string) (*QueryAnswer, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}
	var ans QueryAnswer
	if err := json.Unmarshal([]byte(raw[start:end+1]), &ans); err != nil {
		return nil, err
	}
	ans.Answer = llm.CleanMarkdown(ans.Answer)
	return &ans, nil
}

// --- context merge / assembly ----------------------------------------------

// Every language the ingester supports — not just TS/JS, or Go/Rust/Python
// import targets would never be recognised as files and would vanish from the
// highlighted-file set and the execution flow.
var fileExtRe = regexp.MustCompile(`\.(ts|tsx|js|jsx|mjs|cjs|go|rs|py|md|markdown|mdx)$`)

func looksLikeFile(s string) bool { return fileExtRe.MatchString(s) }

// maxExportFacts caps the noisiest fact family: "X exports Y" lines add little
// and, unbounded, crowd endpoints and imports out of the context window.
const maxExportFacts = 12

// mergeContext de-duplicates reranked vector hits, keyword files, and graph edges
// into a single ordered context, and derives architectural facts + the candidate
// file set. Files are ordered by how strongly they answer the question:
// endpoints (entry points), then semantic hits, then graph edges, then keyword
// matches — so the UI highlights and cites the relevant files first.
func mergeContext(hits, cites []store.ChunkHit, kwFiles []store.FileRow, rels []store.RelRow) retrieved {
	rc := retrieved{Chunks: hits, Cites: cites}

	fileSet := newOrderedSet()

	// Endpoint-owning files first (entry points).
	for _, r := range rels {
		if r.RelationshipType == "endpoint" {
			rc.Endpoints = append(rc.Endpoints, r)
			fileSet.add(r.SourceSymbol)
			rc.Facts = append(rc.Facts, factForEndpoint(r))
		}
	}
	// Then the files the semantic search actually matched.
	for _, h := range hits {
		fileSet.add(h.FilePath)
	}

	exportFacts := 0
	for _, r := range rels {
		switch r.RelationshipType {
		case "imports":
			rc.Imports = append(rc.Imports, r)
			fileSet.add(r.SourceSymbol)
			if looksLikeFile(r.TargetSymbol) {
				fileSet.add(r.TargetSymbol)
			}
			rc.Facts = append(rc.Facts, factForImport(r))
		case "exports":
			rc.Exports = append(rc.Exports, r)
			fileSet.add(r.SourceSymbol)
			if exportFacts < maxExportFacts {
				rc.Facts = append(rc.Facts, fmt.Sprintf("%s exports %s", r.SourceSymbol, r.TargetSymbol))
				exportFacts++
			}
		}
	}

	// Keyword file matches last — weakest signal.
	for _, f := range kwFiles {
		fileSet.add(f.FilePath)
	}

	rc.Files = fileSet.items()
	return rc
}

func factForEndpoint(r store.RelRow) string {
	method, _ := r.Metadata["method"].(string)
	path, _ := r.Metadata["path"].(string)
	handler, _ := r.Metadata["handler"].(string)
	src, _ := r.Metadata["source"].(string)
	fact := fmt.Sprintf("%s declares HTTP endpoint %s %s", r.SourceSymbol, method, path)
	if handler != "" {
		fact += fmt.Sprintf(" (handler: %s)", handler)
	}
	if src != "" {
		fact += fmt.Sprintf(" [%s]", src)
	}
	return fact
}

func factForImport(r store.RelRow) string {
	external, _ := r.Metadata["external"].(bool)
	if external {
		return fmt.Sprintf("%s imports external module %s", r.SourceSymbol, r.TargetSymbol)
	}
	return fmt.Sprintf("%s imports %s", r.SourceSymbol, r.TargetSymbol)
}

// assembleContext renders the merged context into the LLM context window.
func assembleContext(rc retrieved) string {
	var b strings.Builder
	b.WriteString("ARCHITECTURAL FACTS (from the AST dependency graph — ground truth, not guesses):\n")
	if len(rc.Facts) == 0 {
		b.WriteString("- (none matched)\n")
	}
	for i, f := range rc.Facts {
		if i >= 40 {
			break
		}
		b.WriteString("- " + f + "\n")
	}

	b.WriteString("\nRELEVANT CODE FRAGMENTS (semantic search over the repo, most relevant first):\n")
	if len(rc.Chunks) == 0 {
		b.WriteString("(none)\n")
	}
	for i, h := range rc.Chunks {
		content := h.Content
		if len(content) > 1800 {
			content = content[:1800] + "\n…(truncated)"
		}
		loc := h.FilePath
		if h.SymbolName != "" {
			loc += " :: " + h.SymbolName
		}
		if h.StartLine > 0 {
			loc += fmt.Sprintf(" (lines %d-%d)", h.StartLine, h.EndLine)
		}
		b.WriteString(fmt.Sprintf("\n[fragment %d — %s]\n%s\n", i+1, loc, content))
	}
	return b.String()
}

// executionFlow derives an ordered file execution path from the graph edges.
func executionFlow(rc retrieved) []string {
	var flow []string
	for _, e := range rc.Endpoints {
		method, _ := e.Metadata["method"].(string)
		path, _ := e.Metadata["path"].(string)
		flow = append(flow, fmt.Sprintf("Request → %s %s handled in %s", method, path, e.SourceSymbol))
	}
	for _, imp := range rc.Imports {
		if looksLikeFile(imp.TargetSymbol) {
			flow = append(flow, fmt.Sprintf("%s → %s", imp.SourceSymbol, imp.TargetSymbol))
		}
	}
	if len(flow) == 0 {
		flow = append([]string{}, rc.Files...)
	}
	return flow
}

// templateAnswer builds the contract JSON deterministically (offline mode),
// synthesising a readable markdown answer from the retrieved graph + chunks.
func templateAnswer(question string, rc retrieved) *QueryAnswer {
	var b strings.Builder
	fmt.Fprintf(&b, "Based on the codebase knowledge graph and %d semantic code fragment(s):\n\n", len(rc.Chunks))

	if len(rc.Endpoints) > 0 {
		b.WriteString("**Matching endpoints**\n")
		for _, e := range rc.Endpoints {
			method, _ := e.Metadata["method"].(string)
			path, _ := e.Metadata["path"].(string)
			handler, _ := e.Metadata["handler"].(string)
			fmt.Fprintf(&b, "- `%s %s` is handled in `%s`", method, path, e.SourceSymbol)
			if handler != "" {
				fmt.Fprintf(&b, " by `%s`", handler)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(rc.Files) > 0 {
		b.WriteString("**Files involved:** ")
		b.WriteString("`" + strings.Join(rc.Files, "`, `") + "`\n\n")
	}

	if len(rc.Imports) > 0 {
		b.WriteString("**Dependency edges**\n")
		shown := 0
		for _, imp := range rc.Imports {
			b.WriteString("- " + factForImport(imp) + "\n")
			if shown++; shown >= 8 {
				break
			}
		}
		b.WriteString("\n")
	}

	if len(rc.Endpoints) == 0 && len(rc.Files) == 0 && len(rc.Chunks) == 0 {
		b.WriteString("No matching code structures were found for this question. Try ingesting the relevant directory or rephrasing with a concrete file, route, or symbol name.\n")
	}

	b.WriteString("\n_(Offline deterministic synthesis — configure ANTHROPIC_API_KEY or OPENAI_API_KEY for a full natural-language answer.)_")

	return &QueryAnswer{
		Answer:           b.String(),
		HighlightedFiles: rc.Files,
		ExecutionFlow:    executionFlow(rc),
	}
}

// --- token extraction -------------------------------------------------------

var (
	routeRe = regexp.MustCompile(`/[A-Za-z0-9_\-/.]+`)
	identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
)

var stopwords = map[string]bool{
	"where": true, "what": true, "which": true, "how": true, "the": true, "and": true,
	"are": true, "for": true, "with": true, "this": true, "that": true, "does": true,
	"route": true, "routes": true, "handled": true, "handle": true, "file": true,
	"files": true, "code": true, "find": true, "show": true, "from": true, "into": true,
	"function": true, "functions": true, "method": true, "class": true, "between": true,
	"about": true, "when": true, "used": true, "uses": true, "have": true, "has": true,
	// Generic filler that otherwise ILIKE-matches most of the repo.
	"explain": true, "work": true, "works": true, "working": true, "implement": true,
	"implemented": true, "project": true, "repo": true, "repository": true,
	"codebase": true, "system": true, "please": true, "tell": true, "give": true,
	"list": true, "there": true, "their": true, "would": true, "should": true,
	"could": true, "want": true, "need": true, "using": true, "make": true,
}

// extractTokens pulls candidate literals from the question: route-like paths
// (always kept) and identifiers (minus generic stopwords).
func extractTokens(question string) []string {
	set := newOrderedSet()
	for _, r := range routeRe.FindAllString(question, -1) {
		set.add(strings.TrimRight(r, "."))
	}
	for _, w := range identRe.FindAllString(question, -1) {
		if !stopwords[strings.ToLower(w)] {
			set.add(w)
		}
	}
	items := set.items()
	if len(items) > 12 {
		items = items[:12]
	}
	return items
}

func toPatterns(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, "%"+t+"%")
	}
	return out
}

// --- small ordered-set helper ----------------------------------------------

type orderedSet struct {
	seen  map[string]bool
	order []string
}

func newOrderedSet() *orderedSet { return &orderedSet{seen: map[string]bool{}} }

func (s *orderedSet) add(v string) {
	if v == "" || s.seen[v] {
		return
	}
	s.seen[v] = true
	s.order = append(s.order, v)
}

func (s *orderedSet) items() []string { return s.order }
