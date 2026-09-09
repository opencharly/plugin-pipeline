package pluginpipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// TestSkipWhen_RecordsSkipped is the B12 coverage for the skip_when condition:
// a stage declaring skip_when evaluates the condition and records skipped
// instead of failing (the media gate is only meaningful on a passing run).
func TestSkipWhen_RecordsSkipped(t *testing.T) {
	rc := &runCtx{pr: "1", calver: "2026.1.1", env: map[string]string{}}
	l := newLedger()
	curLedger = l
	// the eval stage already ran: its verdict is in the ledger
	l.put(&StageResult{ID: "eval", Kind: "eval", Status: "ok", Outputs: map[string]any{"verdict": "FAIL"}})
	res, err := rc.runStage(nil, "media", "media-gate", map[string]any{
		"kind": "media", "id": "media-gate", "skip_when": "@eval.verdict == \"FAIL\"",
	}, l)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "skipped" {
		t.Errorf("status = %q, want skipped (the skip_when condition matched the FAIL verdict)", res.Status)
	}
}

// TestDryRun_ResolvesEnvRefs is the B12 coverage for the --dry-run path: the
// entity validates + every declared env ref resolves (the plan's env contract).
func TestDryRun_ResolvesEnvRefs(t *testing.T) {
	p := params.PipelineInput{
		Stages: []params.Stage{
			{"kind": "probe", "id": "p1", "verbs": []any{"config_audit"}, "input": map[string]any{"env": "$env.PR_NUMBER"}},
		},
	}
	t.Setenv("PR_NUMBER", "42")
	refs := envRefs(p)
	if len(refs) == 0 {
		t.Fatal("no env refs found in the plan")
	}
	for _, ref := range refs {
		if os.Getenv(ref) == "" {
			t.Errorf("env ref %s unresolved (dry-run env contract)", ref)
		}
	}
}

// TestFixtureMode_RunsOffline is the B12 coverage for the offline fixture mode:
// a probe stage with fixture: true runs the probe offline (no live venue).
func TestFixtureMode_RunsOffline(t *testing.T) {
	p := params.PipelineInput{
		Stages: []params.Stage{
			{"kind": "probe", "id": "p1", "verbs": []any{"config_audit"}, "input": map[string]any{"fixture": true, "bed": "beds/x.yml"}},
		},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "beds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beds", "x.yml"), []byte("plan: []"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPlan(context.Background(), p, "1", "2026.1.1", dir, nil); err != nil {
		t.Fatal(err)
	}
	res, ok := ledgerRef("p1")
	if !ok {
		t.Fatal("p1 stage missing from the ledger")
	}
	if res.Status == "" {
		t.Error("p1 has no status — the fixture probe did not run")
	}
}

// TestBlockScalar_RendersTheProse is the B12 coverage for the agent-check block
// scalar: the render emits the prose as a folded block scalar.
func TestBlockScalar_RendersTheProse(t *testing.T) {
	tmpl := `steps:
              - agent-check: >-`
	out := renderCheckBlock([]any{map[string]any{"what": "the guest must report its hostname"}}, "agent-check", tmpl)
	if !strings.Contains(out, "agent-check: >-") {
		t.Errorf("render = %q, want the agent-check block scalar", out)
	}
	if !strings.Contains(out, "the guest must report its hostname") {
		t.Errorf("render = %q, want the prose", out)
	}
}
