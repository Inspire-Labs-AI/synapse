package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"project-synapse/backend/internal/parser"
)

// Store is the database connector manager. It owns a pgx connection pool and
// exposes the persistence + read operations for the knowledge graph. pgx is
// used directly (no ORM) with raw SQL against the relational + pgvector schema.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pooled connection to Postgres and verifies it is reachable.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Ping verifies database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// PersistAnalysis writes one file's structural extraction in a single
// transaction:
//  1. upsert the code_files row (path, filename, raw content, hash, size),
//  2. clear that file's stale ast_relationships and vector_chunks,
//  3. insert fresh typed edges (imports / exports / endpoints),
//  4. insert fresh semantic chunks (one per export / endpoint).
//
// Replacing per file (delete-then-insert keyed by file_id) keeps re-ingestion
// idempotent.
func (s *Store) PersistAnalysis(ctx context.Context, rootPath string, fa *parser.FileAnalysis) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	fileID, err := upsertCodeFile(ctx, tx, rootPath, fa)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM ast_relationships WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("clear relationships: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vector_chunks WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("clear vector_chunks: %w", err)
	}

	if err := insertRelationships(ctx, tx, fileID, fa); err != nil {
		return err
	}
	if err := insertVectorChunks(ctx, tx, fileID, fa); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RepoInfo summarises one ingested repository (one root_path) for the workspace
// repo switcher.
type RepoInfo struct {
	RootPath string `json:"root_path"`
	Name     string `json:"name"`
	Files    int    `json:"files"`
	Chunks   int    `json:"chunks"`
}

// ListRepos returns one entry per ingested root_path, most-recently-updated
// first, with file + chunk counts.
func (s *Store) ListRepos(ctx context.Context) ([]RepoInfo, error) {
	const q = `
		SELECT cf.root_path,
		       count(DISTINCT cf.id) AS files,
		       count(vc.id)          AS chunks,
		       max(cf.updated_at)    AS updated
		FROM code_files cf
		LEFT JOIN vector_chunks vc ON vc.file_id = cf.id
		GROUP BY cf.root_path
		ORDER BY updated DESC;`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()

	out := []RepoInfo{}
	for rows.Next() {
		var r RepoInfo
		var updated time.Time
		if err := rows.Scan(&r.RootPath, &r.Files, &r.Chunks, &updated); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		r.Name = repoName(r.RootPath)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRoot removes one repository — all its code_files, and via ON DELETE
// CASCADE its ast_relationships + vector_chunks. Returns rows removed.
func (s *Store) DeleteRoot(ctx context.Context, root string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM code_files WHERE root_path = $1`, root)
	if err != nil {
		return 0, fmt.Errorf("delete root: %w", err)
	}
	return tag.RowsAffected(), nil
}

// repoName derives a friendly label (the trailing path segment) from a root
// path, handling both "/" and "\" so Windows clone paths render cleanly.
func repoName(root string) string {
	r := strings.TrimRight(root, `/\`)
	if i := strings.LastIndexAny(r, `/\`); i >= 0 {
		return r[i+1:]
	}
	return r
}

func upsertCodeFile(ctx context.Context, tx pgx.Tx, rootPath string, fa *parser.FileAnalysis) (int64, error) {
	const q = `
		INSERT INTO code_files (root_path, file_path, filename, language, content, content_hash, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (root_path, file_path)
		DO UPDATE SET
			filename     = EXCLUDED.filename,
			language     = EXCLUDED.language,
			content      = EXCLUDED.content,
			content_hash = EXCLUDED.content_hash,
			size_bytes   = EXCLUDED.size_bytes,
			updated_at   = now()
		RETURNING id;`

	var id int64
	err := tx.QueryRow(ctx, q, rootPath, fa.RelPath, fa.Filename, fa.Language, fa.Content, fa.Hash, fa.SizeBytes).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert code_file %s: %w", fa.RelPath, err)
	}
	return id, nil
}

// insertRelationships writes the typed dependency-graph edges for a file:
// imports (file -> file | external module), exports (file -> symbol), and
// endpoints (file -> "METHOD /path").
func insertRelationships(ctx context.Context, tx pgx.Tx, fileID int64, fa *parser.FileAnalysis) error {
	const q = `
		INSERT INTO ast_relationships (file_id, source_symbol, target_symbol, relationship_type, metadata)
		VALUES ($1, $2, $3, $4, $5);`

	batch := &pgx.Batch{}

	for _, imp := range fa.Imports {
		target := imp.Resolved
		if target == "" {
			target = imp.Specifier
		}
		meta := jsonObj(map[string]any{
			"specifier":  imp.Specifier,
			"symbols":    imp.Symbols,
			"kind":       imp.Kind,
			"deferred":   imp.Deferred,
			"line":       imp.Line,
			"external":   imp.External,
			"unresolved": !imp.External && !imp.ResolvedOK,
		})
		batch.Queue(q, fileID, fa.RelPath, target, "imports", meta)
	}

	for _, exp := range fa.Exports {
		patterns := exp.Patterns
		if patterns == nil {
			patterns = []string{}
		}
		meta := jsonObj(map[string]any{
			"kind":              exp.Kind,
			"isDefault":         exp.IsDefault,
			"line":              exp.Line,
			"dendrite_patterns": patterns,
		})
		batch.Queue(q, fileID, fa.RelPath, exp.Name, "exports", meta)
	}

	for _, ep := range fa.Endpoints {
		route := deriveRoutePath(fa.RelPath, ep.Path)
		meta := jsonObj(map[string]any{
			"method":  ep.Method,
			"path":    route,
			"handler": ep.Handler,
			"source":  ep.Source,
			"line":    ep.Line,
		})
		batch.Queue(q, fileID, fa.RelPath, ep.Method+" "+route, "endpoint", meta)
	}

	// Intra-file call graph: caller symbol -> callee symbol (both in this file).
	for _, c := range fa.Calls {
		meta := jsonObj(map[string]any{"caller": c.Caller, "callee": c.Callee})
		batch.Queue(q, fileID, c.Caller, c.Callee, "calls", meta)
	}

	if batch.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert relationship: %w", err)
		}
	}
	return nil
}

// insertVectorChunks seeds the semantic layer with one chunk per exported
// symbol and per endpoint. Embeddings stay NULL until the embedding worker
// lands; the content is the real source line for the symbol.
func insertVectorChunks(ctx context.Context, tx pgx.Tx, fileID int64, fa *parser.FileAnalysis) error {
	const q = `
		INSERT INTO vector_chunks (file_id, chunk_type, symbol_name, start_line, end_line, content, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, NULL);`

	batch := &pgx.Batch{}
	for _, exp := range fa.Exports {
		content := lineSlice(fa.Content, exp.Line)
		if content == "" {
			content = exp.Kind + " " + exp.Name
		}
		batch.Queue(q, fileID, exp.Kind, exp.Name, exp.Line, exp.Line, content)
	}
	for _, ep := range fa.Endpoints {
		route := deriveRoutePath(fa.RelPath, ep.Path)
		content := lineSlice(fa.Content, ep.Line)
		if content == "" {
			content = ep.Method + " " + route
		}
		batch.Queue(q, fileID, "endpoint", ep.Method+" "+route, ep.Line, ep.Line, content)
	}

	if batch.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert vector_chunk: %w", err)
		}
	}
	return nil
}

// --- Read side (graph API) --------------------------------------------------

// FileRow is a code_files projection for graph assembly.
type FileRow struct {
	ID        int64
	RootPath  string
	FilePath  string
	Filename  string
	Language  string
	SizeBytes int64
}

// RelRow is an ast_relationships projection for graph assembly.
type RelRow struct {
	SourceSymbol     string
	TargetSymbol     string
	RelationshipType string
	Metadata         map[string]any
}

// AllFiles returns every code_files row (all repos), without the heavy content
// column.
func (s *Store) AllFiles(ctx context.Context) ([]FileRow, error) {
	return s.FilesByRoot(ctx, "")
}

// FilesByRoot returns code_files rows, optionally scoped to a single repo
// (root == "" returns all repos).
func (s *Store) FilesByRoot(ctx context.Context, root string) ([]FileRow, error) {
	q := `SELECT id, root_path, file_path, filename, language, size_bytes FROM code_files`
	var args []any
	if root != "" {
		q += ` WHERE root_path = $1`
		args = append(args, root)
	}
	q += ` ORDER BY file_path`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query code_files: %w", err)
	}
	defer rows.Close()

	var out []FileRow
	for rows.Next() {
		var f FileRow
		if err := rows.Scan(&f.ID, &f.RootPath, &f.FilePath, &f.Filename, &f.Language, &f.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan code_file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AllRelationships returns every ast_relationships row (all repos) with decoded
// metadata.
func (s *Store) AllRelationships(ctx context.Context) ([]RelRow, error) {
	return s.RelationshipsByRoot(ctx, "")
}

// RelationshipsByRoot returns ast_relationships rows, optionally scoped to a
// single repo (root == "" returns all repos).
func (s *Store) RelationshipsByRoot(ctx context.Context, root string) ([]RelRow, error) {
	q := `SELECT ar.source_symbol, ar.target_symbol, ar.relationship_type, ar.metadata FROM ast_relationships ar`
	var args []any
	if root != "" {
		q += ` JOIN code_files cf ON cf.id = ar.file_id WHERE cf.root_path = $1`
		args = append(args, root)
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query ast_relationships: %w", err)
	}
	defer rows.Close()

	var out []RelRow
	for rows.Next() {
		var r RelRow
		var raw []byte
		if err := rows.Scan(&r.SourceSymbol, &r.TargetSymbol, &r.RelationshipType, &raw); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
