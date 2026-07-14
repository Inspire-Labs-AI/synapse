package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWalkSkipsMacOSAndDeps(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("App.tsx")                   // real source — keep
	write("src/Real.go")               // real source — keep
	write("._App.tsx")                 // AppleDouble sidecar — skip
	write("src/._Real.go")             // AppleDouble sidecar — skip
	write("__MACOSX/._App.tsx")        // macOS metadata dir — skip
	write("__MACOSX/nested/foo.tsx")   // macOS metadata dir — skip
	write("node_modules/pkg/index.ts") // dependency — skip
	write("assets/logo.png")           // unsupported ext — skip

	out := make(chan string, 128)
	go func() { _ = Walk(root, out) }()
	got := map[string]bool{}
	for rel := range out {
		got[filepath.ToSlash(rel)] = true
	}

	for _, keep := range []string{"App.tsx", "src/Real.go"} {
		if !got[keep] {
			t.Errorf("expected %s to be ingested", keep)
		}
	}
	for _, skip := range []string{
		"._App.tsx", "src/._Real.go", "__MACOSX/._App.tsx",
		"__MACOSX/nested/foo.tsx", "node_modules/pkg/index.ts", "assets/logo.png",
	} {
		if got[skip] {
			t.Errorf("expected %s to be skipped", skip)
		}
	}
}
