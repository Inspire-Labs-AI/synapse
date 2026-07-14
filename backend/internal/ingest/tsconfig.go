package ingest

import (
	"io/fs"
	"os"
	"path/filepath"

	"project-synapse/backend/internal/parser"
)

// loadTSConfigResolution finds tsconfig.json / jsconfig.json files under root and
// extracts their import-resolution settings: path-alias rules (e.g. `@/* -> ./*`)
// AND baseUrl roots (e.g. `src/common/x` under baseUrl "."). Both let TS/JS
// imports resolve to real files during ingestion instead of looking external —
// the baseUrl case is the NestJS `import { X } from 'src/...'` convention.
func loadTSConfigResolution(absRoot string) (aliases []parser.AliasRule, baseDirs []string) {
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".next", "dist", "build", ".synapse-clones", "vendor", "target":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "tsconfig.json" && d.Name() != "jsconfig.json" {
			return nil
		}
		content, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(absRoot, filepath.Dir(p))
		if rerr != nil {
			return nil
		}
		dir := filepath.ToSlash(rel)
		if dir == "." {
			dir = ""
		}
		cfg := parser.ParseTSConfig(dir, content)
		aliases = append(aliases, cfg.Aliases...)
		if cfg.HasBaseDir {
			baseDirs = append(baseDirs, cfg.BaseDir)
		}
		return nil
	})
	return aliases, baseDirs
}
