package pluginpipeline

import (
	"path/filepath"
	"regexp"
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
//
// NOTE (behaviour change from the pre-fix code): a RELATIVE result — including
// the legacy default — is now joined with rc.workdir. The old code joined only
// the raw["dir"] branch, so the default was CWD-relative; the workdir is the
// project root (the lane runs with workdir == the eval-omarchy checkout), so
// this makes the default land in the repo as intended.
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

// runMedia — the `media` stage body — lives in verb_media.go, its lifted verb:
// it is registered as verb:media and reachable from any plan.
