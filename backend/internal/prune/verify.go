package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"project-synapse/backend/internal/store"
)

const verifySystem = `You are auditing a STATIC dead-code analysis for FALSE POSITIVES. Each candidate is a source file with NO static importers, flagged as possibly-dead. Static import analysis cannot see files reached indirectly, so your job is to spot those.

You are given each file's path, language, exported symbols, imported packages, AND a short excerpt of its ACTUAL SOURCE. Read the source — it is the deciding evidence:
- Framework decorators / registration in the source (@Controller, @Injectable, @Module, @Guard, @Pipe, @Entity, @Schema/@ObjectType, @Resolver, @Component, @Directive, route/handler/provider registration, DI) => "framework": invoked indirectly, NOT dead.
- Imports of framework packages (@nestjs/*, next, express, react, @angular/*, typeorm, etc.) corroborate a framework role.
- An EMPTY file, a FULLY COMMENTED-OUT module ("no extractable declarations"), or a plain declaration nothing references with no framework signals => "dead": genuinely removable.
- You truly cannot tell => "uncertain".

Respond with ONE JSON object, nothing else:
{"verdicts":[{"id":<number>,"verdict":"framework|dead|uncertain","reason":"short justification grounded in the source"}]}
Return one verdict per candidate id. Output valid JSON only — no prose, no code fences.`

// verify reviews file-level candidates with the LLM and drops the ones that are
// almost certainly reached indirectly (framework/dynamic), demotes uncertain
// ones, and confirms the rest — cutting the main dead-code false-positive class.
func (e *Engine) verify(ctx context.Context, rep *Report, rels []store.RelRow) {
	exportsByFile := map[string][]string{}
	importsByFile := map[string][]string{}
	for _, r := range rels {
		switch r.RelationshipType {
		case "exports":
			exportsByFile[r.SourceSymbol] = append(exportsByFile[r.SourceSymbol], r.TargetSymbol)
		case "imports":
			// Prefer the package/module name so the LLM sees framework signals.
			if ext, _ := r.Metadata["external"].(bool); ext {
				importsByFile[r.SourceSymbol] = appendUnique(importsByFile[r.SourceSymbol], r.TargetSymbol)
			}
		}
	}

	var targets []int // indices of file-level candidates
	for i, c := range rep.Candidates {
		if c.Kind == "file" {
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		return
	}
	const maxVerify = 50
	if len(targets) > maxVerify {
		targets = targets[:maxVerify]
	}

	// Fetch a compact source excerpt for each candidate so the LLM judges the
	// real code (decorators, framework wiring, emptiness), not just the name.
	codeByFile := make(map[string]string, len(targets))
	for _, idx := range targets {
		p := rep.Candidates[idx].Path
		if _, done := codeByFile[p]; done {
			continue
		}
		rows, err := e.Store.FileFunctions(ctx, rep.Repo, p)
		if err != nil {
			codeByFile[p] = ""
			continue
		}
		codeByFile[p] = fileExcerpt(rows)
	}

	drop := map[int]bool{}
	const batch = 8
	for i := 0; i < len(targets); i += batch {
		end := i + batch
		if end > len(targets) {
			end = len(targets)
		}
		e.verifyBatch(ctx, rep, targets[i:end], exportsByFile, importsByFile, codeByFile, drop)
	}

	if len(drop) == 0 {
		return
	}
	kept := rep.Candidates[:0:0]
	for i, c := range rep.Candidates {
		if !drop[i] {
			kept = append(kept, c)
		}
	}
	rep.Candidates = kept
	rep.Summary = map[string]int{}
	for _, c := range rep.Candidates {
		rep.Summary[c.Tier]++
	}
	rep.Notes = append(rep.Notes, fmt.Sprintf("LLM verification removed %d likely framework-invoked / dynamically-loaded false positive(s).", len(drop)))
}

func (e *Engine) verifyBatch(ctx context.Context, rep *Report, idxs []int, exportsByFile, importsByFile map[string][]string, codeByFile map[string]string, drop map[int]bool) {
	var payload strings.Builder
	for i, idx := range idxs {
		c := rep.Candidates[idx]
		exps := exportsByFile[c.Path]
		if len(exps) > 12 {
			exps = exps[:12]
		}
		imps := importsByFile[c.Path]
		if len(imps) > 12 {
			imps = imps[:12]
		}
		expStr := strings.Join(exps, ", ")
		if expStr == "" {
			expStr = "(none)"
		}
		impStr := strings.Join(imps, ", ")
		if impStr == "" {
			impStr = "(none)"
		}
		excerpt := strings.TrimSpace(codeByFile[c.Path])
		if excerpt == "" {
			if len(exps) == 0 {
				excerpt = "(no declarations and no exports — an empty or fully commented-out file)"
			} else {
				excerpt = "(source excerpt unavailable)"
			}
		}
		fmt.Fprintf(&payload, "\n[%d] file: %s\n    language: %s\n    exported symbols: %s\n    imports packages: %s\n    flagged because: %s\n    source excerpt:\n%s\n",
			i, c.Path, c.Language, expStr, impStr, c.Reason, indentLines(excerpt, "      "))
	}
	raw, err := e.Chat.Complete(ctx, verifySystem, payload.String())
	if err != nil {
		return
	}
	var parsed struct {
		Verdicts []struct {
			ID      int    `json:"id"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"verdicts"`
	}
	if json.Unmarshal([]byte(extractJSON(raw)), &parsed) != nil {
		return
	}
	for _, v := range parsed.Verdicts {
		if v.ID < 0 || v.ID >= len(idxs) {
			continue
		}
		idx := idxs[v.ID]
		reason := strings.TrimSpace(v.Reason)
		switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
		case "framework", "reachable", "used":
			drop[idx] = true
		case "uncertain":
			rep.Candidates[idx].Uncertain = true
			rep.Candidates[idx].Confidence = "medium"
			if reason != "" {
				rep.Candidates[idx].Evidence = append(rep.Candidates[idx].Evidence, "needs review: "+reason)
			}
		case "dead":
			rep.Candidates[idx].Confidence = "high"
			if reason != "" {
				rep.Candidates[idx].Evidence = append(rep.Candidates[idx].Evidence, "LLM-verified dead: "+reason)
			}
		}
	}
}

// fileExcerpt renders a compact, size-capped view of a file's real declarations
// (each symbol's signature + a few body lines) so the verifier can see framework
// decorators, wiring, or the lack of any code. Empty rows => "" (the caller turns
// that into an "empty / commented-out" note, distinguishing it from a file whose
// source simply wasn't chunked).
func fileExcerpt(rows []store.FunctionRow) string {
	const (
		perSymbol = 320
		maxTotal  = 1400
	)
	var b strings.Builder
	for _, r := range rows {
		code := strings.TrimSpace(r.Code)
		if code == "" {
			continue
		}
		lines := strings.Split(code, "\n")
		if len(lines) > 6 {
			lines = lines[:6] // the declaration head (decorators + signature), not the whole body
		}
		snippet := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if len(snippet) > perSymbol {
			snippet = snippet[:perSymbol] + "…"
		}
		b.WriteString(snippet)
		b.WriteString("\n---\n")
		if b.Len() >= maxTotal {
			break
		}
	}
	out := strings.TrimRight(b.String(), "-\n ")
	if len(out) > maxTotal {
		out = out[:maxTotal] + "…"
	}
	return out
}

// indentLines prefixes every line of s with pad (for readable prompt payloads).
func indentLines(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
