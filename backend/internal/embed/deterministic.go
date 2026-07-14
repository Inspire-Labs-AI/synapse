package embed

import (
	"context"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
)

// camelBoundary splits camelCase / PascalCase identifiers ("fetchCategories"
// -> "fetch Categories") so the tokenizer aligns code identifiers with the
// natural-language words in a question.
var camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// nonAlnum splits on any run of non-alphanumeric characters (covers paths like
// "/category", dots, slashes, punctuation).
var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// DeterministicEmbedder is a feature-hashing (hashing-trick) bag-of-tokens
// embedder. It is fully local and deterministic: identical text always yields
// identical vectors, and cosine similarity reflects token overlap. Not a
// learned semantic model — it is the offline stand-in that lets the pipeline be
// exercised end-to-end without an external embedding API.
type DeterministicEmbedder struct {
	dim int
}

// NewDeterministic returns a deterministic embedder of the given dimension.
func NewDeterministic(dim int) *DeterministicEmbedder {
	if dim <= 0 {
		dim = DefaultDim
	}
	return &DeterministicEmbedder{dim: dim}
}

func (e *DeterministicEmbedder) Dimensions() int { return e.dim }
func (e *DeterministicEmbedder) Name() string    { return "deterministic" }

// Embed vectorizes each text independently.
func (e *DeterministicEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = e.vectorize(t)
	}
	return out, nil
}

func (e *DeterministicEmbedder) vectorize(text string) []float32 {
	vec := make([]float32, e.dim)
	for _, tok := range tokenize(text) {
		idx, sign := bucket(tok, e.dim)
		vec[idx] += sign
	}
	l2Normalize(vec)
	return vec
}

// tokenize lowercases, splits camelCase, then splits on non-alphanumeric runs,
// keeping tokens of length >= 2.
func tokenize(text string) []string {
	spaced := camelBoundary.ReplaceAllString(text, "$1 $2")
	raw := nonAlnum.Split(strings.ToLower(spaced), -1)
	tokens := make([]string, 0, len(raw))
	for _, t := range raw {
		if len(t) >= 2 {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// bucket maps a token to an index and a +/-1 sign (signed feature hashing,
// which reduces the bias from hash collisions).
func bucket(token string, dim int) (int, float32) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	sum := h.Sum32()
	idx := int(sum % uint32(dim))
	sign := float32(1)
	if sum&1 == 1 {
		sign = -1
	}
	return idx, sign
}

// l2Normalize scales the vector to unit length in place (no-op for zero vectors).
func l2Normalize(v []float32) {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
}
