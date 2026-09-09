package pluginpipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestBedPlanOps_ExtractsTheRenderedPlan is the B12 coverage for the kit drive's
// plan extraction: a rendered bed's check steps map onto spec.Op (the
// command/stdout/eventually/retry_interval/context/id + the assert intent).
func TestBedPlanOps_ExtractsTheRenderedPlan(t *testing.T) {
	dir := t.TempDir()
	bed := filepath.Join(dir, "pr-beds", "pr-42", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `plan:
  - check: the guest reports its hostname
    id: ocb-hostname
    command: "uname -n"
    stdout:
      matches: ".+"
    eventually: 300s
    retry_interval: 10s
    context: [runtime]
`
	if err := os.WriteFile(bed, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ops, err := bedPlanOps(dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %d, want 1", len(ops))
	}
	op := ops[0]
	if op.Command != "uname -n" {
		t.Errorf("command = %q, want uname -n", op.Command)
	}
	if op.Description != "the guest reports its hostname" {
		t.Errorf("description = %q", op.Description)
	}
	if op.ID != "ocb-hostname" {
		t.Errorf("id = %q", op.ID)
	}
	if len(op.Stdout) != 1 || op.Stdout[0].Op != "matches" {
		t.Errorf("stdout = %+v, want a matches matcher", op.Stdout)
	}
	if op.IntentDo != string(spec.DoAssert) {
		t.Errorf("intent = %q, want assert", op.IntentDo)
	}
}

// TestRunAdeBedKit_NilExecutorMapsToFail is the B12 coverage for the verdict
// mapping: a nil host executor (the un-compiled placement's guard) yields
// handled fail results, so the drive reports FAIL rather than crashing.
func TestRunAdeBedKit_NilExecutorMapsToFail(t *testing.T) {
	dir := t.TempDir()
	bed := filepath.Join(dir, "pr-beds", "pr-7", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `plan:
  - check: a step
    id: s1
    command: "true"
`
	if err := os.WriteFile(bed, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	verdict, _, code, err := runAdeBedKit(context.Background(), "7", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "FAIL" || code != 2 {
		t.Errorf("verdict = %s code = %d, want FAIL/2 (the nil executor yields handled fail results)", verdict, code)
	}
}
