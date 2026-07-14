package llm

import "testing"

func TestCleanMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		// Over-escaped newlines (the reported bug) become real line breaks.
		{`Intro text.\n\n## Capabilities\n- one\n- two`, "Intro text.\n\n## Capabilities\n- one\n- two"},
		{`a\r\nb`, "a\nb"},
		{`col1\tcol2`, "col1\tcol2"},
		// Correctly-parsed content (already real newlines) is untouched.
		{"Intro.\n\n## Heading\n- item", "Intro.\n\n## Heading\n- item"},
		// No backslashes at all → fast path, unchanged.
		{"plain text", "plain text"},
	}
	for i, c := range cases {
		if got := CleanMarkdown(c.in); got != c.want {
			t.Errorf("case %d: CleanMarkdown(%q) = %q, want %q", i, c.in, got, c.want)
		}
	}
}
