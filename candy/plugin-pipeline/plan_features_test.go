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
	l := newLedger()
	rc := &runCtx{pr: "1", calver: "2026.1.1", env: map[string]string{}, ledger: l}
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
	l := newLedger()
	if err := runPlanL(context.Background(), p, "1", "2026.1.1", dir, nil, l); err != nil {
		t.Fatal(err)
	}
	res, ok := l.results["p1"]
	if !ok {
		t.Fatal("p1 stage missing from the ledger")
	}
	if res.Status == "" {
		t.Error("p1 has no status — the fixture probe did not run")
	}
}

// TestChecksRenderAsCommandSteps is the B12 coverage for the deterministic
// command-check rendering (the agent-check prose emission is GONE — hard
// cutover, R5): each check renders as check: + id: + command: and EXECUTES in
// the venue. A check without an assertion is inert and never rendered.
func TestChecksRenderAsCommandSteps(t *testing.T) {
	tmpl := `steps:
              - check: the guest must report its hostname`
	out := renderCheckBlock([]any{map[string]any{"what": "the guest must report its hostname", "assertion": "uname -n | grep -q ."}}, "check", tmpl)
	if !strings.Contains(out, "- check: the guest must report its hostname") {
		t.Errorf("render = %q, want the command check step", out)
	}
	if !strings.Contains(out, "command: 'uname -n | grep -q .'") {
		t.Errorf("render = %q, want the deterministic command", out)
	}
	if strings.Contains(out, "agent-check") || strings.Contains(out, "verify with") {
		t.Errorf("render = %q, the agent-check prose emission must be gone", out)
	}
	// an assertion-less check is inert — never rendered
	out2 := renderCheckBlock([]any{map[string]any{"what": "no assertion"}}, "check", tmpl)
	if strings.Contains(out2, "no assertion") {
		t.Errorf("render = %q, an assertion-less check must not render", out2)
	}
}

func TestProbeArtifact(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := probeArtifact(map[string]any{"path": p}); !ok {
		t.Fatal("existing file should pass")
	}
	if ok, _ := probeArtifact(map[string]any{"path": filepath.Join(dir, "nope")}); ok {
		t.Fatal("missing file should fail")
	}
	if ok, _ := probeArtifact(map[string]any{"path": p, "min_bytes": 100}); ok {
		t.Fatal("undersized should fail")
	}
}

func TestProbeExpectExit(t *testing.T) {
	if ok, _ := probeExpectExit(map[string]any{"got": 2, "want": 2}); !ok {
		t.Fatal("matching exit should pass")
	}
	if ok, _ := probeExpectExit(map[string]any{"got": 0, "want": 2}); ok {
		t.Fatal("mismatch should fail")
	}
}

func TestFindBedEntity(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "pr-beds", "pr-1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bed := "check-omarchy-pr-1-vm:\n  vm:\n    from: x\n"
	if err := os.WriteFile(filepath.Join(sub, "charly.yml"), []byte(bed), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBedEntity(dir, "check-omarchy-pr-1-vm"); got == "" {
		t.Fatal("by-name fallback should find the bed")
	}
}
