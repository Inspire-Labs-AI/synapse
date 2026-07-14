package prune

import "testing"

// pytest discovers tests by filename; nothing imports them, so "0 incoming
// imports" is expected. Missing this reported 43 live test files as dead code.
func TestIsTestRecognisesPythonAndFriends(t *testing.T) {
	tests := []string{
		"be/tests/test_org_model.py",
		"be/src/services/analytics_test.py",
		"be/tests/conftest.py",
		"be/tests/helpers.py", // under /tests/
		"internal/prune/prune_test.go",
		"src/app.test.ts",
		"src/app.spec.tsx",
		"src/__tests__/foo.ts",
	}
	for _, f := range tests {
		if !isTest(f) {
			t.Errorf("%s should be recognised as a test file", f)
		}
	}
	notTests := []string{
		"be/src/controllers/service.py",
		"be/src/latest.py", // "test" nowhere near it
		"internal/prune/prune.go",
		"src/contest.ts", // must not match the `test_` prefix loosely
	}
	for _, f := range notTests {
		if isTest(f) {
			t.Errorf("%s must NOT be treated as a test file", f)
		}
	}
}

// Console-script and manually-run entry points have no importer by design.
func TestIsEntryCoversCliAndScripts(t *testing.T) {
	entries := []struct{ file, lang string }{
		{"be/src/cli.py", "python"}, // pyproject [project.scripts] target
		{"be/src/__main__.py", "python"},
		{"be/src/wsgi.py", "python"},
		{"scripts/backfill_categories.py", "python"}, // run by hand
		{"be/scripts/cleanup.py", "python"},
		{"bin/tool.go", "go"},
		{"cmd/server/main.go", "go"},
	}
	for _, e := range entries {
		if !isEntry(e.file, e.lang, false) {
			t.Errorf("%s (%s) should be an entry point", e.file, e.lang)
		}
	}
	if isEntry("be/src/services/analytics/reconciliation.py", "python", false) {
		t.Errorf("an ordinary module must not be an entry point")
	}
}
