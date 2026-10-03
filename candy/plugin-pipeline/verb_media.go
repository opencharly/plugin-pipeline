package pluginpipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_media.go — the lifted `media` verb. The plan stage body (runMedia) MOVED
// here from media.go when the stage kind became a reachable verb: it is registered
// as {Class: "verb", Word: "media", InputDef: "#PipelineMediaInput"} and
// dispatched either by runStage (a kind:pipeline stage) or by Invoke (a
// `<word>: <input>` step in any plan). mediaDir — the layout resolver shared with
// the media_gate probe — stays in media.go. ONE body, two callers (R3).

// runVerbMedia is the verb handler. media produces no outputs — its artifact is
// the assembled dir — so only the error is returned. The ledger (unused by the
// body today, kept for its signature) comes from the env.
func runVerbMedia(in params.PipelineMediaInput, e *verbEnv) (map[string]any, error) {
	rc := e.runCtx()
	return nil, rc.runMedia(e.stageRaw(in), e.l)
}

func (rc *runCtx) runMedia(raw map[string]any, l *ledger) error {
	pr := rc.pr
	if pr == "" {
		pr = "unknown"
	}
	calver := rc.calver
	if calver == "" {
		calver = "run"
	}
	dir := rc.mediaDir(raw, pr, calver)
	files := []string{"cast", "gif", "mjpeg", "png"}
	if f := ss(raw["files"]); len(f) > 0 {
		files = f
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, ext := range files {
		src := filepath.Join("/tmp", "pr-"+pr+"."+ext)
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("media: source missing %s", src)
		}
		if err := os.WriteFile(filepath.Join(dir, "pr-"+pr+"."+ext), b, 0o644); err != nil {
			return err
		}
	}
	tr := s(raw["transcode"])
	if tr != "" {
		parts := strings.SplitN(tr, ":", 2)
		if len(parts) == 2 {
			src := filepath.Join(dir, "pr-"+pr+"."+parts[0])
			dst := filepath.Join(dir, "pr-"+pr+"."+parts[1])
			if _, err := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", src,
				"-c:v", "libx264", "-pix_fmt", "yuv420p", dst).CombinedOutput(); err != nil {
				return fmt.Errorf("media: transcode failed: %v", err)
			}
		}
	}
	return nil
}
