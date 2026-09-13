package pluginpipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// media.go — the ENGINE-NATIVE media stage: assemble the run's /tmp artifacts
// into the configured dir and transcode the MJPEG to screen.mp4 (in-process call
// to the ffmpeg binary — a real tool, never a charly subprocess).

var calverRe = regexp.MustCompile("[0-9]{3,}") // extract calver from a run dir when not passed

// mediaDir resolves the media stage's output dir. Precedence: the stage's own
// dir: (if authored), then the pipeline-level media.dir (the lane's ONE layout
// declaration), then the legacy media/pr-<pr>-<calver> default. The media.dir
// fallback is load-bearing: without it the stage silently wrote the legacy dir
// while the media_gate / ledger-gate / report read the configured layout, so
// every PR eval lost its media (RCA: the layout cutover changed the config dirs
// but the stage reads only its own raw["dir"]).
func (rc *runCtx) mediaDir(raw map[string]any, pr, calver string) string {
	dir := "media/pr-" + pr + "-" + calver
	switch {
	case s(raw["dir"]) != "":
		dir = rc.resolveRefs(s(raw["dir"]))
	case rc.media != nil && s(rc.media["dir"]) != "":
		dir = rc.resolveRefs(s(rc.media["dir"]))
	}
	if !filepath.IsAbs(dir) && rc.workdir != "" {
		dir = filepath.Join(rc.workdir, dir)
	}
	return dir
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
