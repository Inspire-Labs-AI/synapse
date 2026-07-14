package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ReconcileEmbedding makes the vector schema match the active embedder so the
// model / dimension can be changed purely via .env. On startup it:
//   - creates the embed_config bookkeeping row if missing;
//   - if the vector_chunks.embedding column dimension differs from `dim`, re-types
//     the column + rebuilds the HNSW index (clearing the now-incompatible chunks);
//   - if only the embedder identity changed at the same dim, clears the chunks
//     (old vectors came from a different model and aren't comparable);
//   - otherwise does nothing.
//
// It records the active (name, dim) so unchanged restarts are no-ops. Returns a
// human-readable action ("" when nothing changed) for the caller to log; after a
// non-empty action the repos must be re-ingested to repopulate the vectors.
func (s *Store) ReconcileEmbedding(ctx context.Context, name string, dim int) (string, error) {
	if dim <= 0 {
		return "", nil
	}

	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS embed_config (
			id         int PRIMARY KEY DEFAULT 1,
			model      text        NOT NULL DEFAULT '',
			dim        int         NOT NULL DEFAULT 0,
			updated_at timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT embed_config_singleton CHECK (id = 1)
		)`); err != nil {
		return "", fmt.Errorf("ensure embed_config: %w", err)
	}

	colDim, err := s.embeddingColumnDim(ctx)
	if err != nil {
		return "", err
	}

	var storedModel string
	var storedDim int
	seeded := true
	if err := s.pool.QueryRow(ctx, `SELECT model, dim FROM embed_config WHERE id = 1`).Scan(&storedModel, &storedDim); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			seeded = false
		} else {
			return "", fmt.Errorf("read embed_config: %w", err)
		}
	}

	save := func() error {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO embed_config (id, model, dim, updated_at) VALUES (1, $1, $2, now())
			ON CONFLICT (id) DO UPDATE SET model = EXCLUDED.model, dim = EXCLUDED.dim, updated_at = now()`,
			name, dim)
		return err
	}

	// Column dimension already matches the configured dim.
	if colDim == dim {
		if !seeded {
			// First run against a DB whose column already fits — adopt the existing
			// data as-is (do NOT wipe), then just record the identity.
			return "", save()
		}
		if storedModel == name && storedDim == dim {
			return "", nil // nothing changed
		}
		// Same dim, different embedder → existing vectors are from another model.
		if err := s.clearChunks(ctx); err != nil {
			return "", err
		}
		if err := save(); err != nil {
			return "", err
		}
		return fmt.Sprintf("embedder changed (%s@%d → %s@%d); cleared vector chunks — re-ingest to repopulate",
			storedModel, storedDim, name, dim), nil
	}

	// Column dimension differs → re-type the column + HNSW index (needs clearing).
	if err := s.clearChunks(ctx); err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `DROP INDEX IF EXISTS idx_vector_chunks_embedding_hnsw`); err != nil {
		return "", fmt.Errorf("drop hnsw index: %w", err)
	}
	// dim is a validated positive int — safe to interpolate (not a bind param slot).
	if _, err := s.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE vector_chunks ALTER COLUMN embedding TYPE vector(%d)`, dim)); err != nil {
		return "", fmt.Errorf("alter embedding dimension: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_vector_chunks_embedding_hnsw
			ON vector_chunks USING hnsw (embedding vector_cosine_ops)
			WITH (m = 16, ef_construction = 64)`); err != nil {
		return "", fmt.Errorf("create hnsw index: %w", err)
	}
	if err := save(); err != nil {
		return "", err
	}
	from := "unset"
	if colDim > 0 {
		from = strconv.Itoa(colDim)
	}
	return fmt.Sprintf("dimension changed %s → %d (model %s); migrated column + HNSW index and cleared vector chunks — re-ingest to repopulate",
		from, dim, name), nil
}

// embeddingColumnDim returns the declared dimension of vector_chunks.embedding
// (0 if the column has no dimension modifier).
func (s *Store) embeddingColumnDim(ctx context.Context) (int, error) {
	var typ string
	err := s.pool.QueryRow(ctx, `
		SELECT format_type(atttypid, atttypmod)
		FROM pg_attribute
		WHERE attrelid = 'vector_chunks'::regclass AND attname = 'embedding' AND NOT attisdropped`).Scan(&typ)
	if err != nil {
		return 0, fmt.Errorf("read embedding column type: %w", err)
	}
	open := strings.IndexByte(typ, '(')
	closep := strings.IndexByte(typ, ')')
	if open < 0 || closep <= open {
		return 0, nil // bare "vector" — no dimension modifier
	}
	n, err := strconv.Atoi(strings.TrimSpace(typ[open+1 : closep]))
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func (s *Store) clearChunks(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `TRUNCATE vector_chunks`); err != nil {
		return fmt.Errorf("clear vector chunks: %w", err)
	}
	return nil
}
