package ingest

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"project-synapse/backend/internal/myelin"
	"project-synapse/backend/internal/parser"
	"project-synapse/backend/internal/store"
)

// Persister is the slice of the storage layer the pipeline depends on. Keeping
// it narrow lets the pipeline run in dry-run mode (nil persister) and keeps it
// unit-testable.
type Persister interface {
	PersistAnalysis(ctx context.Context, rootPath string, fa *parser.FileAnalysis) error
}

// Compile-time assertion that the concrete store satisfies the interface.
var _ Persister = (*store.Store)(nil)

// Stats summarises a single pipeline run.
type Stats struct {
	FilesDiscovered int64
	FilesParsed     int64
	FilesPersisted  int64
	ChunksEmbedded  int64
	Errors          int64
}

// Pipeline turns a directory of TS/JS files into the knowledge graph. It runs
// in three stages:
//
//  1. Discover  — sequentially walk the tree, collecting source-file paths.
//  2. Parse+Resolve+Persist — a bounded worker pool processes files
//     concurrently: each worker parses one file (true AST via NodeParser),
//     resolves its imports against the full file set, and persists the result.
//
// The full file set must be known before resolution, which is why discovery is
// a distinct first stage.
type Pipeline struct {
	Parser    parser.Parser
	Persister Persister // may be nil for dry-run (parse only, no DB writes)
	Enricher  *Enricher // may be nil to skip semantic chunking + embeddings
	Workers   int
}

// Run executes the pipeline against root and returns aggregate statistics.
func (pl *Pipeline) Run(ctx context.Context, root string) (Stats, error) {
	var stats Stats
	err := pl.RunInto(ctx, root, &stats)
	return stats, err
}

// RunInto executes the pipeline, reporting progress live into the supplied stats
// (atomic counters), so an async caller can poll while it runs.
func (pl *Pipeline) RunInto(ctx context.Context, root string, stats *Stats) error {
	workers := pl.Workers
	if workers <= 0 {
		workers = 4
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(absRoot); statErr != nil {
		return statErr
	}

	// --- Stage 1: discover ------------------------------------------------
	paths := make(chan string, 128)
	walkErrCh := make(chan error, 1)
	go func() { walkErrCh <- Walk(absRoot, paths) }()

	var relPaths []string
	for p := range paths {
		relPaths = append(relPaths, filepath.ToSlash(p))
	}
	if err := <-walkErrCh; err != nil {
		return err
	}

	tsAliases, tsBaseDirs := loadTSConfigResolution(absRoot)
	known := parser.BuildIndexWithConfig(relPaths, tsAliases, tsBaseDirs)
	atomic.StoreInt64(&stats.FilesDiscovered, int64(len(relPaths)))
	if len(relPaths) == 0 {
		return nil
	}

	// --- Stage 2: parse + resolve + persist (worker pool) -----------------
	jobs := make(chan string, workers*2)
	go func() {
		defer close(jobs)
		for _, rel := range relPaths {
			select {
			case <-ctx.Done():
				return
			case jobs <- rel:
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pl.worker(ctx, id, absRoot, known, jobs, stats)
		}(i)
	}
	wg.Wait()

	return ctx.Err()
}

func (pl *Pipeline) worker(ctx context.Context, id int, root string, known *parser.ResolveIndex, jobs <-chan string, stats *Stats) {
	for rel := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			log.Printf("[worker %d] read %s: %v", id, rel, err)
			atomic.AddInt64(&stats.Errors, 1)
			continue
		}

		// Skip binary files: a NUL byte means it isn't text source (a stray image,
		// AppleDouble sidecar, etc.). Postgres text columns reject 0x00, so this
		// also prevents "invalid byte sequence for encoding UTF8" persist errors.
		if bytes.IndexByte(content, 0) >= 0 {
			log.Printf("[worker %d] skip %s: binary (contains NUL bytes)", id, rel)
			continue
		}

		var analysis *parser.FileAnalysis
		if myelin.IsMarkdown(rel) {
			// Markdown is analysed in-process (no TS parser): links → edges,
			// header sections → myelin_doc chunks during enrichment.
			analysis = myelin.Analyze(rel, string(content))
			atomic.AddInt64(&stats.FilesParsed, 1)
		} else {
			a, perr := pl.Parser.Parse(ctx, rel, content)
			if perr != nil {
				// Parse errors are logged; a non-nil analysis (file node with no
				// edges) may still be returned for partial persistence.
				log.Printf("[worker %d] parse %s: %v", id, rel, perr)
				atomic.AddInt64(&stats.Errors, 1)
				if a == nil {
					continue
				}
			} else {
				atomic.AddInt64(&stats.FilesParsed, 1)
			}
			analysis = a
		}

		parser.ResolveImports(analysis, known)

		if pl.Persister == nil {
			continue // dry-run
		}
		if err := pl.Persister.PersistAnalysis(ctx, root, analysis); err != nil {
			log.Printf("[worker %d] persist %s: %v", id, rel, err)
			atomic.AddInt64(&stats.Errors, 1)
			continue
		}
		atomic.AddInt64(&stats.FilesPersisted, 1)

		if pl.Enricher != nil {
			n, err := pl.Enricher.EnrichFile(ctx, root, analysis)
			if err != nil {
				log.Printf("[worker %d] enrich %s: %v", id, rel, err)
				atomic.AddInt64(&stats.Errors, 1)
				continue
			}
			atomic.AddInt64(&stats.ChunksEmbedded, int64(n))
		}
	}
}
