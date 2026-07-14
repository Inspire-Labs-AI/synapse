# Project Synapse — developer tasks. Run `make help` for the list.
# On Windows without `make` installed, use the equivalent PowerShell script:
#   .\setup.ps1
#
# (Recipes are TAB-indented, as Make requires.)

.DEFAULT_GOAL := help
.PHONY: help setup setup-parser setup-frontend setup-backend db-up db-down build test tsc

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

setup: setup-parser setup-frontend setup-backend ## Install ALL dependencies (run this first)
	@echo ""
	@echo "Setup complete. Next: 'make db-up', then run the backend + 'cd frontend && npm run dev'."

setup-parser: ## Install the TypeScript parser subprocess deps (backend/tools/tsparser)
	@echo "-> TypeScript parser deps..."
	cd backend/tools/tsparser && npm install

setup-frontend: ## Install the Next.js frontend deps
	@echo "-> Frontend deps..."
	cd frontend && npm install

setup-backend: ## Download Go module dependencies
	@echo "-> Go modules..."
	cd backend && go mod download

db-up: ## Start Postgres + pgvector (Docker)
	docker compose -f docker/docker-compose.yml up -d

db-down: ## Stop the database container
	docker compose -f docker/docker-compose.yml down

build: ## Build backend + frontend
	cd backend && go build ./...
	cd frontend && npm run build

test: ## Run backend tests + frontend typecheck
	cd backend && go test ./...
	cd frontend && npx tsc --noEmit

tsc: ## Frontend typecheck only
	cd frontend && npx tsc --noEmit
