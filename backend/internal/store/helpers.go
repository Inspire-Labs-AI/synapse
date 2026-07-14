package store

import (
	"encoding/json"
	"path"
	"strings"
)

// jsonObj marshals a map into a JSON byte slice for a jsonb column. pgx's JSONB
// codec writes a []byte through verbatim, so this is the safe, explicit form.
func jsonObj(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// lineSlice returns the 1-based line `n` of content, trimmed. Used to give each
// vector_chunk a small, human-readable code excerpt for the symbol it
// represents. Returns "" if the line is out of range.
func lineSlice(content string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if n > len(lines) {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(lines[n-1], "\r"))
}

// deriveRoutePath produces the HTTP path for an endpoint. Router-style endpoints
// already carry their literal path ("/users"); Next.js App Router handlers do
// not, so we derive the route from the file location:
//
//	app/api/users/route.ts      -> /api/users
//	app/dashboard/page.tsx      -> /dashboard
//	pages/api/health.ts         -> /health   (segment after the api dir)
func deriveRoutePath(relPath, explicitPath string) string {
	if explicitPath != "" {
		return explicitPath
	}

	p := strings.ReplaceAll(relPath, "\\", "/")
	dir := path.Dir(p)
	segs := strings.Split(dir, "/")

	// Find the last "app" or "pages" boundary segment.
	start := -1
	for i, s := range segs {
		if s == "app" || s == "pages" {
			start = i
		}
	}
	if start == -1 {
		return "/" + path.Base(dir)
	}

	rest := segs[start+1:]
	if len(rest) == 0 {
		return "/"
	}
	return "/" + strings.Join(rest, "/")
}
