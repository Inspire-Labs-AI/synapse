package bugs

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/store"
)

// --- Tier 1c: bad practices / anti-patterns ---------------------------------

type practiceRule struct {
	name     string
	category string // Bug.Category — "security" | "bad_practice"
	severity string
	langs    map[string]bool
	re       *regexp.Regexp
	issue    string
	impact   string
	fix      string
	perFile  bool // cap to one finding per file (noisy patterns)
}

var practiceRules = []practiceRule{
	{
		name: "hardcoded secret", category: "security", severity: "HIGH", langs: allLangs,
		re:     regexp.MustCompile(`(?i)(api[_-]?key|secret|passwd|password|access[_-]?token|auth[_-]?token|private[_-]?key|client[_-]?secret)\s*[:=]\s*["'][A-Za-z0-9_\-./+]{16,}["']`),
		issue:  "A credential appears to be hard-coded as a string literal.",
		impact: "Secrets in source get committed to version control and leak — anyone with repo access (or a scraped commit) obtains live credentials.",
		fix:    "Move it to an environment variable / secrets manager and load it at runtime; rotate the exposed value.",
	},
	{
		name: "use of eval", category: "security", severity: "HIGH", langs: jsLangs,
		re:     regexp.MustCompile(`(^|[^.\w])eval\s*\(`),
		issue:  "`eval(` executes arbitrary code from its argument.",
		impact: "If any part of the argument is attacker-influenced this is a remote-code-execution / injection vector; it also defeats optimization and tooling.",
		fix:    "Replace with a safe parser (`JSON.parse`, a lookup table, or explicit logic). Never eval untrusted input.",
	},
	{
		name: "empty catch block", category: "bad_practice", severity: "MEDIUM", langs: jsLangs,
		re:     regexp.MustCompile(`catch\s*(\([^)]*\))?\s*\{\s*\}`),
		issue:  "An exception is caught and silently swallowed (empty `catch` block).",
		impact: "Failures disappear with no log or recovery, turning real errors into silent data loss / corrupt state that is very hard to debug.",
		fix:    "Handle the error, or at minimum log it with context and rethrow / surface it to the caller.",
	},
	{
		name: "leftover debugger statement", category: "bad_practice", severity: "MEDIUM", langs: jsLangs,
		re:     regexp.MustCompile(`(^|[^.\w])debugger\s*;`),
		issue:  "A `debugger;` statement is left in the code.",
		impact: "It halts execution when devtools are open and should never ship to production.",
		fix:    "Remove the `debugger;` statement.",
	},
	{
		name: "suppressed type error", category: "bad_practice", severity: "LOW", langs: jsLangs,
		re:      regexp.MustCompile(`@ts-(ignore|nocheck)`),
		issue:   "A TypeScript error is suppressed with `@ts-ignore` / `@ts-nocheck`.",
		impact:  "It hides a real type mismatch that can surface as a runtime bug, and masks regressions on future edits.",
		fix:     "Fix the underlying type, or narrow the suppression to a specific line with an explanatory `@ts-expect-error` note.",
		perFile: true,
	},
	{
		name: "panic in library code", category: "bad_practice", severity: "MEDIUM", langs: goLangs,
		re:     regexp.MustCompile(`(^|[^.\w])panic\s*\(`),
		issue:  "`panic(` is used to signal a recoverable condition.",
		impact: "A panic crashes the goroutine (and, unrecovered, the process). In library / request-handling code an error return is expected; a panic is a denial-of-service risk.",
		fix:    "Return an `error` and let the caller decide. Reserve panic for truly unrecoverable programmer errors (or guard the handler with recover).",
	},
	{
		name: "use of eval/exec", category: "security", severity: "HIGH", langs: pyLangs,
		re:     regexp.MustCompile(`(^|[^.\w])(eval|exec)\s*\(`),
		issue:  "`eval(`/`exec(` runs arbitrary Python from its argument.",
		impact: "If any part of the argument is attacker-influenced this is a remote-code-execution vector; even with trusted input it defeats tooling and is easy to misuse.",
		fix:    "Replace with explicit logic — `ast.literal_eval`, `json.loads`, a dispatch dict, or `getattr` on a whitelisted object. Never eval untrusted input.",
	},
	{
		name: "silently swallowed exception", category: "bad_practice", severity: "MEDIUM", langs: pyLangs,
		re:     regexp.MustCompile(`except[^\n:]*:[ \t]*(#[^\n]*)?\n[ \t]*pass\b`),
		issue:  "An exception is caught and silently ignored (`except: pass`).",
		impact: "Failures disappear with no log or recovery, turning real errors into silent data loss / corrupt state that is very hard to debug. A bare `except:` also swallows `KeyboardInterrupt`/`SystemExit`.",
		fix:    "Catch a specific exception type and handle it — at minimum `logging.exception(...)` with context, then re-raise or return a clear error.",
	},
}

var allLangs = map[string]bool{"go": true, "typescript": true, "javascript": true, "python": true}
var pyLangs = map[string]bool{"python": true}

// detectBadPractices scans function bodies for high-signal anti-patterns. Output
// is candidates — the LLM verify pass confirms each and drops false positives.
func detectBadPractices(funcs []store.FuncCodeRow) []candidate {
	perFileSeen := map[string]bool{} // rule|file -> already flagged
	perCat := map[string]int{}       // category -> count (cap the noisy ones)
	const maxPerCategory = 10
	var cands []candidate
	for _, f := range reassembleFuncs(funcs) {
		lang := langOf(f.file)
		if lang == "" {
			continue
		}
		for _, rule := range practiceRules {
			if !rule.langs[lang] || perCat[rule.name] >= maxPerCategory {
				continue
			}
			if rule.perFile {
				k := rule.name + "\x00" + f.file
				if perFileSeen[k] {
					continue
				}
				if rule.re.MatchString(f.code) {
					perFileSeen[k] = true
				}
			}
			if !rule.re.MatchString(f.code) {
				continue
			}
			perCat[rule.name]++
			cands = append(cands, candidate{
				code: f.code,
				bug: Bug{
					Title:        "Bad practice: " + rule.name,
					Severity:     rule.severity,
					Category:     rule.category,
					Tier:         "deterministic",
					Confidence:   "medium",
					Location:     Location{File: f.file, Entity: f.symbol, LineStart: f.start, LineEnd: f.end},
					Finding:      Finding{Issue: rule.issue, Impact: rule.impact, Fix: rule.fix},
					ContextNodes: []string{f.file},
				},
			})
		}
	}
	return cands
}

// --- verification: confirm heuristic candidates against their real code ------

const verifySystem = `You are a meticulous staff engineer verifying static-analysis findings to ELIMINATE false positives. For EACH candidate you get its category, the heuristic's claim, and the ACTUAL source code of the enclosing function.

Decide whether each is a REAL, actionable defect in the code shown. Be strict — reject anything that is a false positive, intentional and safe in context, obviously test/example/mock code, already handled elsewhere in the shown code, or simply not a real problem. Do NOT confirm out of caution; only confirm concrete, defensible issues. When you DO confirm, set an accurate severity and confidence and write a specific issue / impact / fix grounded in the exact code.

Respond with ONE JSON object, nothing else:
{"verdicts":[{"id":<number>,"confirmed":<bool>,"severity":"CRITICAL|HIGH|MEDIUM|LOW","confidence":"high|medium|low","title":"short specific title","issue":"...","impact":"...","fix":"..."}]}
Return exactly one verdict per candidate id you were given. Output valid JSON only — no prose, no code fences.`

type verdict struct {
	ID         int    `json:"id"`
	Confirmed  bool   `json:"confirmed"`
	Severity   string `json:"severity"`
	Confidence string `json:"confidence"`
	Title      string `json:"title"`
	Issue      string `json:"issue"`
	Impact     string `json:"impact"`
	Fix        string `json:"fix"`
}

// verifyCandidates confirms heuristic candidates with the LLM (batched), keeping
// only the ones judged real and applying the model's refined severity/text.
func (e *Engine) verifyCandidates(ctx context.Context, cands []candidate) []Bug {
	if len(cands) == 0 {
		return nil
	}
	// Prioritise security + higher severity, then cap total for token budget.
	sort.SliceStable(cands, func(i, j int) bool { return candPriority(cands[i]) < candPriority(cands[j]) })
	const maxVerify = 40
	if len(cands) > maxVerify {
		cands = cands[:maxVerify]
	}

	var out []Bug
	const batch = 5
	for i := 0; i < len(cands); i += batch {
		end := i + batch
		if end > len(cands) {
			end = len(cands)
		}
		out = append(out, e.verifyBatch(ctx, cands[i:end])...)
	}
	return out
}

func (e *Engine) verifyBatch(ctx context.Context, batch []candidate) []Bug {
	var payload strings.Builder
	for i, c := range batch {
		fmt.Fprintf(&payload, "\n=== CANDIDATE id=%d ===\ncategory: %s\nheuristic claim: %s\nfile: %s\nentity: %s\nsource:\n%s\n",
			i, c.bug.Category, c.bug.Finding.Issue, c.bug.Location.File, c.bug.Location.Entity, truncate(c.code, 2200))
	}
	raw, err := e.Chat.Complete(ctx, verifySystem, payload.String())
	if err != nil {
		return nil
	}
	var parsed struct {
		Verdicts []verdict `json:"verdicts"`
	}
	if json.Unmarshal([]byte(extractJSONObject(raw)), &parsed) != nil {
		return nil
	}
	var out []Bug
	for _, v := range parsed.Verdicts {
		if !v.Confirmed || v.ID < 0 || v.ID >= len(batch) {
			continue
		}
		out = append(out, applyVerdict(batch[v.ID].bug, v))
	}
	return out
}

func applyVerdict(b Bug, v verdict) Bug {
	b.Tier = "verified"
	if s := strings.ToUpper(strings.TrimSpace(v.Severity)); validSeverity(s) {
		b.Severity = s
	}
	if c := strings.ToLower(strings.TrimSpace(v.Confidence)); c == "high" || c == "medium" || c == "low" {
		b.Confidence = c
	}
	if t := strings.TrimSpace(v.Title); t != "" {
		b.Title = t
	}
	if s := strings.TrimSpace(v.Issue); s != "" {
		b.Finding.Issue = llm.CleanMarkdown(s)
	}
	if s := strings.TrimSpace(v.Impact); s != "" {
		b.Finding.Impact = llm.CleanMarkdown(s)
	}
	if s := strings.TrimSpace(v.Fix); s != "" {
		b.Finding.Fix = llm.CleanMarkdown(s)
	}
	return b
}

func candPriority(c candidate) int {
	base := severityRank[c.bug.Severity] // 0 (CRITICAL) .. 3 (LOW)
	if c.bug.Category == "security" {
		return base // security first within a severity band
	}
	return base + 10
}

func validSeverity(s string) bool {
	_, ok := severityRank[s]
	return ok
}
