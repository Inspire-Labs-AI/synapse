package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a repo that contains agent git worktrees (.claude/worktrees/agent-*/)
// or a submodule must NOT have those duplicate checkouts ingested. Before this
// guard, telegram_agent ingested 1425 files of which 1168 (82%) were four full
// duplicate copies of the same 257-file tree.
func TestWalkSkipsNestedGitCheckouts(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The real source tree.
	write("be/src/main.py", "def main():\n    return 1\n")
	write("be/src/util.py", "def helper():\n    return 2\n")

	// A Claude Code agent worktree: a full duplicate copy, with a `.git` gitfile.
	write(".claude/worktrees/agent-abc/be/src/main.py", "def main():\n    return 1\n")
	write(".claude/worktrees/agent-abc/.git", "gitdir: /somewhere/.git/worktrees/agent-abc\n")

	// A submodule-style nested checkout outside .claude (real `.git` directory).
	write("vendored/libx/lib.py", "def lib():\n    return 3\n")
	if err := os.MkdirAll(filepath.Join(root, "vendored", "libx", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := make(chan string, 64)
	go func() {
		if err := Walk(root, out); err != nil {
			t.Errorf("walk: %v", err)
		}
	}()
	var got []string
	for p := range out {
		got = append(got, filepath.ToSlash(p))
	}

	for _, want := range []string{"be/src/main.py", "be/src/util.py"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("real source %q was not ingested (got %v)", want, got)
		}
	}
	for _, g := range got {
		if strings.HasPrefix(g, ".claude/") {
			t.Errorf("agent worktree copy must be skipped, got %q", g)
		}
		if strings.HasPrefix(g, "vendored/libx/") {
			t.Errorf("nested git checkout must be skipped, got %q", g)
		}
	}
	if len(got) != 2 {
		t.Errorf("expected exactly the 2 real files, got %d: %v", len(got), got)
	}
}
