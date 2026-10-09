package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillBundleAppendsReferences pins opencharly/plugin-pipeline#36: an `agent:` stage read only
// SKILL.md, so references/*.md — the progressive-disclosure split the skills contract prescribes — were
// unreachable from a pipeline stage. The bundle must carry them, in sorted order, and a skill with no
// references must be unchanged.
func TestSkillBundleAppendsReferences(t *testing.T) {
	corpus := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(corpus, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("with-refs/SKILL.md", "ENTRY")
	write("with-refs/references/b-second.md", "SECOND")
	write("with-refs/references/a-first.md", "FIRST")
	write("with-refs/references/notes.txt", "IGNORED") // only *.md is part of the split
	write("bare/SKILL.md", "ONLY")

	got, err := skillBundle(corpus, "with-refs")
	if err != nil {
		t.Fatalf("skillBundle: %v", err)
	}
	for _, want := range []string{"--- with-refs ---\nENTRY", "a-first.md ---\nFIRST", "b-second.md ---\nSECOND"} {
		if !strings.Contains(got, want) {
			t.Errorf("bundle is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "IGNORED") {
		t.Errorf("a non-.md sibling must not be pulled in:\n%s", got)
	}
	if strings.Index(got, "a-first") > strings.Index(got, "b-second") {
		t.Errorf("references must be appended in sorted order, so one skill yields one prompt:\n%s", got)
	}

	bare, err := skillBundle(corpus, "bare")
	if err != nil {
		t.Fatalf("skillBundle(bare): %v", err)
	}
	if bare != "\n\n--- bare ---\nONLY" {
		t.Errorf("a skill with no references must be unchanged, got %q", bare)
	}

	if _, err := skillBundle(corpus, "absent"); err == nil {
		t.Error("a missing skill must still fail the stage — that behaviour is load-bearing and must not regress")
	}
}
