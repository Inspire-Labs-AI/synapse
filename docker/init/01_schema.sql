-- ============================================================================
-- Project Synapse — Schema bootstrap
-- Runs automatically on first container boot (docker-entrypoint-initdb.d).
-- Establishes the three core relations: code_files, ast_relationships,
-- and vector_chunks (with an HNSW index for approximate nearest-neighbour
-- semantic search).
-- ============================================================================

-- Vector type + similarity operators.
CREATE EXTENSION IF NOT EXISTS vector;

-- ----------------------------------------------------------------------------
-- users
--   OAuth identities (GitHub / Google) + the local-dev session. Created the
--   first time a user signs in. code_files.user_id (below) optionally attributes
--   an ingested workspace to the user who created it (multi-user tracking).
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT        NOT NULL,
    name       TEXT        NOT NULL DEFAULT '',
    avatar_url TEXT        NOT NULL DEFAULT '',
    provider   TEXT        NOT NULL DEFAULT '', -- github | google | local
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_users_email UNIQUE (email)
);

-- ----------------------------------------------------------------------------
-- code_files
--   One row per ingested source file. The structural + semantic layers both
--   hang off this table via file_id foreign keys.
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_files (
    id            BIGSERIAL PRIMARY KEY,
    root_path     TEXT        NOT NULL,            -- ingestion root that owned this file
    file_path     TEXT        NOT NULL,            -- path relative to root_path
    filename      TEXT        NOT NULL DEFAULT '', -- basename, denormalised for fast node labels
    language      TEXT        NOT NULL DEFAULT 'typescript', -- typescript* | javascript* | markdown (myelin docs)
    content       TEXT        NOT NULL DEFAULT '', -- raw file contents
    content_hash  TEXT        NOT NULL,            -- sha256 of file contents for change detection
    size_bytes    BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A file is uniquely identified by its root + relative path.
    CONSTRAINT uq_code_files_root_path UNIQUE (root_path, file_path)
);

CREATE INDEX IF NOT EXISTS idx_code_files_language ON code_files (language);

-- Optional multi-user attribution: which signed-in user ingested this workspace.
-- Nullable so single-user / local-dev ingestion keeps working untouched.
ALTER TABLE code_files ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_code_files_user_id ON code_files (user_id);

-- ----------------------------------------------------------------------------
-- ast_relationships
--   Edges of the dependency graph. Each row links a source symbol (within a
--   file) to a target symbol, typed by the kind of relationship (import,
--   call, extends, implements, endpoint, ...).
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ast_relationships (
    id                BIGSERIAL PRIMARY KEY,
    file_id           BIGINT      NOT NULL REFERENCES code_files (id) ON DELETE CASCADE,
    source_symbol     TEXT        NOT NULL,        -- e.g. "UserService.create"
    target_symbol     TEXT        NOT NULL,        -- e.g. "Repository.save"
    relationship_type TEXT        NOT NULL,        -- imports | exports | endpoint | markdown-link
    -- Per-type payload. "exports" rows may carry "dendrite_patterns": ["decorator",
    -- "closure", "generic_wrapper", ...] — discovered structural idioms (Dendrite Callouts).
    metadata          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ast_rel_file_id   ON ast_relationships (file_id);
CREATE INDEX IF NOT EXISTS idx_ast_rel_type      ON ast_relationships (relationship_type);
CREATE INDEX IF NOT EXISTS idx_ast_rel_source    ON ast_relationships (source_symbol);
CREATE INDEX IF NOT EXISTS idx_ast_rel_target    ON ast_relationships (target_symbol);

-- ----------------------------------------------------------------------------
-- vector_chunks
--   Semantic layer. Each chunk is a structurally-bounded slice of code (a
--   function/class body) OR a markdown section. Dimension 1024 matches the
--   configured embedder (Ollama mxbai-embed-large / Voyage voyage-code-3 /
--   OpenAI text-embedding-3-* with dimensions=1024). This VALUE MUST EQUAL the Go
--   ingest layer's SYNAPSE_EMBED_DIM — if you swap to a 768-dim model (Jina
--   jina-embeddings-v2-base-code, nomic-embed-text), change VECTOR(1024) below to
--   VECTOR(768) AND set SYNAPSE_EMBED_DIM=768 (and rebuild the HNSW index). chunk_type
--   "myelin_doc" tags markdown sections so human docs blend seamlessly into
--   hybrid-RAG search (Myelin Insulation).
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS vector_chunks (
    id          BIGSERIAL PRIMARY KEY,
    file_id     BIGINT       NOT NULL REFERENCES code_files (id) ON DELETE CASCADE,
    chunk_type  TEXT         NOT NULL,             -- function | class | interface | variable | ... | myelin_doc (markdown)
    symbol_name TEXT         NOT NULL,
    start_line  INTEGER      NOT NULL DEFAULT 0,
    end_line    INTEGER      NOT NULL DEFAULT 0,
    content     TEXT         NOT NULL,
    embedding   VECTOR(1024),                      -- nullable until embeddings are computed; MUST match SYNAPSE_EMBED_DIM
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_vector_chunks_file_id ON vector_chunks (file_id);
-- Fast filtering by kind (e.g. only code, or only "myelin_doc" markdown sections).
CREATE INDEX IF NOT EXISTS idx_vector_chunks_chunk_type ON vector_chunks (chunk_type);

-- HNSW index for approximate nearest-neighbour search over the semantic layer.
-- vector_cosine_ops pairs with cosine distance (<=>), the typical choice for
-- normalised text embeddings.
CREATE INDEX IF NOT EXISTS idx_vector_chunks_embedding_hnsw
    ON vector_chunks
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- ----------------------------------------------------------------------------
-- embed_config
--   Single-row bookkeeping of the embedder (model + dim) that currently populates
--   vector_chunks. The backend reconciles the embedding column + HNSW index to the
--   active SYNAPSE_EMBED_MODEL / SYNAPSE_EMBED_DIM on startup (see store.Recon-
--   cileEmbedding); this row lets it detect a model/dim change across restarts.
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS embed_config (
    id         INT         PRIMARY KEY DEFAULT 1,
    model      TEXT        NOT NULL DEFAULT '',
    dim        INT         NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT embed_config_singleton CHECK (id = 1)
);

-- ----------------------------------------------------------------------------
-- updated_at maintenance for code_files.
-- ----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_code_files_updated_at ON code_files;
CREATE TRIGGER trg_code_files_updated_at
    BEFORE UPDATE ON code_files
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- ----------------------------------------------------------------------------
-- Trigram (pg_trgm) GIN indexes accelerate the ILIKE keyword / graph searches
-- used by the hybrid query layer and the blueprint discovery engine, keeping
-- substring lookups fast as the codebase grows.
-- ----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS idx_code_files_path_trgm
    ON code_files USING gin (file_path gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_code_files_content_trgm
    ON code_files USING gin (content gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_ast_rel_source_trgm
    ON ast_relationships USING gin (source_symbol gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_ast_rel_target_trgm
    ON ast_relationships USING gin (target_symbol gin_trgm_ops);
