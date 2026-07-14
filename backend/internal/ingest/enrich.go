package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"project-synapse/backend/internal/chunk"
	"project-synapse/backend/internal/embed"
	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/myelin"
	"project-synapse/backend/internal/parser"
	"project-synapse/backend/internal/store"
)

// Enricher turns a parsed file into semantic chunks, embeds them, and upserts
// the vectors into vector_chunks. It runs as the per-file post-persistence step
// of the ingestion pipeline; because the worker pool processes files
// concurrently, many files embed in parallel, and each file's chunks are
// embedded together in one batch call.
//
// When Chat is set and Summaries is on, each symbol also gets a one-line LLM
// "purpose" folded into the embedded text — a natural-language bridge between how
// users ask and how code reads, which sharpens retrieval.
type Enricher struct {
	Store     *store.Store
	Embedder  embed.Embedder
	Chat      llm.ChatClient // optional: per-symbol purpose enrichment
	Summaries bool           // enable LLM purpose enrichment
}

// EnrichFile chunks, embeds, and stores the semantic layer for one file.
// Returns the number of chunks persisted.
func (e *Enricher) EnrichFile(ctx context.Context, rootPath string, fa *parser.FileAnalysis) (int, error) {
	// Markdown ("myelin") files chunk by header section into myelin_doc chunks;
	// code files chunk by structural declaration boundaries.
	var chunks []chunk.Chunk
	if fa.Language == "markdown" {
		chunks = myelin.Chunk(fa)
	} else {
		chunks = chunk.File(fa)
	}
	if len(chunks) == 0 {
		return 0, nil
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	// Fold an LLM-written one-line purpose per symbol into the embedded text.
	if e.Summaries && e.Chat != nil && fa.Language != "markdown" {
		if sums := e.summarize(ctx, fa, chunks); len(sums) > 0 {
			for i := range chunks {
				if p := sums[baseSymbol(chunks[i].SymbolName)]; p != "" {
					texts[i] = chunk.WithPurpose(texts[i], p)
				}
			}
		}
	}

	vecs, err := e.Embedder.Embed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("embed %s: %w", fa.RelPath, err)
	}
	if len(vecs) != len(chunks) {
		return 0, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(chunks))
	}

	inserts := make([]store.ChunkInsert, len(chunks))
	for i, c := range chunks {
		inserts[i] = store.ChunkInsert{
			ChunkType:  c.ChunkType,
			SymbolName: c.SymbolName,
			StartLine:  c.StartLine,
			EndLine:    c.EndLine,
			Content:    texts[i], // the (possibly enriched) embedded text; header stripped on display
			Embedding:  vecs[i],
		}
	}

	if err := e.Store.ReplaceChunks(ctx, rootPath, fa.RelPath, inserts); err != nil {
		return 0, err
	}
	return len(inserts), nil
}

const summarizeSystem = `You label code symbols for semantic search. For each "### symbol" block given (its kind + source), write ONE concise sentence: what it does and when it is used, grounded strictly in the code. Return ONLY a JSON object mapping each exact symbol name to its sentence — no prose, no code fences.`

// summarize asks the LLM for a one-sentence purpose per symbol in a file, in a
// single batched call. Failures degrade gracefully to no enrichment.
func (e *Enricher) summarize(ctx context.Context, fa *parser.FileAnalysis, chunks []chunk.Chunk) map[string]string {
	seen := map[string]bool{}
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s (%s)\nWrite a one-sentence purpose for each symbol.\n\n", fa.RelPath, fa.Language)
	n := 0
	for _, c := range chunks {
		base := baseSymbol(c.SymbolName)
		if base == "" || base == "file" || seen[base] {
			continue
		}
		seen[base] = true
		code := c.Code
		if len(code) > 600 {
			code = code[:600] + "\n…"
		}
		fmt.Fprintf(&b, "### %s [%s]\n%s\n\n", base, c.ChunkType, code)
		if n++; n >= 25 {
			break
		}
	}
	if n == 0 {
		return nil
	}
	raw, err := e.Chat.Complete(ctx, summarizeSystem, b.String())
	if err != nil {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &out); err != nil {
		return nil
	}
	return out
}

func baseSymbol(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

func extractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
