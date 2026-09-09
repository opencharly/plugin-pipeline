package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

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
