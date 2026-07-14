# Project Synapse — Architecture

> **Read this first (TL;DR for an LLM or a new engineer).**
> Project Synapse is a **local-first codebase-intelligence platform**. You point it at a
> Git repository (or a local folder); it **clones, parses the true AST, builds a typed
> dependency graph, and embeds the code into a pgvector semantic index**. On top of that
> single ingested model it serves five capabilities:
> 1. **Living documentation** (`/docs`) — LLM-written guide + AST-derived API reference.
> 2. **Interactive topology** (`/workspace`) — a React Flow canvas of folders → files →
>    functions, with a **hybrid-RAG chat** that traces answers onto the graph.
> 3. **System-architecture view** (`/architecture`) — the LLM clusters the repo into
>    components/layers with data-flow edges.
> 4. **Capability/reuse blueprinting** — score how much of a proposed feature already
>    exists vs. must be built.
> 5. **Multi-repo** — keep many repos ingested and switch between them.
>
> Backend = **Go** (concurrent ingestion, raw `pgx`/SQL). Datastore = **PostgreSQL 16 +
> pgvector (HNSW) + pg_trgm** in Docker. Parser = a **Node.js subprocess using the
> TypeScript compiler API**. Frontend = **Next.js 16 (App Router) + React Flow + GSAP +
> Tailwind v4**. LLMs/embeddings are **provider-abstracted** (Anthropic / OpenAI /
> OpenRouter / local Ollama) with offline deterministic fallbacks. The reference local
> setup runs **fully offline-capable**: Ollama `mxbai-embed-large` (768-d) for embeddings
> and `qwen2.5-coder` (or OpenRouter) for chat.

---

## 0. The product flow (what a user does)

```
┌──────────┐   enter repo URL    ┌──────────┐   POST /api/ingest    ┌──────────────┐
│   /  (landing)  ├───────────────────▶│  ingest  ├──────────────────────▶│  redirect to  │
│  repo input +   │   (clone+parse+    │ pipeline │  returns root_path    │  /docs?repo=… │
│ recent repos    │    embed)          └──────────┘                       └──────┬───────┘
└──────────┘                                                                     │
                                                                                 ▼
                          ┌──────────────────────  /docs?repo=…  ──────────────────────┐
                          │  LLM narrative (Introduction, Architecture)                 │
                          │  + AST-derived per-folder reference (files → functions → code)
                          │  top buttons:  [◇ Architecture]   [Workspace →]             │
                          └───────────────┬───────────────────────────┬───────────────┘
                                          │                           │
                                          ▼                           ▼
                         /architecture?repo=…              /workspace?repo=…
                         component/layer diagram           topology canvas + hybrid-RAG chat
                         (React Flow) + side panel         + blueprint discovery
```

Every page is scoped by a `?repo=<root_path>` query param; `root_path` is the canonical
repo identifier (see §5).

---

## 1. Repository layout

```
project-synapse/
├── backend/                         Go module: project-synapse/backend
│   ├── cmd/server/main.go           entrypoint: connect DB → build providers → (optional) ingest → serve
│   ├── internal/
│   │   ├── config/                  env-driven configuration (config.go)
│   │   ├── ingest/                  walker.go, worker.go (pool), enrich.go, handler.go (clone-on-demand)
│   │   ├── parser/                  ast.go (NodeParser subprocess), resolve.go (import resolver), types.go
│   │   ├── chunk/                   structural semantic chunker (chunk.go)
│   │   ├── embed/                   Embedder iface: deterministic | openai | ollama (+ cache.go)
│   │   ├── llm/                     ChatClient iface: anthropic | openai/openrouter/ollama (OpenAI-compat) | template
│   │   ├── rag/                     hybrid retrieval + context assembly + JSON answer contract
│   │   ├── blueprint/               intent extraction + reuse scoring (green/yellow/red)
│   │   ├── docs/                    hybrid documentation generator (LLM narrative + AST reference)
│   │   ├── architecture/           LLM system-architecture clustering (components + edges)
│   │   ├── store/                   pgx pool + ALL SQL (store.go, rag.go, helpers.go)
│   │   └── api/                     HTTP server, routes, graph builder (server.go, graph.go, query.go, blueprint.go)
│   ├── tools/tsparser/parse.mjs     TypeScript-compiler AST extractor (+ local `typescript` install)
│   ├── .env                         backend config (read into env by run.ps1 — see §15)
│   └── run.ps1                      loads .env → `go run ./cmd/server` (Windows helper)
├── frontend/                        Next.js 16 app (App Router)
│   └── app/
│       ├── page.tsx                 / — landing (repo input → ingest → /docs)
│       ├── docs/page.tsx            /docs — documentation viewer
│       ├── architecture/page.tsx    /architecture — component/layer diagram
│       └── workspace/
│           ├── page.tsx             /workspace — canvas + chat + blueprint + repo switcher
│           ├── components/          WorkspaceCanvas, nodes, ChatPanel, BlueprintPanel, ExecutionPanel,
│           │                        FunctionList, RepoSelector
│           └── lib/                 api.ts (all fetchers + types), graph.ts (folder-nesting layout)
├── docker/                          docker-compose.yml (pgvector/pgvector:pg16) + init/01_schema.sql
└── docs/ARCHITECTURE.md             this document
```

---

## 2. End-to-end data flow

```mermaid
flowchart LR
  subgraph Ingest
    URL[Git URL or local dir] --> CLONE[Clone-on-demand<br/>ingest/handler.go]
    CLONE --> WALK[Walker<br/>ingest/walker.go]
    WALK --> POOL[Worker pool<br/>ingest/worker.go]
    POOL --> PARSE[NodeParser → parse.mjs<br/>TypeScript AST]
    PARSE --> RESOLVE[Import resolver<br/>parser/resolve.go]
    RESOLVE --> PERSIST[(code_files +<br/>ast_relationships)]
    PERSIST --> ENRICH[Chunker → Embedder<br/>chunk + embed/ollama]
    ENRICH --> VEC[(vector_chunks<br/>VECTOR 1024 · HNSW)]
  end
  PERSIST --> GRAPH[GET /api/graph/data]
  VEC --> QUERY[POST /api/query<br/>hybrid RAG]
  PERSIST --> QUERY
  VEC --> BP[POST /api/blueprint/discover]
  VEC --> DOCS[GET /api/docs<br/>LLM narrative + AST ref]
  PERSIST --> DOCS
  PERSIST --> ARCH[GET /api/architecture<br/>LLM clustering]
  GRAPH --> CANVAS[/workspace canvas/]
  QUERY --> CANVAS
  DOCS --> DOCPAGE[/docs page/]
  ARCH --> ARCHPAGE[/architecture page/]
```

---

## 3. Ingestion pipeline

Two entry points, the same pipeline (`ingest.Pipeline.Run(ctx, root)`):
- **Clone-on-demand** — `POST /api/ingest {repo_url, pat}` → `ingest/handler.go` shallow-clones
  (`git clone --depth 1`) into `SYNAPSE_CLONES_DIR` (default `.synapse-clones/`), injecting the
  PAT into the in-memory URL (never logged), then runs the pipeline on the clone. Returns
  `root_path` (the clone's absolute path) so the frontend can redirect to `/docs?repo=…`.
- **Startup ingest** — set `SYNAPSE_INGEST_ROOT=<local dir>` and the server ingests it before serving.

Pipeline stages:
1. **Discover** (`walker.go`): `filepath.WalkDir` collects `.ts/.tsx/.js/.jsx` files, skipping
   `node_modules`, `.git`, `.next`, `dist`, `build`, `coverage`, `out`, `vendor`. (MVP scope is
   the TypeScript/JavaScript family only.)
2. **Parse** (`parser/ast.go` + `tools/tsparser/parse.mjs`): a bounded worker pool
   (`SYNAPSE_WORKERS`, default 4) spawns `node parse.mjs` per file, pipes the source over stdin,
   and reads back a **true compiler AST extraction** (`ts.createSourceFile`) of:
   - **imports** — `import` / `export … from` / `require` / dynamic `import()` with named bindings,
   - **exports** — functions, classes, interfaces, types, enums, variables, defaults,
   - **endpoints** — Express/Fastify `router.get('/x')` and Next.js App Router `export function GET()`,
   - **declarations** — every top-level declaration with line spans (used by the chunker).
3. **Resolve** (`parser/resolve.go`): import specifiers are classified against the full ingest set —
   relative paths → internal file edges (with extension/`index` fallbacks); bare specifiers →
   external module roots (`@scope/pkg/sub` → `@scope/pkg`).
4. **Persist** (`store/store.go`): one transaction per file upserts `code_files` (path, filename,
   **raw content**, sha256 hash, size) and replaces the file's typed `ast_relationships`
   (`imports` / `exports` / `endpoint`). Delete-then-insert keyed by `file_id` makes re-ingestion
   idempotent.
5. **Enrich** (`ingest/enrich.go`): per file, chunk → embed → `ReplaceChunks` into `vector_chunks`
   (see §7).

---

## 4. The TypeScript AST parser (Node subprocess)

The Go side shells out because the **TypeScript compiler API** is the source of truth for TS/JS
structure. `parser.NodeParser` (`ast.go`) runs `node parse.mjs` with the file source on stdin and
decodes a JSON `FileAnalysis` (imports, exports, endpoints, declarations). `parse.mjs` lives in
`tools/tsparser/` with its own local `typescript` dependency. `Verify()` checks `node --version`
before ingestion so a misconfigured environment fails fast. The Go program's working directory
must be `backend/` so `SYNAPSE_PARSER_DIR=tools/tsparser` resolves.

---

## 5. The multi-repo model

The system holds **many repos at once**. There is no separate `repos` table — a repo **is** its
`root_path` (the absolute ingest path; for clones, `…/.synapse-clones/<owner-repo>`). Everything
keys off it:
- `GET /api/repos` → `[{root_path, name, files, chunks}]` (most-recently-updated first) drives the
  landing-page list and the workspace repo switcher.
- Every read endpoint accepts `?repo=<root_path>` and scopes its SQL with
  `WHERE root_path = $1` (graph) or a join to `code_files` (vector/keyword search). An empty/absent
  `repo` means "all repos" for back-compat.
- `DELETE /api/repos?repo=<root_path>` removes a repo (`DELETE FROM code_files …`, cascading to
  relationships + chunks).
- Re-ingesting the **same** `root_path` replaces that repo's rows in place (idempotent upsert);
  ingesting a **new** root adds alongside the others.

The frontend passes the active `root_path` to graph/query/discover/docs/architecture calls; the
`RepoSelector` in the workspace header (and the landing-page list) switches it.

---

## 6. Database schema

PostgreSQL 16 with `vector` (pgvector) and `pg_trgm`. Bootstrapped by `docker/init/01_schema.sql`
on first container boot (init scripts only run on an empty data dir — changing the embedding
dimension therefore requires `docker compose … down -v && up -d`).

### `code_files` — one row per ingested source file
| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | PK |
| `root_path` | `TEXT NOT NULL` | the repo id (ingestion root) |
| `file_path` | `TEXT NOT NULL` | relative to root (forward-slashed) |
| `filename` | `TEXT NOT NULL DEFAULT ''` | basename (denormalised for node labels) |
| `language` | `TEXT NOT NULL DEFAULT 'typescript'` | |
| `content` | `TEXT NOT NULL DEFAULT ''` | raw file contents |
| `content_hash` | `TEXT NOT NULL` | sha256 (change detection) |
| `size_bytes` | `BIGINT NOT NULL DEFAULT 0` | |
| `created_at` / `updated_at` | `TIMESTAMPTZ DEFAULT now()` | `updated_at` via trigger |

Constraint `UNIQUE (root_path, file_path)`. Indexes: btree `language`; GIN trigram on `file_path`
and `content`. Trigger `trg_code_files_updated_at`.

### `ast_relationships` — typed dependency-graph edges
| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | PK |
| `file_id` | `BIGINT NOT NULL` | FK → `code_files(id)` `ON DELETE CASCADE` |
| `source_symbol` | `TEXT NOT NULL` | owning file path |
| `target_symbol` | `TEXT NOT NULL` | resolved file / module / export / `"METHOD /path"` |
| `relationship_type` | `TEXT NOT NULL` | `imports` \| `exports` \| `endpoint` |
| `metadata` | `JSONB NOT NULL DEFAULT '{}'` | per-type (see below) |

Indexes: btree on `file_id`, `relationship_type`, `source_symbol`, `target_symbol`; GIN trigram on
`source_symbol`, `target_symbol`. `metadata`: `imports` → `{specifier, symbols, kind, line,
external, unresolved}`; `exports` → `{kind, isDefault, line}`; `endpoint` → `{method, path,
handler, source, line}`.

### `vector_chunks` — semantic layer
| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | PK |
| `file_id` | `BIGINT NOT NULL` | FK → `code_files(id)` `ON DELETE CASCADE` |
| `chunk_type` | `TEXT NOT NULL` | `function` \| `class` \| `interface` \| `variable` \| … |
| `symbol_name` | `TEXT NOT NULL` | |
| `start_line` / `end_line` | `INTEGER` | |
| `content` | `TEXT NOT NULL` | structural header + code (the embedded text) |
| `embedding` | **`VECTOR(1024)`** | nullable until embedded — **matches `mxbai-embed-large`** |
| `created_at` | `TIMESTAMPTZ DEFAULT now()` | |

**HNSW** index `idx_vector_chunks_embedding_hnsw USING hnsw (embedding vector_cosine_ops) WITH (m=16,
ef_construction=64)` — approximate nearest-neighbour over cosine distance (`<=>`).

> **Dimension is coupled to the embedder.** The column is `VECTOR(1024)` for the current default
> (`mxbai-embed-large`). Switching embedders means editing the schema dimension, recreating the
> volume, and re-ingesting (embeddings are not comparable across models). See §8.

---

## 7. Semantic layer — chunking & embeddings

### Chunking (`chunk/chunk.go`)
Code is split on **structural boundaries** (declaration line spans), not arbitrary character
counts. Declarations longer than `maxLines = 50` are **windowed** (8-line overlap) into multiple
chunks. Each chunk's embedded text is prefixed with a plain-text structural header:
```
---
File: <relPath>
Context: <kind> <symbol>
Imports/Dependencies: <resolved deps>
---
<raw code>
```
The header injects file + symbol + dependency context so the embedding captures *where* the code
lives, not just the tokens. The full `content` (header + code) is stored; the chunker's 50-line
window keeps chunks precise and within small embedder context windows.

### Embedding (`embed/ollama.go` and friends)
The chunk batch is vectorized through the configured `Embedder`. Files embed concurrently (worker
pool); each file's chunks embed together. `ReplaceChunks` (`store/rag.go`) upserts the vectors,
replacing the file's prior chunks in one transaction.

**Context-window handling (important):** `mxbai-embed-large` caps at **512 tokens** (vs nomic's
2048). Token-dense code (wordlists, punctuation-heavy) can overflow even after a char cap, so
`OllamaEmbedder.embedOne` truncates the input (1500-char initial cap) and, on an Ollama "exceeds the
context length" error, **retries with progressive ~30% truncation** until it fits. This guarantees
every chunk gets an embedding (no silent retrieval gaps); only the *embedding input* is shortened,
never the stored source.

---

## 8. Providers — embeddings & LLM

Both are abstracted behind Go interfaces, selected by env config, with offline fallbacks.

### Embeddings (`embed.New`, provider = `SYNAPSE_EMBED_PROVIDER`)
| Provider | Model (default) | Dim | Notes |
|---|---|---|---|
| `ollama` | **`mxbai-embed-large`** (current) | **1024** | local, offline, free; dim is configurable (`SYNAPSE_EMBED_DIM`) and reported by `Dimensions()` |
| `openai` | `text-embedding-3-small` | 1536 | API; respects `OPENAI_BASE_URL` |
| `deterministic` | feature-hashing bag-of-tokens | `SYNAPSE_EMBED_DIM` | offline fallback, no network/key |
| `auto` | openai if `OPENAI_API_KEY` set, else deterministic | — | |

The **query embedder must match the stored embedder** (same model → same vector space); the app
uses one embedder for both ingest and query. A small in-memory cache (`embed/cache.go`) collapses
repeated term embeddings so blueprint scoring stays fast.

### LLM (`llm.NewChatClient`, provider = `SYNAPSE_LLM_PROVIDER`)
`auto` walks a preference chain — **first present API key wins, local Ollama is the floor:**
```
ANTHROPIC_API_KEY → OPENAI_API_KEY → OPENROUTER_API_KEY → ollama (local)
```
| Provider | Transport | Default model |
|---|---|---|
| `anthropic` | Claude Messages API (official SDK) | `claude-opus-4-8` |
| `openai` | OpenAI chat completions | `gpt-4o` |
| `openrouter` | OpenAI-compatible (`https://openrouter.ai/api/v1`) | `openai/gpt-4o-mini` |
| `ollama` | OpenAI-compatible (`<OLLAMA_HOST>/v1`) | `qwen2.5-coder:3b` |
| `template` | offline deterministic responder | — |

OpenRouter and Ollama reuse the OpenAI chat client (they're OpenAI-compatible) with their own base
URL; `SYNAPSE_LLM_MODEL` overrides the model for whichever provider `auto` selects (leave empty to
use per-provider defaults). The chat client requests `response_format: json_object`. Its HTTP
timeout is **5 minutes** so slow local CPU inference returns an answer instead of a 500.

---

## 9. Hybrid-RAG query (`rag/rag.go`, `POST /api/query`)

Two retrieval strategies, scoped to `repo`, merged into one context window:
- **Vector search** — embed the question → cosine ANN over `vector_chunks` (`embedding <=> $1` on
  the HNSW index) → top-`SYNAPSE_RAG_TOPK` (default 5) code blocks.
- **Keyword / graph match** — extract literals (routes like `/category`, identifiers) → `ILIKE`
  over `code_files` + `ast_relationships` (accelerated by `pg_trgm` GIN indexes).

Results are de-duplicated and assembled into a window combining **architectural facts** (AST edges)
with **semantic code fragments**, then handed to the LLM (or the offline template responder), which
must answer in the strict JSON contract:
```json
{ "answer": "markdown", "highlighted_files": ["a.ts"], "execution_flow": ["…"], "functions": [
  { "file":"client/crypto.js", "symbol":"deriveKey", "chunk_type":"function",
    "start_line":165, "end_line":190, "code":"…" } ] }
```
`functions` is the set of semantically-retrieved symbol-level chunks (header-stripped code) — the
"responsible functions" surfaced both in the chat panel (expandable code) and as `ƒ` nodes on the
canvas. If the model returns malformed JSON the orchestrator degrades gracefully (uses the raw text
+ derived files/flow) rather than failing.

---

## 10. Blueprint discovery (`blueprint/`, `POST /api/blueprint/discover`)

Turns a feature description into a reuse blueprint:
1. **Intent extraction** (`intent.go`): the LLM decomposes the text into `entities` + `actions`
   (offline heuristic fallback uses a verb lexicon + noun extraction).
2. **Concurrent scoring** (`engine.go`): for each intent, bounded goroutines
   (≤ `SYNAPSE_BLUEPRINT_CONCURRENCY`, kept under the DB pool of 8) run semantic + relational search
   (scoped to `repo`) and categorize:
   - 🟢 **Green** (≥0.85): a dedicated structure (symbol/endpoint/filename) → *reuse*.
   - 🟡 **Yellow** (0.40–0.84): partial/structural mention → *extend*.
   - 🔴 **Red** (<0.40): absent → *build new* (a speculative gap node).
3. **Assembly**: highlights (green/yellow node ids), speculative **gap** nodes + dashed gap-edges,
   and a diff summary (extend vs create) — all React-Flow-ready, plus a `reuse_score`.

---

## 11. Documentation engine (`docs/`, `GET /api/docs?repo=…`)

**Hybrid** generation, cached per repo (in-memory; `&refresh=true` regenerates):
- **Narrative** (LLM): a single call produces `{introduction, architecture}` markdown, grounded in
  a compact repo summary (file tree + exported symbols + endpoints). Falls back to a deterministic
  intro + folder-structure description when no LLM is configured.
- **Reference** (deterministic): one section per top-level folder, each listing its files and their
  symbol-level functions (with code) straight from `vector_chunks` via `FileFunctions`.

Response: `{repo, name, sections:[{id, title, kind:"narrative"|"module", group, content?, files?}]}`.
The `/docs` page renders this as a real docs site: left section nav (Overview / Reference groups),
center markdown-or-reference content, right "On this page" TOC.

---

## 12. Architecture engine (`architecture/`, `GET /api/architecture?repo=…`)

The LLM **clusters the repo into a small set of architectural components** (3–7 typically), each
with a `layer` (`frontend` | `backend` | `data` | `external` | `shared`), description, and member
files, plus directed **data-flow edges**. Cached per repo (`&refresh=true` regenerates). Edges
referencing unknown component ids are dropped. With no LLM, a deterministic fallback clusters by
top-level folder with inter-folder import edges.

Response: `{repo, name, summary, components:[{id, name, layer, description, files}], edges:[{source,
target, label}]}`. The `/architecture` page lays components out in **per-layer columns** on a React
Flow canvas (color-coded), with a side panel listing each component, its description, and files.

---

## 13. API route registry

Base URL `http://localhost:8080` (`SYNAPSE_HTTP_ADDR`). Permissive CORS (`GET, POST, DELETE,
OPTIONS`). JSON everywhere. All read endpoints take `?repo=<root_path>` (empty = all repos).

| Method & path | Purpose |
|---|---|
| `GET /healthz` | liveness + DB ping |
| `GET /api/repos` | list ingested repos `{repos:[{root_path,name,files,chunks}]}` |
| `DELETE /api/repos?repo=` | remove a repo (cascade) |
| `POST /api/ingest` | clone-on-demand `{repo_url, pat}` → ingest → `{root_path, files_*, chunks_embedded, errors}` |
| `GET /api/graph/data?repo=` | React-Flow topology (nodes/edges); node ids `file:<path>` / `module:<name>` / `endpoint:<file>:<METHOD /path>` |
| `GET /api/file/functions?repo=&path=` | symbol-level functions (with code) of one file (canvas expand-on-click) |
| `POST /api/query` | hybrid-RAG answer `{question, repo}` → `{answer, highlighted_files, execution_flow, functions}` |
| `POST /api/blueprint/discover` | reuse blueprint `{description, repo}` → matches/highlights/gaps/diff |
| `GET /api/docs?repo=` (`&refresh=`) | generated documentation (narrative + reference) |
| `GET /api/architecture?repo=` (`&refresh=`) | generated system-architecture (components + edges) |

---

## 14. Frontend

Next.js 16 App Router, all pages client components. `app/workspace/lib/api.ts` is the single source
of API fetchers + TS types; `API_BASE` defaults to `http://localhost:8080`
(`NEXT_PUBLIC_SYNAPSE_API`). Pages read `?repo=` from `window.location.search`.

### `/` landing (`app/page.tsx`)
Hero + repo URL/PAT input (advanced foldout for private repos) with a terminal-style ingest
animation, plus a "recently analyzed" list (`GET /api/repos`). On submit → `POST /api/ingest` →
`router.push('/docs?repo='+root_path)`.

### `/docs` (`app/docs/page.tsx`)
The documentation viewer described in §11 — section nav, markdown/reference content, TOC, and top
buttons to `/architecture` and `/workspace` (carrying `?repo=`).

### `/architecture` (`app/architecture/page.tsx`)
The component/layer diagram from §12 on a React Flow canvas + side panel.

### `/workspace` (`app/workspace/page.tsx`)
60/40 split: the canvas (left) and a tabbed **Assistant** / **Blueprint** panel + execution
terminal (right). Header has the `RepoSelector` (switch/remove repos). Reads `?repo=` to preselect.

**The canvas (`components/WorkspaceCanvas.tsx`, `lib/graph.ts`, `components/nodes.tsx`):**
- **Nested folder layout** (`graph.ts`): the frontend derives the folder tree from file paths and
  runs a recursive row-packing layout — files pack inside `📁` **folder group nodes** (React Flow
  `parentId` nesting), folders nest in parent folders, and external **modules** / **endpoints** sit
  in columns to the right.
- **Node types** (`nodes.tsx`, stable module-level `nodeTypes`): `synapseFolder` (container),
  `synapseFile` (extension-accented card), `synapseDatabase` (`.sql/.prisma`), `synapseFunction`
  (`ƒ` node with expandable code), `synapseModule` (dashed dependency pill), `synapseEndpoint`
  (HTTP method tag), `synapseGap` (pulsing speculative blueprint box).
- **Function nodes**: a chat answer's `functions` (or clicking a file → `GET /api/file/functions`)
  attach `ƒ` nodes **as children of the file node** (anchored beside it), expandable to reveal source.
  These are synced into React Flow's node *state* (not just the display array) so they render
  reliably.
- **Highlighting & camera**: query `highlighted_files` illuminate the file **and its ancestor
  folders**; GSAP tweens `{x,y,zoom}` via `reactFlow.setViewport` to frame the files + their
  function nodes. Dim/glow is CSS-class driven (`rf-dim`/`rf-active`/`bp-*` in `globals.css`) for
  GPU-friendly transitions. Blueprint mode recolors nodes green/yellow and injects pulsing gap nodes.
- **Panels**: `ChatPanel` (hybrid-RAG chat + `FunctionList` expandable code), `BlueprintPanel`
  (feature pitch → reuse matrix + diff), `ExecutionPanel` (execution-trace terminal).

---

## 15. Configuration

All backend config is **environment-driven** (`internal/config`). The Go server has **no built-in
`.env` loader** — `backend/run.ps1` reads `backend/.env` into the process environment and runs
`go run ./cmd/server` (so editing `.env` is reflected on the next run).

| Variable | Default | Purpose |
|---|---|---|
| `SYNAPSE_DATABASE_URL` | `postgres://synapse:synapse@localhost:5432/synapse?sslmode=disable` | pgx DSN |
| `SYNAPSE_INGEST_ROOT` | `` | local dir to ingest at startup (empty = serve only) |
| `SYNAPSE_WORKERS` | `4` | ingestion worker-pool size |
| `SYNAPSE_ENRICH` | `true` | run chunk+embed during ingestion |
| `SYNAPSE_PARSER_DIR` / `SYNAPSE_NODE_BIN` | `tools/tsparser` / `node` | TS parser location + node binary |
| `SYNAPSE_HTTP_ADDR` / `SYNAPSE_SERVE` | `:8080` / `true` | API listen addr; serve after ingest |
| `SYNAPSE_EMBED_PROVIDER` / `SYNAPSE_EMBED_MODEL` / `SYNAPSE_EMBED_DIM` | `ollama` / `mxbai-embed-large` / `1024` | embeddings (current local config) |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama endpoint (embeddings + ollama LLM) |
| `SYNAPSE_LLM_PROVIDER` / `SYNAPSE_LLM_MODEL` | `auto` / `` | chat provider chain + optional model pin |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `OPENROUTER_API_KEY` | `` | provider keys (`auto` picks first present) |
| `OPENAI_BASE_URL` / `OPENROUTER_BASE_URL` | `` | OpenAI-compatible base-URL overrides |
| `SYNAPSE_RAG_TOPK` | `5` | top-K chunks for query + blueprint |
| `SYNAPSE_BLUEPRINT_CONCURRENCY` | `6` | blueprint scoring concurrency (≤ DB pool of 8) |
| `SYNAPSE_CLONES_DIR` / `SYNAPSE_GIT_BIN` | `.synapse-clones` / `git` | clone-on-demand workspace + git binary |

Frontend: `NEXT_PUBLIC_SYNAPSE_API` (default `http://localhost:8080`).
---

## 16. Running locally

```powershell
# 1. Database (Docker Desktop must be running first)
docker compose -f docker/docker-compose.yml up -d

# 2. Local models (Ollama)
#    ollama pull mxbai-embed-large      # embeddings (1024-d)
#    ollama pull qwen2.5-coder:3b        # chat (or set OPENROUTER_API_KEY in .env)

# 3. Backend — loads .env, ingests (if SYNAPSE_INGEST_ROOT set), serves :8080
cd backend ; .\run.ps1

# 4. Frontend — http://localhost:3000 (landing page)
npm --prefix frontend run dev          # dev script uses --webpack (see gotchas)
```

Verification gates: `go build ./... && go vet ./...` (backend); `npx tsc --noEmit` and
`npm --prefix frontend run dev` (frontend).

---

## 17. Operational notes & gotchas (Windows / local-CPU)

- **Frontend dev uses `--webpack`**, not the default Turbopack: Turbopack's PostCSS/Tailwind worker
  leaked Node child processes on this Windows machine and OOM-fork-bombed `next dev`. Webpack runs
  PostCSS in-process.
- **`run.ps1` re-applies `.env` every run** (process env vars from a prior run would otherwise
  shadow edits). The Go server reads real env vars; `.env` is bridged by `run.ps1`.
- **CPU-only LLM** (no GPU): the chat client's 5-minute timeout absorbs slow inference. Prefer a
  small chat model (`qwen2.5-coder:3b`) or a fast API (OpenRouter `gemini-2.5-flash`) for usable latency.
- **Changing the embedder** is a full re-index: edit `SYNAPSE_EMBED_MODEL` + `SYNAPSE_EMBED_DIM` +
  the schema `VECTOR(n)`, `docker compose … down -v && up -d`, then re-ingest every repo.
- **Generation caches** (docs/architecture) are in-memory per server run; re-ingesting a repo won't
  refresh its docs until a restart or an `&refresh=true` call.
