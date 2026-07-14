package bugs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// target is one file selected for deep (Tier-2) analysis, with why it was picked.
type target struct {
	file   string
	reason string
}

// pickTargets ranks files by risk for the (token-bounded) LLM pass: Tier-1
// suspects first, then HTTP entry points, then high-fan-in hubs.
func (e *Engine) pickTargets(g *graph, funcs []store.FuncCodeRow, found []Bug) []target {
	maxN := e.MaxLLM
	if maxN <= 0 {
		maxN = 8
	}
	score := map[string]int{}
	reason := map[string]string{}
	bump := func(file string, pts int, why string) {
		if file == "" {
			return
		}
		score[file] += pts
		if _, ok := reason[file]; !ok {
			reason[file] = why
		}
	}
	for _, b := range found {
		bump(b.Location.File, 5, "flagged by a deterministic scan ("+b.Category+")")
	}
	for f := range g.endpoints {
		bump(f, 4, "an HTTP entry point (handles external input)")
	}
	for f, imps := range g.importers {
		if len(imps) >= 3 {
			bump(f, 2, fmt.Sprintf("high fan-in (%d importers)", len(imps)))
		}
	}

	hasCode := map[string]bool{}
	for _, r := range funcs {
		hasCode[r.File] = true
	}
	var ts []target
	for f := range score {
		if hasCode[f] {
			ts = append(ts, target{file: f, reason: reason[f]})
		}
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if score[ts[i].file] != score[ts[j].file] {
			return score[ts[i].file] > score[ts[j].file]
		}
		return ts[i].file < ts[j].file
	})
	if len(ts) > maxN {
		ts = ts[:maxN]
	}
	return ts
}

// buildContext assembles the dense payload for one target: its code, its direct
// dependencies' signatures, and pgvector-similar code/doc chunks from the repo.
func (e *Engine) buildContext(ctx context.Context, root string, t target, g *graph, byFile map[string][]store.FuncCodeRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TARGET FILE: %s\nWhy flagged: %s\n\n", t.file, t.reason)

	// The file head (imports + top-level declarations) tells the model where
	// symbols come from — without it, aliased imports like `import { X as Y }`
	// read as "undefined variable" and produce false positives.
	if head := fileHead(root, t.file, 50); head != "" {
		b.WriteString("=== FILE HEAD (imports & top-level declarations; symbols below resolve against these) ===\n")
		b.WriteString(truncate(head, 2600))
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "=== TARGET CODE (%s) ===\n", baseName(t.file))
	b.WriteString(truncate(concatCode(byFile[t.file]), 6000))

	deps := uniqueStrings(g.importsOf[t.file])
	if len(deps) > 0 {
		b.WriteString("\n\n=== DIRECT DEPENDENCIES (signatures) ===\n")
		for i, d := range deps {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "// %s\n%s\n", d, signatures(byFile[d], 6))
		}
	}

	// pgvector similarity: pull related code + docs the target may interact with.
	if e.Embedder != nil {
		if vecs, err := e.Embedder.Embed(ctx, []string{truncate(concatCode(byFile[t.file]), 4000)}); err == nil && len(vecs) > 0 {
			if hits, herr := e.Store.VectorSearch(ctx, vecs[0], 6, root); herr == nil {
				wrote := false
				for _, h := range hits {
					if h.FilePath == t.file {
						continue
					}
					if !wrote {
						b.WriteString("\n\n=== RELATED CODE / DOCS (semantic search) ===\n")
						wrote = true
					}
					fmt.Fprintf(&b, "\n[%s :: %s]\n%s\n", h.FilePath, h.SymbolName, truncate(store.StripChunkHeader(h.Content), 700))
				}
			}
		}
	}
	return b.String()
}

const redTeamSystem = `You are an adversarial staff security + reliability engineer red-teaming ONE code unit for a CRITICAL, CONCRETE defect. You receive the target file's head (imports + top-level declarations), the target code, its direct dependencies, and related code from the same repository.

Hunt for REAL, specific defects only: logic errors, unhandled errors / nil or null dereferences, race conditions & unsafe concurrency, resource leaks, injection / auth / SSRF / path-traversal, broken invariants, or unhandled states. Ignore style, naming, and purely hypothetical issues. Ground every claim in the code shown — cite the exact symbol.

CRITICAL — you are shown EXTRACTED, possibly TRUNCATED snippets, not the whole program. You CANNOT see every definition. Therefore you MUST NOT report symbol-resolution issues: do NOT claim a variable/function/type is "undefined", "not defined in the provided code", "missing import", "not declared", or "not in scope". You MUST NOT claim a function is "incomplete", "missing its body", "empty", "not implemented", "truncated", or has a "syntax error" — a snippet may simply be cut off; assume the body exists. Assume any symbol you cannot see is correctly imported or defined elsewhere. Note that ` + "`import { A as B }`" + ` defines B, and ` + "`import X`" + ` / ` + "`const { x } = …`" + ` define their bindings. Only report a defect when it is provable from the code actually shown — if you are not highly confident it is a real bug, set "found": false.

GUARDS — before reporting, look for the guard. If the shown code already handles the condition, there is NO defect and you must set "found": false:
- An explicit ` + "`if x is None`" + ` / ` + "`if err != nil`" + ` / ` + "`if not x`" + ` check before the risky use.
- A ` + "`try`/`except`" + ` (or ` + "`catch`" + `) already wrapping the call you would flag as unhandled.
- An early return behind a confirmation or dry-run flag — e.g. ` + "`if not confirm: return preview(...)`" + ` — is a SAFETY guard, not a destructive-action bug. A delete that only runs when ` + "`confirm=True`" + ` is correct.
- An authorization decorator or dependency on the handler (` + "`require_role(...)`" + `, ` + "`Depends(current_user)`" + `, an auth middleware) means the route is NOT unauthenticated.
- A function that deliberately raises/returns an error for its caller to handle is doing its job. "No error handling here" is not a defect unless you can see the caller ignoring it.

SEVERITY — be strict, and never inflate to make a finding sound important:
- CRITICAL: a defect an attacker or ordinary input can trigger TODAY, causing data loss, RCE, auth bypass, or a crash of the running service.
- HIGH: a real bug with serious impact but requiring unusual conditions.
- MEDIUM/LOW: correctness or hygiene issues.
- Values the application itself generates (its own date strings, enum names, its own error messages like "old password incorrect") are NOT untrusted input and NOT information disclosure. Do not report injection or leakage for them.
- If your best description is "could be improved" or "is a nice-to-have", set "found": false.

LANGUAGE IDIOMS — these are NOT bugs; never report them:
- Python: a ` + "`with`" + ` / ` + "`async with`" + ` block ALWAYS runs its context manager's cleanup on every exit (return, exception, or fall-through) — a ` + "`return`" + ` inside ` + "`with session_scope() as s:`" + ` is NOT a resource/session leak. ` + "`getattr(o, name, default)`" + `, ` + "`dict.get(k, default)`" + `, and ` + "`next(it, default)`" + ` never raise on a missing attribute/key/item — do NOT report them as null/None dereference or AttributeError risks. A deferred import inside a function body is the standard way to break an import cycle, not a bug. Reserve "circular dependency" claims for MODULE-LEVEL imports only.
- JS/TS: ` + "`encodeURIComponent`" + ` encodes ` + "`/`" + ` (to %2F) and other reserved chars, and ` + "`URLSearchParams`" + `/` + "`new URL()`" + ` percent-encode their inputs automatically — do NOT report path-traversal or injection through values already passed through them.

Respond with ONE JSON object, nothing else:
{
  "found": true,
  "title": "short, specific",
  "severity": "CRITICAL|HIGH|MEDIUM",
  "confidence": "high|medium|low",
  "category": "logic|concurrency|security|resource_leak|error_handling",
  "location": { "file": "exact/path", "line_start": 0, "line_end": 0, "entity": "function or type name" },
  "finding": { "issue": "what is wrong, citing the code", "impact": "what breaks in production", "fix": "the concrete change" }
}
Set "found": false (and leave the other fields empty) if there is no real, high-confidence defect — do NOT invent one. Never fabricate code or paths not shown. Output valid JSON only — no prose, no code fences.`

// adversarial runs the red-team prompt over one target's context and returns a
// validated Bug if a real defect was found.
func (e *Engine) adversarial(ctx context.Context, root string, t target, g *graph, funcs []store.FuncCodeRow) (Bug, bool) {
	byFile := map[string][]store.FuncCodeRow{}
	for _, r := range funcs {
		byFile[r.File] = append(byFile[r.File], r)
	}
	payload := e.buildContext(ctx, root, t, g, byFile)

	raw, err := e.Chat.Complete(ctx, redTeamSystem, payload)
	if err != nil {
		return Bug{}, false
	}
	var p struct {
		Found      bool     `json:"found"`
		Title      string   `json:"title"`
		Severity   string   `json:"severity"`
		Category   string   `json:"category"`
		Confidence string   `json:"confidence"`
		Location   Location `json:"location"`
		Finding    Finding  `json:"finding"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &p); err != nil {
		return Bug{}, false
	}
	if !p.Found || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Finding.Issue) == "" {
		return Bug{}, false
	}
	sev := strings.ToUpper(strings.TrimSpace(p.Severity))
	if _, ok := severityRank[sev]; !ok {
		sev = "MEDIUM"
	}
	conf := strings.ToLower(strings.TrimSpace(p.Confidence))
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "medium"
	}
	loc := p.Location
	if loc.File == "" {
		loc.File = t.file
	}
	cat := strings.TrimSpace(p.Category)
	if cat == "" {
		cat = "logic"
	}
	return Bug{
		Title:      strings.TrimSpace(p.Title),
		Severity:   sev,
		Category:   cat,
		Tier:       "llm",
		Confidence: conf,
		Location:   loc,
		Finding: Finding{
			Issue:  llm.CleanMarkdown(p.Finding.Issue),
			Impact: llm.CleanMarkdown(p.Finding.Impact),
			Fix:    llm.CleanMarkdown(p.Finding.Fix),
		},
		ContextNodes: []string{t.file},
	}, true
}

// --- small text helpers -----------------------------------------------------

// fileHead reads the top of a source file (imports + top-level declarations)
// from the local repo. Best-effort: returns "" if the file can't be read.
func fileHead(root, rel string, maxLines int) string {
	if root == "" || rel == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func concatCode(rows []store.FuncCodeRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Code)
		b.WriteByte('\n')
	}
	return b.String()
}

func signatures(rows []store.FuncCodeRow, n int) string {
	var b strings.Builder
	count := 0
	for _, r := range rows {
		sig := firstCodeLine(r.Code)
		if sig == "" {
			continue
		}
		b.WriteString("  " + truncate(sig, 120) + "\n")
		if count++; count >= n {
			break
		}
	}
	return b.String()
}

func firstCodeLine(code string) string {
	for _, ln := range strings.Split(code, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "#") {
			continue
		}
		return t
	}
	return ""
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "\n…(truncated)"
	}
	return s
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
