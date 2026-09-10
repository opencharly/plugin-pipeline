package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProbeCorpusAudit: the corpus gate — every oracle-selected corpus step id
// MUST exist as a step id: in the omarchy-corpus charly.yml (the CUE-validated
// single source; there is NO side manifest). A selected id the plan does not
// carry is the oracle's gap. An empty selection is legal.
func TestProbeCorpusAudit(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus-charly.yml")
	content := "omarchy-corpus:\n    candy:\n        plan:\n            - check: x\n              id: cli-help-renders\n            - check: y\n              id: shell-config-valid-json\n"
	if err := os.WriteFile(corpus, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Every selected id present → ok.
	if ok, msg := probeCorpusAudit(map[string]any{"corpus": corpus, "ids": []string{"cli-help-renders", "shell-config-valid-json"}}); !ok {
		t.Fatalf("probeCorpusAudit(all present) = %v", msg)
	}
	// An empty selection → ok (legal).
	if ok, _ := probeCorpusAudit(map[string]any{"corpus": corpus, "ids": []string{}}); !ok {
		t.Fatal("probeCorpusAudit(empty) failed, want ok")
	}
	// A selected id the plan does not carry → the oracle's gap.
	if ok, msg := probeCorpusAudit(map[string]any{"corpus": corpus, "ids": []string{"no-such-step"}}); ok {
		t.Fatal("probeCorpusAudit(missing id) = ok, want the gap failure")
	} else if !filepath.IsAbs(msg[:1]) && msg == "" {
		t.Fatalf("the gap failure carries no message")
	}
	// An unreadable corpus file → fail.
	if ok, _ := probeCorpusAudit(map[string]any{"corpus": filepath.Join(dir, "missing.yml"), "ids": []string{"x"}}); ok {
		t.Fatal("probeCorpusAudit(unreadable) = ok, want a failure")
	}
}
