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
//
// The fixture is the REAL shape and a NON-`pr-beds` layout: a name-first entity
// (`<bed>: { vm: { plan: [...] } }`) under `eval/pr-42/`. The old fixture was a
// flat top-level `plan:` under `pr-beds/`, which the hardcoded reader matched but
// a real bed never does — so the drive read ZERO ops and reported a vacuous PASS.
func TestBedPlanOps_ExtractsTheRenderedPlan(t *testing.T) {
	dir := t.TempDir()
	bed := filepath.Join(dir, "eval", "pr-42", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `version: 2026.249.2125
check-omarchy-pr-42-vm:
  vm:
    from: check-omarchy-eval-edge-inst
    disposable: true
    plan:
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
	ops, err := bedPlanOps(dir, "42", "check-omarchy-pr-42-vm")
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

// TestBedPlanOps_MissingEntityFails: a bed name with no matching entity is a
// hard error, never a silent zero-op (vacuous) PASS.
func TestBedPlanOps_MissingEntityFails(t *testing.T) {
	dir := t.TempDir()
	bed := filepath.Join(dir, "eval", "pr-9", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bed, []byte("some-other-bed:\n  vm:\n    plan:\n      - check: x\n        command: 'true'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bedPlanOps(dir, "9", "check-omarchy-pr-9-vm"); err == nil {
		t.Fatal("a missing bed entity must be an error, not a zero-op PASS")
	}
}

// TestRunAdeBedKit_NilExecutorMapsToFail is the B12 coverage for the verdict
// mapping: a nil host executor (the un-compiled placement's guard) yields
// handled fail results, so the drive reports FAIL rather than crashing.
func TestRunAdeBedKit_NilExecutorMapsToFail(t *testing.T) {
	dir := t.TempDir()
	bed := filepath.Join(dir, "eval", "pr-7", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `check-omarchy-pr-7-vm:
  vm:
    plan:
      - check: a step
        id: s1
        command: "true"
`
	if err := os.WriteFile(bed, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	verdict, _, code, err := runAdeBedKit(context.Background(), "7", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "FAIL" || code != 2 {
		t.Errorf("verdict = %s code = %d, want FAIL/2 (the nil executor yields handled fail results)", verdict, code)
	}
}
