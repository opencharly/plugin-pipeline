package pluginpipeline

import (
	"path/filepath"
	"testing"
)

// The media stage must write where the media_gate / ledger-gate / report read.
// Regression: the stage ignored the pipeline-level media.dir and used the legacy
// media/pr-<pr>-<calver> default, so every PR eval lost its media.
func TestMediaDir_FallsBackToPipelineMediaDir(t *testing.T) {
	wd := t.TempDir()
	rc := &runCtx{
		pr:      "10115",
		calver:  "2026.256.2137",
		workdir: wd,
		media:   map[string]any{"dir": "media/$calver/pr-$pr"},
	}
	got := rc.mediaDir(map[string]any{}, "10115", "2026.256.2137")
	want := filepath.Join(wd, "media", "2026.256.2137", "pr-10115")
	if got != want {
		t.Fatalf("mediaDir = %q, want the pipeline media.dir %q", got, want)
	}
}

func TestMediaDir_StageDirWinsOverPipelineDir(t *testing.T) {
	wd := t.TempDir()
	rc := &runCtx{
		pr:      "9",
		calver:  "c",
		workdir: wd,
		media:   map[string]any{"dir": "media/$calver/pr-$pr"},
	}
	got := rc.mediaDir(map[string]any{"dir": "custom/$pr"}, "9", "c")
	want := filepath.Join(wd, "custom", "9")
	if got != want {
		t.Fatalf("mediaDir = %q, want the stage dir %q", got, want)
	}
}

func TestMediaDir_LegacyDefaultWhenUnset(t *testing.T) {
	wd := t.TempDir()
	rc := &runCtx{pr: "9", calver: "c", workdir: wd}
	got := rc.mediaDir(map[string]any{}, "9", "c")
	want := filepath.Join(wd, "media", "pr-9-c")
	if got != want {
		t.Fatalf("mediaDir = %q, want the legacy default %q", got, want)
	}
}
