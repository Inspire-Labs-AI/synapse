# Contributing to Project Synapse

Thanks for your interest in improving Synapse! This project is a local-first,
neuroscience-inspired codebase intelligence engine, and contributions of all
kinds are welcome: bug fixes, features, docs, parsers, and ideas.

## Ways to contribute

- **Report a bug** — open an issue with steps to reproduce (see the bug template).
- **Request a feature** — open an issue describing the problem you want solved.
- **Send a PR** — for anything non-trivial, please open an issue first so we can
  agree on the approach before you invest time.
- **Improve docs** — READMEs, comments, and architecture notes are all fair game.

## Development setup

**Prerequisites:** Docker, Go (version in [`backend/go.mod`](backend/go.mod)), Node 20+.

```bash
# 1. Install everything (Go modules, frontend deps, TS parser subprocess deps)
make setup          # or, on Windows without make:  .\setup.ps1

# 2. Start Postgres + pgvector
make db-up          # or: docker compose -f docker/docker-compose.yml up -d

# 3. Configure (optional — the app runs fully offline with no keys)
cp backend/.env.example  backend/.env
cp frontend/.env.example frontend/.env.local

# 4. Run the backend  →  http://localhost:8080
cd backend && ./run.ps1        # or export the vars and: go run ./cmd/server

# 5. Run the frontend →  http://localhost:3000
cd frontend && npm run dev
```

Open http://localhost:3000, click **Continue as Local Developer**, and point it
at a local repository to build its graph.

## Before you open a PR

Please make sure the checks pass locally — CI runs the same ones:

```bash
make build          # backend `go build ./...` + frontend `npm run build`
make test           # backend `go test ./...` + frontend `npx tsc --noEmit`
```

- **Go:** clean, idiomatic Go; strict error handling; keep goroutine worker
  pools isolated; use the `pgx` pool for DB access. Run `go build ./...` and
  `go test ./...`.
- **Frontend:** strict TypeScript (`npx tsc --noEmit` must pass), functional
  components, `useMemo`/`useCallback` where it matters, Tailwind for styling.
- **Scope:** the parser stays within its four languages (TypeScript/JavaScript,
  Go, Rust). Please don't pull in heavy multi-language frameworks (e.g. a
  bundled tree-sitter toolchain) without discussing it first.
- **Secrets:** never commit real API keys. Only `.env.example` templates are
  tracked; real `.env` / `.env.local` files are gitignored.

## Pull request guidelines

- Keep PRs focused; one logical change per PR is easiest to review.
- Write a clear description: what changed, why, and how you verified it.
- Add or update tests for behavior changes where practical.
- Match the surrounding code's style, naming, and comment density.
- Update docs (README / comments) when you change user-facing behavior.

## Architecture (where things live)

```
backend/internal/parser/     MultiParser: tsc · go/parser · rust · import resolve
backend/internal/ingest/     worker pool · enrichment · embeddings
backend/internal/store/      pgx: code_files · ast_relationships · vector_chunks
backend/internal/rag/        hybrid retrieval + streamed answers
backend/internal/blueprint/  feature validate / roadmap engine
backend/internal/prune/      dead-code detection + LLM verification
backend/internal/{docs,architecture,axon,bugs}/
frontend/app/workspace/      GraphView (d3 canvas) · Assistant · Blueprint · Prune
docker/                      Postgres + pgvector, schema init
```

See [`CLAUDE.md`](CLAUDE.md) for the project's conventions and feature naming.

## Code of Conduct

By participating, you agree to uphold our [Code of Conduct](CODE_OF_CONDUCT.md).

## License

By contributing, you agree that your contributions will be licensed under the
project's [MIT License](LICENSE).
