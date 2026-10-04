package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChecksRenderAsCommandSteps is the B12 coverage for the deterministic
// command-check rendering (the agent-check prose emission is GONE — hard
// cutover, R5): each check renders as check: + id: + command: and EXECUTES in
// the venue. A check without an assertion is inert and never rendered.
func TestChecksRenderAsCommandSteps(t *testing.T) {
	tmpl := `steps:
              - check: the guest must report its hostname`
	out := renderCheckBlock([]any{map[string]any{"what": "the guest must report its hostname", "assertion": "uname -n | grep -q ."}}, "check", tmpl, false)
	if !strings.Contains(out, "- check: 'the guest must report its hostname'") {
		t.Errorf("render = %q, want the command check step", out)
	}
	if !strings.Contains(out, "command: 'uname -n | grep -q .'") {
		t.Errorf("render = %q, want the deterministic command", out)
	}
	if strings.Contains(out, "agent-check") || strings.Contains(out, "verify with") {
		t.Errorf("render = %q, the agent-check prose emission must be gone", out)
	}
	// an assertion-less check is inert — never rendered
	out2 := renderCheckBlock([]any{map[string]any{"what": "no assertion"}}, "check", tmpl, false)
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
	// The REAL lane shape: the workdir's root charly.yml `discover:`s eval/, and the
	// rendered per-PR bed lives at eval/pr-<N>/charly.yml. findBedEntity resolves it
	// through the SAME project-directive walk loadEntity uses.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte(
		"version: 2026.249.2125\ndiscover:\n    - path: eval\n      recursive: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "eval", "pr-1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bed := "check-omarchy-pr-1-vm:\n  vm:\n    from: x\n"
	if err := os.WriteFile(filepath.Join(sub, "charly.yml"), []byte(bed), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBedEntity(dir, "check-omarchy-pr-1-vm"); got == "" {
		t.Fatal("by-name resolver should find the discovered bed")
	}
}

// TestFindBedEntity_RealLaneRootDiscoversEval proves the REAL lane shape resolves: the repo root
// charly.yml declares `discover: - path: eval`, and the rendered bed lives at
// eval/pr-<N>/charly.yml. This mirrors eval-omarchy's actual root (verified: its charly.yml has
// `path: eval` recursive).
func TestFindBedEntity_RealLaneRootDiscoversEval(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte(
		"version: 2026.249.2125\ndiscover:\n    - path: eval\n      recursive: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bed := filepath.Join(dir, "eval", "pr-9", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bed, []byte("check-omarchy-pr-9-vm:\n    vm:\n        from: x\n        disposable: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBedEntity(dir, "check-omarchy-pr-9-vm"); got == "" {
		t.Fatal("the real lane shape (root discover: eval + eval/pr-9/charly.yml) must resolve")
	}
}

// TestFindBedEntity_FallbackWhenRootUnreachable proves the bounded fallback: a bed the directive
// walk cannot reach (no root charly.yml at all) is still found, so an unreachable root cannot
// masquerade as "bed missing" (which would yield a vacuous PASS downstream).
func TestFindBedEntity_FallbackWhenRootUnreachable(t *testing.T) {
	dir := t.TempDir()
	// NO root charly.yml — the directive walk cannot start.
	bed := filepath.Join(dir, "pr-beds", "pr-1", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bed, []byte("check-omarchy-pr-1-vm:\n    vm:\n        from: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBedEntity(dir, "check-omarchy-pr-1-vm"); got == "" {
		t.Fatal("the bounded fallback must still find a bed when the root is unreachable")
	}
}

// TestFindBedEntity_FallbackAfterMalformedRoot proves a malformed root does not collapse to
// "bed missing".
func TestFindBedEntity_FallbackAfterMalformedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte("import: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bed := filepath.Join(dir, "beds", "charly.yml")
	if err := os.MkdirAll(filepath.Dir(bed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bed, []byte("check-x:\n    vm:\n        from: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findBedEntity(dir, "check-x"); got == "" {
		t.Fatal("a malformed root must not collapse to 'bed missing' — the fallback must find the bed")
	}
}
