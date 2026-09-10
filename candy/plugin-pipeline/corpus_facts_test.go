package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCorpusFacts: the corpus-surface facts of the bed's latest run — the
// omarchy-corpus steps (the corpus charly.yml single source is the identity
// source) counted separately, never conflated with the oracle-authored
// assertions.
func TestCorpusFacts(t *testing.T) {
	dir := t.TempDir()
	bed := "check-omarchy-pr-42-vm"
	base := filepath.Join(dir, ".check", bed, "2026.253.1")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	log := "  PASS  check the ocr reads the prompt  [ocr-screen-text]  ok\n" +
		"  PASS  check the PR assertion  [behavior-1]  exit=0\n" +
		"  SKIP  check a skipped corpus step  [battery-model-present]  skipped\n" +
		"  FAIL  check a failing corpus step  [bluetooth-panel-model]  exit=1\n"
	if err := os.WriteFile(filepath.Join(base, "check-live.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	corpus := filepath.Join(dir, "corpus.yml")
	content := "plan:\n  - check: x\n    id: ocr-screen-text\n  - check: y\n    id: battery-model-present\n  - check: z\n    id: bluetooth-panel-model\n"
	if err := os.WriteFile(corpus, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := &runCtx{workdir: dir}
	run, okCount, skipped := corpusFacts(rc, bed, corpus)
	if run != 3 {
		t.Fatalf("run = %d, want 3 (the corpus steps only — behavior-1 is the oracle's, not a corpus step)", run)
	}
	if okCount != 1 {
		t.Fatalf("okCount = %d, want 1", okCount)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	// No corpus file → zero facts (never a crash).
	run, okCount, skipped = corpusFacts(rc, bed, "")
	if run != 0 || okCount != 0 || skipped != 0 {
		t.Fatalf("no-corpus facts = (%d,%d,%d), want zeros", run, okCount, skipped)
	}
	// A missing corpus file → zero facts.
	run, okCount, skipped = corpusFacts(rc, bed, filepath.Join(dir, "missing.yml"))
	if run != 0 || okCount != 0 || skipped != 0 {
		t.Fatalf("missing-corpus facts = (%d,%d,%d), want zeros", run, okCount, skipped)
	}
}
