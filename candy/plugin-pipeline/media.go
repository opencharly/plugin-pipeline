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

func (rc *runCtx) runMedia(raw map[string]any, l *ledger) error {
	pr := rc.pr
	if pr == "" {
		pr = "unknown"
	}
	calver := rc.calver
	if calver == "" {
		calver = "run"
	}
	dir := "media/pr-" + pr + "-" + calver
	if d := s(raw["dir"]); d != "" {
		dir = rc.resolveRefs(d)
	}
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
