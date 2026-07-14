package config

import (
	"os"
	"strconv"
)

// Config holds the runtime configuration for the ingestion + graph + RAG
// server. Values are sourced from environment variables with local-development
// defaults that line up with the docker-compose Postgres container.
type Config struct {
	DatabaseURL string // pgx connection string
	IngestRoot  string // directory to walk + ingest ("" = skip ingestion)
	Workers     int    // ingestion worker-pool size
	ParserDir   string // directory containing the Node parse.mjs + its node_modules
	NodeBin     string // node executable
	HTTPAddr    string // listen address for the HTTP API
	Serve       bool   // start the HTTP server after ingestion

	// Embeddings (semantic layer).
	Enrich        bool   // chunk + embed during ingestion
	EmbedProvider string // auto | deterministic | jina | voyage | openai | openrouter | gemini | ollama
	EmbedModel    string
	EmbedDim      int
	JinaKey       string // JINA_API_KEY (code-specialized embeddings)
	JinaBase      string
	VoyageKey     string // VOYAGE_API_KEY (voyage-code-3 embeddings)
	VoyageBase    string
	GeminiKey     string // GEMINI_API_KEY (Google Gemini embeddings)
	GeminiBase    string // optional base-URL override
	OllamaHost    string

	// Chunk enrichment: an LLM-written one-line purpose per symbol, embedded
	// alongside the code to sharpen semantic retrieval.
	EnrichSummaries bool

	// LLM (answer synthesis).
	LLMProvider    string // auto | template | anthropic | openai | openrouter | ollama
	LLMModel       string // base model — assistant (RAG), blueprint, tours, enrichment
	DocsModel      string // optional stronger model for documentation generation
	ArchModel      string // optional stronger model for architecture generation
	BugsModel      string // optional stronger model for the Tier-2 bug analysis
	AnthropicKey   string
	OpenAIKey      string
	OpenAIBase     string
	OpenRouterKey  string
	OpenRouterBase string

	// RAG.
	RAGTopK int

	// Bug detection (Tier-2 adversarial LLM pass).
	BugsLLM    bool
	BugsMaxLLM int

	// Dead-code pruning: LLM verification of file-level candidates.
	PruneVerify bool

	// Blueprint discovery.
	BlueprintConcurrency int

	// Repository ingestion (clone-on-demand).
	ClonesDir string // base directory for shallow clones
	GitBin    string // git executable

	// Auth (OAuth is handled by the Next.js front-end via Auth.js; these are
	// surfaced here for env parity and future server-side session validation /
	// per-user workspace attribution).
	AuthSecret         string
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
}

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		DatabaseURL: getEnv("SYNAPSE_DATABASE_URL", "postgres://synapse:synapse@localhost:5432/synapse?sslmode=disable"),
		IngestRoot:  getEnv("SYNAPSE_INGEST_ROOT", ""),
		Workers:     getEnvInt("SYNAPSE_WORKERS", 4),
		ParserDir:   getEnv("SYNAPSE_PARSER_DIR", "tools/tsparser"),
		NodeBin:     getEnv("SYNAPSE_NODE_BIN", "node"),
		HTTPAddr:    getEnv("SYNAPSE_HTTP_ADDR", ":8080"),
		Serve:       getEnvBool("SYNAPSE_SERVE", true),

		Enrich:        getEnvBool("SYNAPSE_ENRICH", true),
		EmbedProvider: getEnv("SYNAPSE_EMBED_PROVIDER", "auto"),
		EmbedModel:    getEnv("SYNAPSE_EMBED_MODEL", ""),
		EmbedDim:      getEnvInt("SYNAPSE_EMBED_DIM", 1024),
		JinaKey:       getEnv("JINA_API_KEY", ""),
		JinaBase:      getEnv("JINA_BASE_URL", ""),
		VoyageKey:     getEnv("VOYAGE_API_KEY", ""),
		VoyageBase:    getEnv("VOYAGE_BASE_URL", ""),
		GeminiKey:     getEnv("GEMINI_API_KEY", ""),
		GeminiBase:    getEnv("GEMINI_BASE_URL", ""),
		OllamaHost:    getEnv("OLLAMA_HOST", "http://localhost:11434"),

		EnrichSummaries: getEnvBool("SYNAPSE_ENRICH_SUMMARIES", true),

		LLMProvider:    getEnv("SYNAPSE_LLM_PROVIDER", "auto"),
		LLMModel:       getEnv("SYNAPSE_LLM_MODEL", ""),
		DocsModel:      getEnv("SYNAPSE_DOCS_MODEL", ""),
		ArchModel:      getEnv("SYNAPSE_ARCH_MODEL", ""),
		BugsModel:      getEnv("SYNAPSE_BUGS_MODEL", ""),
		AnthropicKey:   getEnv("ANTHROPIC_API_KEY", ""),
		OpenAIKey:      getEnv("OPENAI_API_KEY", ""),
		OpenAIBase:     getEnv("OPENAI_BASE_URL", ""),
		OpenRouterKey:  getEnv("OPENROUTER_API_KEY", ""),
		OpenRouterBase: getEnv("OPENROUTER_BASE_URL", ""),

		RAGTopK: getEnvInt("SYNAPSE_RAG_TOPK", 5),

		BugsLLM:    getEnvBool("SYNAPSE_BUGS_LLM", true),
		BugsMaxLLM: getEnvInt("SYNAPSE_BUGS_MAX_LLM", 8),

		PruneVerify: getEnvBool("SYNAPSE_PRUNE_VERIFY", true),

		BlueprintConcurrency: getEnvInt("SYNAPSE_BLUEPRINT_CONCURRENCY", 6),

		ClonesDir: getEnv("SYNAPSE_CLONES_DIR", ".synapse-clones"),
		GitBin:    getEnv("SYNAPSE_GIT_BIN", "git"),

		AuthSecret:         getEnv("AUTH_SECRET", ""),
		GitHubClientID:     getEnv("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: getEnv("GITHUB_CLIENT_SECRET", ""),
		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
