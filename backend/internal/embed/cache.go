package embed

import (
	"context"
	"sync"
)

// CachingEmbedder wraps an Embedder with an in-memory cache keyed by input
// text. It collapses repeated embedding work — the same query term embedded
// across many blueprint searches, or a repeated chat question — into a single
// upstream call, keeping deep lookups sub-second. Safe for concurrent use.
type CachingEmbedder struct {
	inner    Embedder
	maxItems int

	mu    sync.RWMutex
	cache map[string][]float32
}

// NewCache wraps inner with a bounded cache (defaults to 8192 entries).
func NewCache(inner Embedder, maxItems int) *CachingEmbedder {
	if maxItems <= 0 {
		maxItems = 8192
	}
	return &CachingEmbedder{
		inner:    inner,
		maxItems: maxItems,
		cache:    make(map[string][]float32),
	}
}

func (c *CachingEmbedder) Dimensions() int { return c.inner.Dimensions() }
func (c *CachingEmbedder) Name() string    { return "cached(" + c.inner.Name() + ")" }

// Embed returns cached vectors where available and embeds the rest in one
// batched call to the inner embedder.
func (c *CachingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missIdx []int
	var missText []string

	c.mu.RLock()
	for i, t := range texts {
		if v, ok := c.cache[t]; ok {
			out[i] = v
		} else {
			missIdx = append(missIdx, i)
			missText = append(missText, t)
		}
	}
	c.mu.RUnlock()

	if len(missText) == 0 {
		return out, nil
	}

	fresh, err := c.inner.Embed(ctx, missText)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if len(c.cache) > c.maxItems {
		c.cache = make(map[string][]float32) // crude eviction: reset when full
	}
	for j, idx := range missIdx {
		out[idx] = fresh[j]
		c.cache[missText[j]] = fresh[j]
	}
	c.mu.Unlock()

	return out, nil
}
