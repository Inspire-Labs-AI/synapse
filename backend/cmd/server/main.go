// Command server is the Project Synapse ingestion + graph + RAG entrypoint.
//
// Lifecycle:
//  1. Connect to Postgres (pgvector) and build the embedding + LLM clients.
//  2. If SYNAPSE_INGEST_ROOT is set, run the ingestion pipeline: walk the
//     TS/JS tree, parse each file's true AST, resolve imports, persist files +
//     typed relationships, then (if enrichment is on) chunk + embed each file
//     into vector_chunks.
//  3. If SYNAPSE_SERVE is true (default), start the HTTP API:
//     GET  /api/graph/data  — React-Flow-ready topology
//     POST /api/query       — hybrid RAG question answering
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"project-synapse/backend/internal/api"
	"project-synapse/backend/internal/architecture"
	"project-synapse/backend/internal/axon"
	"project-synapse/backend/internal/blueprint"
	"project-synapse/backend/internal/bugs"
	"project-synapse/backend/internal/config"
	"project-synapse/backend/internal/docs"
	"project-synapse/backend/internal/embed"
	"project-synapse/backend/internal/ingest"
	"project-synapse/backend/internal/llm"
	"project-synapse/backend/internal/parser"
	"project-synapse/backend/internal/prune"
	"project-synapse/backend/internal/rag"
	"project-synapse/backend/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[synapse] ")

	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connectCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	db, err := store.New(connectCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	defer db.Close()
	log.Printf("connected to database")

	// Embedding client (semantic layer).
	embedder, err := embed.New(embed.Config{
		Provider:       cfg.EmbedProvider,
		Model:          cfg.EmbedModel,
		Dim:            cfg.EmbedDim,
		JinaKey:        cfg.JinaKey,
		JinaBase:       cfg.JinaBase,
		VoyageKey:      cfg.VoyageKey,
		VoyageBase:     cfg.VoyageBase,
		OpenAIKey:      cfg.OpenAIKey,
		OpenAIBase:     cfg.OpenAIBase,
		OpenRouterKey:  cfg.OpenRouterKey,
		OpenRouterBase: cfg.OpenRouterBase,
		GeminiKey:      cfg.GeminiKey,
		GeminiBase:     cfg.GeminiBase,
		OllamaHost:     cfg.OllamaHost,
	})
	if err != nil {
		log.Fatalf("embedder init: %v", err)
	}
	log.Printf("embedder: %s (dim=%d)", embedder.Name(), embedder.Dimensions())

	// Reconcile the vector schema to the active embedder. Lets you switch model /
	// dimension purely via .env (SYNAPSE_EMBED_MODEL / SYNAPSE_EMBED_DIM): on any
	// change, the vector_chunks column + HNSW index are re-typed and stale vectors
	// cleared automatically — just re-ingest afterwards.
	if action, rerr := db.ReconcileEmbedding(ctx, embedder.Name(), embedder.Dimensions()); rerr != nil {
		log.Fatalf("embedding schema reconcile: %v", rerr)
	} else if action != "" {
		log.Printf("embedding: %s", action)
	}

	// Chat client (answer synthesis); nil => offline template responder.
	chat, err := llm.NewChatClient(llm.Config{
		Provider:       cfg.LLMProvider,
		Model:          cfg.LLMModel,
		AnthropicKey:   cfg.AnthropicKey,
		OpenAIKey:      cfg.OpenAIKey,
		OpenAIBase:     cfg.OpenAIBase,
		OpenRouterKey:  cfg.OpenRouterKey,
		OpenRouterBase: cfg.OpenRouterBase,
		OllamaHost:     cfg.OllamaHost,
	})
	if err != nil {
		log.Fatalf("llm init: %v", err)
	}
	if chat == nil {
		log.Printf("llm: template (offline deterministic responder)")
	} else {
		log.Printf("llm: %s", chat.Name())
	}

	// Build the ingestion pipeline once — shared by the optional startup
	// ingest and the on-demand POST /api/ingest clone handler.
	np := parser.NewNodeParser(cfg.ParserDir, cfg.NodeBin)
	// MultiParser keeps TS/JS on the Node tsc-AST subprocess while parsing Go
	// (go/parser) and Rust (lexical) in-process.
	pipeline := &ingest.Pipeline{Parser: parser.NewMultiParser(np), Persister: db, Workers: cfg.Workers}
	if cfg.Enrich {
		pipeline.Enricher = &ingest.Enricher{
			Store:     db,
			Embedder:  embedder,
			Chat:      chat,
			Summaries: cfg.EnrichSummaries,
		}
	}

	if cfg.IngestRoot != "" {
		if err := runIngestion(ctx, cfg, np, pipeline); err != nil {
			log.Fatalf("ingestion failed: %v", err)
		}
	}

	if !cfg.Serve {
		return
	}

	// Query + discovery share a cached embedder (repeated terms hit the cache,
	// keeping deep structural lookups sub-second).
	queryEmbedder := embed.NewCache(embedder, 0)

	orch := &rag.Orchestrator{
		Store:    db,
		Embedder: queryEmbedder,
		Chat:     chat,
		TopK:     cfg.RAGTopK,
	}

	bp := &blueprint.Engine{
		Store:       db,
		Embedder:    queryEmbedder,
		Extractor:   &blueprint.Extractor{Chat: chat},
		Concurrency: cfg.BlueprintConcurrency,
		TopK:        cfg.RAGTopK,
	}

	ingestHandler := &ingest.Handler{
		Pipeline:  pipeline,
		ClonesDir: cfg.ClonesDir,
		GitBin:    cfg.GitBin,
	}

	// Per-engine model selection. Docs / architecture / bug analysis are heavier
	// reasoning tasks and can run on a stronger model (SYNAPSE_DOCS_MODEL /
	// SYNAPSE_ARCH_MODEL / SYNAPSE_BUGS_MODEL), while the assistant (RAG),
	// blueprint discovery, tours, and enrichment use the base SYNAPSE_LLM_MODEL.
	// Clients are cached per model so identical overrides share one instance.
	chatCache := map[string]llm.ChatClient{cfg.LLMModel: chat}
	chatFor := func(model string) llm.ChatClient {
		if model == "" || model == cfg.LLMModel {
			return chat
		}
		if c, ok := chatCache[model]; ok {
			return c
		}
		c, cerr := llm.NewChatClient(llm.Config{
			Provider: cfg.LLMProvider, Model: model,
			AnthropicKey: cfg.AnthropicKey, OpenAIKey: cfg.OpenAIKey, OpenAIBase: cfg.OpenAIBase,
			OpenRouterKey: cfg.OpenRouterKey, OpenRouterBase: cfg.OpenRouterBase, OllamaHost: cfg.OllamaHost,
		})
		if cerr != nil || c == nil {
			return chat // fall back to the base client
		}
		chatCache[model] = c
		log.Printf("llm[%s]: %s", model, c.Name())
		return c
	}

	docsEngine := &docs.Engine{Store: db, Chat: chatFor(cfg.DocsModel)}
	archEngine := &architecture.Engine{Store: db, Chat: chatFor(cfg.ArchModel)}
	axonEngine := &axon.Engine{Store: db, Chat: chat}
	pruneEngine := &prune.Engine{Store: db, Chat: chatFor(cfg.BugsModel), Verify: cfg.PruneVerify}
	bugsEngine := &bugs.Engine{Store: db, Embedder: queryEmbedder, Chat: chatFor(cfg.BugsModel), LLM: cfg.BugsLLM, MaxLLM: cfg.BugsMaxLLM}

	srv := api.NewHTTPServer(cfg.HTTPAddr, db, orch, bp, ingestHandler, docsEngine, archEngine, axonEngine, pruneEngine, bugsEngine)
	if err := api.Run(ctx, srv); err != nil {
		log.Fatalf("http server error: %v", err)
	}
	log.Printf("shutdown complete")
}

func runIngestion(ctx context.Context, cfg config.Config, np *parser.NodeParser, pipeline *ingest.Pipeline) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := np.Verify(verifyCtx); err != nil {
		return err
	}

	log.Printf("ingestion start — root=%q workers=%d enrich=%v", cfg.IngestRoot, cfg.Workers, pipeline.Enricher != nil)
	start := time.Now()
	stats, err := pipeline.Run(ctx, cfg.IngestRoot)
	if err != nil {
		return err
	}

	log.Printf("ingestion complete in %s", time.Since(start).Round(time.Millisecond))
	log.Printf("  discovered: %d", stats.FilesDiscovered)
	log.Printf("  parsed:     %d", stats.FilesParsed)
	log.Printf("  persisted:  %d", stats.FilesPersisted)
	log.Printf("  chunks:     %d", stats.ChunksEmbedded)
	log.Printf("  errors:     %d", stats.Errors)
	return nil
}
