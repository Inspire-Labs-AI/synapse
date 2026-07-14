package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"project-synapse/backend/internal/embed"
)

// ChunkInsert is one semantic chunk to persist, with its (optional) embedding.
type ChunkInsert struct {
	ChunkType  string
	SymbolName string
	StartLine  int
	EndLine    int
	Content    string    // structural header + code
	Embedding  []float32 // nil => stored as NULL
}

// ChunkHit is a vector-search result row.
type ChunkHit struct {
	FilePath   string
	SymbolName string
	ChunkType  string
	StartLine  int
	EndLine    int
	Content    string
	Distance   float64 // cosine distance (lower = closer)
}

// FunctionRow is one symbol-level chunk of a file (function/class/...), with its
// source code, for the canvas function nodes + the assistant's code panel.
type FunctionRow struct {
	Symbol    string `json:"symbol"`
	ChunkType string `json:"chunk_type"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Code      string `json:"code"`
}

// StripChunkHeader removes the "--- File/Context/Imports ---" preamble that the
// chunker prepends to each embeddable chunk, leaving just the source code for
// display.
func StripChunkHeader(content string) string {
	if strings.HasPrefix(content, "---\n") {
		if i := strings.Index(content, "\n---\n"); i >= 0 {
			return content[i+len("\n---\n"):]
		}
	}
	return content
}

// FileFunctions returns the symbol-level chunks (functions, classes, ...) of one
// file in a repo, ordered by position, with the chunk header stripped.
func (s *Store) FileFunctions(ctx context.Context, root, filePath string) ([]FunctionRow, error) {
	const q = `
		SELECT vc.symbol_name, vc.chunk_type, vc.start_line, vc.end_line, vc.content
		FROM vector_chunks vc
		JOIN code_files cf ON cf.id = vc.file_id
		WHERE cf.root_path = $1 AND cf.file_path = $2
		ORDER BY vc.start_line, vc.id;`

	rows, err := s.pool.Query(ctx, q, root, filePath)
	if err != nil {
		return nil, fmt.Errorf("file functions: %w", err)
	}
	defer rows.Close()

	out := []FunctionRow{}
	for rows.Next() {
		var f FunctionRow
		var content string
		if err := rows.Scan(&f.Symbol, &f.ChunkType, &f.StartLine, &f.EndLine, &content); err != nil {
			return nil, fmt.Errorf("scan function row: %w", err)
		}
		f.Code = StripChunkHeader(content)
		out = append(out, f)
	}
	return out, rows.Err()
}

// CallEdge is an intra-file call relationship (caller symbol -> callee symbol).
type CallEdge struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
}

// FileCallEdges returns the intra-file call graph for one file.
func (s *Store) FileCallEdges(ctx context.Context, root, filePath string) ([]CallEdge, error) {
	const q = `
		SELECT ar.source_symbol, ar.target_symbol
		FROM ast_relationships ar
		JOIN code_files cf ON cf.id = ar.file_id
		WHERE cf.root_path = $1 AND cf.file_path = $2 AND ar.relationship_type = 'calls';`

	rows, err := s.pool.Query(ctx, q, root, filePath)
	if err != nil {
		return nil, fmt.Errorf("file call edges: %w", err)
	}
	defer rows.Close()

	out := []CallEdge{}
	for rows.Next() {
		var c CallEdge
		if err := rows.Scan(&c.Caller, &c.Callee); err != nil {
			return nil, fmt.Errorf("scan call edge: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CallRow is one intra-file call edge with its owning file path.
type CallRow struct {
	File   string
	Caller string
	Callee string
}

// CallsByRoot returns every intra-file call edge in a repo, with its file — a
// single query for whole-repo analysis (e.g. dead-code detection).
func (s *Store) CallsByRoot(ctx context.Context, root string) ([]CallRow, error) {
	const q = `
		SELECT cf.file_path, ar.source_symbol, ar.target_symbol
		FROM ast_relationships ar
		JOIN code_files cf ON cf.id = ar.file_id
		WHERE cf.root_path = $1 AND ar.relationship_type = 'calls';`
	rows, err := s.pool.Query(ctx, q, root)
	if err != nil {
		return nil, fmt.Errorf("calls by root: %w", err)
	}
	defer rows.Close()
	out := []CallRow{}
	for rows.Next() {
		var c CallRow
		if err := rows.Scan(&c.File, &c.Caller, &c.Callee); err != nil {
			return nil, fmt.Errorf("scan call row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeclRow is one declared symbol (function/class/method/...) with its file + kind.
type DeclRow struct {
	File      string
	Symbol    string
	ChunkType string
}

// DeclarationsByRoot returns every code symbol chunked in a repo, with its file —
// a single query. Endpoint + markdown chunks are excluded (not code declarations).
func (s *Store) DeclarationsByRoot(ctx context.Context, root string) ([]DeclRow, error) {
	const q = `
		SELECT cf.file_path, vc.symbol_name, vc.chunk_type
		FROM vector_chunks vc
		JOIN code_files cf ON cf.id = vc.file_id
		WHERE cf.root_path = $1 AND vc.chunk_type NOT IN ('endpoint', 'myelin_doc');`
	rows, err := s.pool.Query(ctx, q, root)
	if err != nil {
		return nil, fmt.Errorf("declarations by root: %w", err)
	}
	defer rows.Close()
	out := []DeclRow{}
	for rows.Next() {
		var d DeclRow
		if err := rows.Scan(&d.File, &d.Symbol, &d.ChunkType); err != nil {
			return nil, fmt.Errorf("scan decl row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FuncCodeRow is one code symbol with its source (header-stripped on read), for
// whole-repo code scans (e.g. resource-leak detection).
type FuncCodeRow struct {
	File      string
	Symbol    string
	ChunkType string
	StartLine int
	EndLine   int
	Code      string
}

// FunctionsWithCodeByRoot returns every code symbol in a repo with its source —
// one query, for deterministic code scans. Endpoint + markdown chunks excluded;
// the structural header is stripped so callers see raw code.
func (s *Store) FunctionsWithCodeByRoot(ctx context.Context, root string) ([]FuncCodeRow, error) {
	const q = `
		SELECT cf.file_path, vc.symbol_name, vc.chunk_type, vc.start_line, vc.end_line, vc.content
		FROM vector_chunks vc
		JOIN code_files cf ON cf.id = vc.file_id
		WHERE cf.root_path = $1 AND vc.chunk_type NOT IN ('endpoint', 'myelin_doc')
		ORDER BY cf.file_path, vc.start_line;`
	rows, err := s.pool.Query(ctx, q, root)
	if err != nil {
		return nil, fmt.Errorf("functions with code: %w", err)
	}
	defer rows.Close()
	out := []FuncCodeRow{}
	for rows.Next() {
		var r FuncCodeRow
		var content string
		if err := rows.Scan(&r.File, &r.Symbol, &r.ChunkType, &r.StartLine, &r.EndLine, &content); err != nil {
			return nil, fmt.Errorf("scan func code row: %w", err)
		}
		r.Code = StripChunkHeader(content)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceChunks replaces all vector_chunks for one file with the supplied
// embedded chunks, in a single transaction. The file must already exist in
// code_files (persisted during ingestion).
func (s *Store) ReplaceChunks(ctx context.Context, rootPath, relPath string, chunks []ChunkInsert) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var fileID int64
	err = tx.QueryRow(ctx,
		`SELECT id FROM code_files WHERE root_path = $1 AND file_path = $2`,
		rootPath, relPath).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("code_file not found for %s (ingest it first)", relPath)
	}
	if err != nil {
		return fmt.Errorf("lookup file_id: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM vector_chunks WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("clear vector_chunks: %w", err)
	}

	const q = `
		INSERT INTO vector_chunks (file_id, chunk_type, symbol_name, start_line, end_line, content, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, $7::vector);`

	batch := &pgx.Batch{}
	for _, c := range chunks {
		var emb any // nil -> NULL
		if len(c.Embedding) > 0 {
			emb = embed.ToVectorLiteral(c.Embedding)
		}
		batch.Queue(q, fileID, c.ChunkType, c.SymbolName, c.StartLine, c.EndLine, c.Content, emb)
	}
	if batch.Len() > 0 {
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("insert chunk: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("close chunk batch: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit chunks: %w", err)
	}
	return nil
}

// VectorSearch returns the top-N chunks by cosine distance to the query vector
// (HNSW index, vector_cosine_ops).
func (s *Store) VectorSearch(ctx context.Context, queryVec []float32, limit int, root string) ([]ChunkHit, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}
	lit := embed.ToVectorLiteral(queryVec)

	args := []any{lit}
	where := "WHERE vc.embedding IS NOT NULL"
	if root != "" {
		args = append(args, root)
		where += fmt.Sprintf(" AND cf.root_path = $%d", len(args))
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT cf.file_path, vc.symbol_name, vc.chunk_type, vc.start_line, vc.end_line, vc.content,
		       (vc.embedding <=> $1::vector) AS distance
		FROM vector_chunks vc
		JOIN code_files cf ON cf.id = vc.file_id
		%s
		ORDER BY vc.embedding <=> $1::vector
		LIMIT $%d`, where, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var hits []ChunkHit
	for rows.Next() {
		var h ChunkHit
		if err := rows.Scan(&h.FilePath, &h.SymbolName, &h.ChunkType, &h.StartLine, &h.EndLine, &h.Content, &h.Distance); err != nil {
			return nil, fmt.Errorf("scan chunk hit: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// KeywordSearchFiles returns code_files whose path, filename, or raw content
// matches any of the ILIKE patterns (e.g. "%/category%", "%fetchCategories%").
func (s *Store) KeywordSearchFiles(ctx context.Context, patterns []string, root string) ([]FileRow, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	args := []any{patterns}
	q := `
		SELECT id, root_path, file_path, filename, language, size_bytes
		FROM code_files
		WHERE (file_path ILIKE ANY($1) OR filename ILIKE ANY($1) OR content ILIKE ANY($1))`
	if root != "" {
		args = append(args, root)
		q += fmt.Sprintf(" AND root_path = $%d", len(args))
	}
	q += ` ORDER BY file_path`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword file search: %w", err)
	}
	defer rows.Close()

	var out []FileRow
	for rows.Next() {
		var f FileRow
		if err := rows.Scan(&f.ID, &f.RootPath, &f.FilePath, &f.Filename, &f.Language, &f.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// KeywordSearchRelationships returns ast_relationships whose source or target
// symbol matches any ILIKE pattern — e.g. an endpoint row "GET /category".
func (s *Store) KeywordSearchRelationships(ctx context.Context, patterns []string, root string) ([]RelRow, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	args := []any{patterns}
	q := `
		SELECT ar.source_symbol, ar.target_symbol, ar.relationship_type, ar.metadata
		FROM ast_relationships ar`
	if root != "" {
		args = append(args, root)
		q += fmt.Sprintf(`
		JOIN code_files cf ON cf.id = ar.file_id
		WHERE (ar.source_symbol ILIKE ANY($1) OR ar.target_symbol ILIKE ANY($1)) AND cf.root_path = $%d`, len(args))
	} else {
		q += `
		WHERE ar.source_symbol ILIKE ANY($1) OR ar.target_symbol ILIKE ANY($1)`
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword relationship search: %w", err)
	}
	defer rows.Close()

	var out []RelRow
	for rows.Next() {
		var r RelRow
		var raw []byte
		if err := rows.Scan(&r.SourceSymbol, &r.TargetSymbol, &r.RelationshipType, &raw); err != nil {
			return nil, fmt.Errorf("scan rel row: %w", err)
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
