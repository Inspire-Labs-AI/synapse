package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"project-synapse/backend/internal/agentctx"
	"project-synapse/backend/internal/architecture"
	"project-synapse/backend/internal/axon"
	"project-synapse/backend/internal/blueprint"
	"project-synapse/backend/internal/bugs"
	"project-synapse/backend/internal/docs"
	"project-synapse/backend/internal/ingest"
	"project-synapse/backend/internal/prune"
	"project-synapse/backend/internal/rag"
	"project-synapse/backend/internal/store"
)

// Server wires the HTTP API for the knowledge graph + RAG + blueprint layers.
type Server struct {
	store     *store.Store
	orch      *rag.Orchestrator     // may be nil if querying is disabled
	blueprint *blueprint.Engine     // may be nil if discovery is disabled
	ingest    *ingest.Handler       // may be nil if clone-on-demand is disabled
	docs      *docs.Engine          // may be nil if docs generation is disabled
	arch      *architecture.Engine  // may be nil if architecture is disabled
	axon      *axon.Engine          // may be nil if pathway tours are disabled
	prune     *prune.Engine         // may be nil if pruning is disabled
	bugs      *bugs.Engine          // may be nil if bug detection is disabled
}

// NewHTTPServer builds an *http.Server bound to addr, serving the full API.
func NewHTTPServer(addr string, st *store.Store, orch *rag.Orchestrator, bp *blueprint.Engine, ig *ingest.Handler, dc *docs.Engine, ar *architecture.Engine, ax *axon.Engine, pr *prune.Engine, bg *bugs.Engine) *http.Server {
	s := &Server{store: st, orch: orch, blueprint: bp, ingest: ig, docs: dc, arch: ar, axon: ax, prune: pr, bugs: bg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/repos", s.handleRepos)
	mux.HandleFunc("DELETE /api/repos", s.handleDeleteRepo)
	mux.HandleFunc("GET /api/graph/data", s.handleGraphData)
	mux.HandleFunc("GET /api/file/functions", s.handleFileFunctions)
	mux.HandleFunc("GET /api/file/summary", s.handleFileSummary)
	mux.HandleFunc("GET /api/docs", s.handleDocs)
	mux.HandleFunc("GET /api/context", s.handleContext)
	mux.HandleFunc("GET /api/architecture", s.handleArchitecture)
	mux.HandleFunc("GET /api/prune", s.handlePrune)
	mux.HandleFunc("GET /api/bugs", s.handleBugs)
	mux.HandleFunc("GET /api/axon/pathway", s.handleAxon)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("POST /api/query/stream", s.handleQueryStream)
	mux.HandleFunc("POST /api/blueprint/discover", s.handleDiscover)
	mux.HandleFunc("POST /api/blueprint/discover/stream", s.handleDiscoverStream)
	if ig != nil {
		mux.HandleFunc("POST /api/ingest", ig.ServeHTTP)
		mux.HandleFunc("GET /api/ingest/status", ig.StatusHTTP)
	}

	return &http.Server{
		Addr:              addr,
		Handler:           withCORS(withLogging(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRepos lists the ingested repositories for the workspace switcher.
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepos(r.Context())
	if err != nil {
		log.Printf("list repos error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list repos"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// handleDeleteRepo removes one ingested repository, identified by ?repo=<root_path>.
func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo query parameter is required"})
		return
	}
	removed, err := s.store.DeleteRoot(r.Context(), repo)
	if err != nil {
		log.Printf("delete repo error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete repo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "files_removed": removed})
}

// handleDocs returns generated documentation for ?repo=<root_path> (&refresh=true
// to regenerate).
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	if s.docs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "docs engine not configured"})
		return
	}
	d, err := s.docs.Generate(r.Context(), r.URL.Query().Get("repo"), r.URL.Query().Get("refresh") == "true")
	if err != nil {
		log.Printf("docs error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate docs"})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleContext exports a portable, self-contained brief of ?repo=<root_path>:
// the structural facts from the AST graph (languages, entry points, endpoints,
// modules, hub files, live dependencies) plus the generated documentation. It is
// the payload another AI agent can be handed to understand the project without
// reading it. Pass &docs=false to omit the narrative pages.
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo query parameter is required"})
		return
	}
	eng := &agentctx.Engine{Store: s.store, Docs: s.docs}
	c, err := eng.Build(r.Context(), repo, r.URL.Query().Get("docs") != "false")
	if err != nil {
		log.Printf("context error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build project context"})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleArchitecture returns the generated system-architecture view for
// ?repo=<root_path> (&refresh=true to regenerate).
func (s *Server) handleArchitecture(w http.ResponseWriter, r *http.Request) {
	if s.arch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "architecture engine not configured"})
		return
	}
	a, err := s.arch.Generate(r.Context(), r.URL.Query().Get("repo"), r.URL.Query().Get("refresh") == "true")
	if err != nil {
		log.Printf("architecture error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate architecture"})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// handlePrune returns the "Synaptic Pruning" dead-code analysis for
// ?repo=<root_path>: confidence-tiered candidates for unused files, exports, and
// functions, with the evidence behind each.
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	if s.prune == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pruning engine not configured"})
		return
	}
	rep, err := s.prune.Analyze(r.Context(), r.URL.Query().Get("repo"), r.URL.Query().Get("refresh") == "true")
	if err != nil {
		log.Printf("prune error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to analyze repository"})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleBugs returns the two-tier critical-bug + anti-pattern scan for
// ?repo=<root_path> (&refresh=true to re-run the LLM pass).
func (s *Server) handleBugs(w http.ResponseWriter, r *http.Request) {
	if s.bugs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bug detection engine not configured"})
		return
	}
	rep, err := s.bugs.Scan(r.Context(), r.URL.Query().Get("repo"), r.URL.Query().Get("refresh") == "true")
	if err != nil {
		log.Printf("bugs error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to scan repository"})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleAxon returns the dependency-ordered onboarding walkthrough ("Axon
// Pathway") for ?repo=<root_path>.
func (s *Server) handleAxon(w http.ResponseWriter, r *http.Request) {
	if s.axon == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "axon pathway not configured"})
		return
	}
	p, err := s.axon.Pathway(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		log.Printf("axon error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build pathway"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleGraphData returns the React-Flow-ready topology of the ingested code,
// scoped to ?repo=<root_path> when provided.
func (s *Server) handleGraphData(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	graph, err := BuildGraph(r.Context(), s.store, repo)
	if err != nil {
		log.Printf("graph build error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build graph"})
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

// handleFileFunctions returns the symbol-level functions (with code) of one
// file for the canvas expand-on-click. Params: ?repo=<root>&path=<file_path>.
func (s *Server) handleFileFunctions(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter is required"})
		return
	}
	fns, err := s.store.FileFunctions(r.Context(), repo, path)
	if err != nil {
		log.Printf("file functions error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load functions"})
		return
	}
	calls, err := s.store.FileCallEdges(r.Context(), repo, path)
	if err != nil {
		log.Printf("file call edges error: %v", err)
		calls = nil // non-fatal — still return the functions
	}
	writeJSON(w, http.StatusOK, map[string]any{"functions": fns, "calls": calls})
}

// handleFileSummary returns a short LLM-written explanation of one file's role,
// for the canvas detail panel. Params: ?repo=<root>&path=<file_path>.
func (s *Server) handleFileSummary(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query parameter is required"})
		return
	}
	if s.axon == nil {
		writeJSON(w, http.StatusOK, map[string]string{"summary": ""})
		return
	}
	summary, err := s.axon.FileSummary(r.Context(), repo, path)
	if err != nil {
		log.Printf("file summary error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to summarize"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("encode response: %v", err)
	}
}

// withCORS permits the Next.js dev origin (and others) to call the API from the
// browser. Wide-open is fine for local single-tenant development.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully. Returns nil on a clean shutdown.
func Run(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		log.Printf("HTTP API listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
