package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateValidateContract drives the GENERIC validate contract (B12: both
// branches would fail without the implementation - the pre-fix engine ignored
// the validate field entirely).
func TestGenerateValidateContract(t *testing.T) {
	workdir := t.TempDir()
	rc := &runCtx{workdir: workdir, env: map[string]string{}}
	raw := map[string]any{
		"template": "artifact: rendered",
		"out":      "out/artifact.txt",
	}

	// 1. a PASSING validator: the stage succeeds and the artifact lands
	raw["validate"] = "test -s out/artifact.txt"
	if err := rc.runGenerate(raw); err != nil {
		t.Fatalf("passing validator: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "out", "artifact.txt")); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}

	// 2. a FAILING validator: the stage FAILS (the rendered artifact never
	// ships unvalidated - the RCA 2026.252 control)
	raw["validate"] = "exit 1"
	if err := rc.runGenerate(raw); err == nil {
		t.Fatal("failing validator: the stage must FAIL")
	}

	// 3. NO validate field: the stage succeeds (back-compat)
	delete(raw, "validate")
	if err := rc.runGenerate(raw); err != nil {
		t.Fatalf("no validator: %v", err)
	}
}
