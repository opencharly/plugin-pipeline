package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProbeLedgerGate_Aliases: the entity's input keys are the contract — the
// ledger-gate honors bed/bed_name and control_bed/control_bed_name, and the
// entity names are rooted at the run workdir (the probe stage's bed/dir
// rooting keys on "bed"/"dir" and misses them).
func TestProbeLedgerGate_Aliases(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, ".check", "check-omarchy-pr-42-vm", "2026.253.1")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "check-live.log"), []byte("  PASS  check x  [behavior-1]  ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cbase := filepath.Join(dir, ".check", "check-omarchy-pr-42-control", "2026.253.1")
	if err := os.MkdirAll(cbase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cbase, "check-live.log"), []byte("  PASS  check y  [negated-1]  ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the control verdict rides summary.yml (the check-run's top-level steps)
	if err := os.WriteFile(filepath.Join(cbase, "summary.yml"), []byte("steps:\n  - ok: true\n  - ok: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := &runCtx{workdir: dir}
	// the ENTITY-NAME spelling (the charly.yml's actual keys) — rooted at the workdir
	ok, msg, val := probeLedgerGate(map[string]any{
		"bed_name":         "check-omarchy-pr-42-vm",
		"control_bed_name": "check-omarchy-pr-42-control",
		"media_dir":        "",
	}, rc)
	if !ok {
		t.Fatalf("probeLedgerGate(bed_name aliases) = %v", msg)
	}
	if v, m := val.(map[string]any); m {
		if n, _ := v["executed_checks"].(int); n != 1 {
			t.Fatalf("executed_checks = %v, want 1", v["executed_checks"])
		}
	}
	// NO keys at all → the failure names the input keys.
	if ok, msg, _ := probeLedgerGate(map[string]any{}, rc); ok {
		t.Fatal("probeLedgerGate(no keys) = ok, want a failure")
	} else if msg == "" {
		t.Fatal("the failure carries no message")
	}
}
