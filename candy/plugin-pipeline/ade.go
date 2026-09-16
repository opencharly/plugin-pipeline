package pluginpipeline

// ade.go — the org-wide ADE evaluation stage. The rendered ORACLE bed (from:
// golden + pr-apply + the agent-check steps + the media loop) is run through the
// HOST's compiled-in check-run ONCE — the org's R10 machinery (build -> deploy ->
// check live with the agent-check ADE grading -> teardown) — with the org's batch
// flags (--var passes the per-PR data; --keep-venue the venue). No custom bed
// contracts, no per-step orchestration.
//
// RCA (why NOT in-plugin dial dispatch): the plugin is fork/exec'd by charly's
// COMMAND dispatch in CLI mode (sdk.Main, IsServeMode false) — no go-plugin
// broker, no reverse-channel executor. The in-venue verb dispatch only exists
// compiled-in (plugin-check). The org's check-run is the compiled-in drive; the
// pipeline invokes it ONCE and takes the deterministic exit contract.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runAdeBed invokes the org's check-run for the rendered bed and maps its exit
// contract to the deterministic verdict (0 PASS, 2 FAIL, 3 SKIP, else
// NO_VALIDATION).
//
// headSHA is the LANE's head, passed explicitly. It must NOT be read from the
// process env: the batch lanes run concurrently and the per-lane value lives in
// the run context (executor.go's "$env.PR_HEAD_SHA to this lane's head"), so an
// os.Getenv here returns the operator's value or EMPTY — never this lane's.
// Live-caught: the run logged `--var PR_HEAD_SHA=` while the lane's context
// carried a real sha.
func runAdeBed(ctx context.Context, pr, headSHA, bed, workdir string) (string, string, int, error) {
	charlyBin := os.Getenv("CHARLY_BIN")
	if charlyBin == "" {
		charlyBin = "charly"
	}
	if bed == "" {
		bed = "check-omarchy-pr-" + pr + "-vm"
	}
	args := []string{
		"check", "run", bed,
		"--var", "PR_NUMBER=" + pr,
		"--var", "PR_HEAD_SHA=" + headSHA,
		"--keep-venue",
	}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	cmd := exec.CommandContext(ctx, charlyBin, args...)
	cmd.Env = os.Environ()
	out, rerr := cmd.CombinedOutput()
	summary := lastLines(string(out), 12, 400)
	code := 0
	if rerr != nil {
		if ee, ok := rerr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return "NO_VALIDATION", "ade: run: " + rerr.Error(), 0, rerr
		}
	}
	// the runner NEVER leaves a VM running: --keep-venue kept the domain for
	// the evidence collection — destroy it now (best-effort; a lingering
	// domain holds the golden's snapshot and blocks the next lane's
	// sequencing gate). --if-exists makes an already-absent venue a SUCCESS:
	// without it every clean lane logged a spurious
	// `no such VM … nothing destroyed` error.
	destroy := exec.CommandContext(ctx, charlyBin, "vm", "destroy", bed, "--if-exists")
	destroy.Env = os.Environ()
	if dout, derr := destroy.CombinedOutput(); derr != nil {
		summary += "\n[ade] venue destroy: " + strings.TrimSpace(string(dout))
	}
	verdict := adeVerdictForExit(code)
	return verdict, summary, code, nil
}

// lastLines keeps the TAIL of a multi-line diagnostic, capped at maxBytes. The
// predecessor kept the last 400 BYTES of the message, which sliced a mid-word
// fragment (the live log showed `check-run exit 1: eline/candy/plugin-pipeline`)
// and DROPPED the actual diagnosis — the trailing lines are the error, the
// leading ones are the banner. Whole lines, so the message stays readable.
func lastLines(s string, maxLines, maxBytes int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(out) > maxBytes {
		out = "…" + out[len(out)-maxBytes:]
	}
	return out
}

// adeVerdict resolves the rendered bed for the lane's PR and runs the org
// check-run; returns the deterministic verdict + the run summary.
func adeVerdict(ctx context.Context, pr, headSHA, bed, workdir string) (string, string, error) {
	if bed == "" {
		bed = "check-omarchy-pr-" + pr + "-vm"
	}
	// the bed is resolved BY ENTITY NAME (the loader's own discovery) — never a
	// hardcoded `pr-beds/` layout (the layout belongs to the lane).
	if findBedEntity(workdir, bed) == "" {
		return "NO_VALIDATION", "ade: bed entity missing: " + bed, fmt.Errorf("ade: bed entity %q not found under %s", bed, workdir)
	}
	verdict, summary, code, err := runAdeBed(ctx, pr, headSHA, bed, workdir)
	if err != nil {
		return verdict, summary, err
	}
	return verdict, fmt.Sprintf("check-run exit %d: %s", code, summary), nil
}

// adeVerdictForExit maps the org check-run exit contract to the deterministic
// report verdict: 0 PASS, 2 FAIL (checks ran + failed), 3 SKIP (a host
// prerequisite is absent), anything else NO_VALIDATION.
func adeVerdictForExit(code int) string {
	switch code {
	case 0:
		return "PASS"
	case 2:
		return "FAIL"
	case 3:
		return "NO_VALIDATION"
	default:
		return "NO_VALIDATION"
	}
}

// findBedEntity: a bounded search for a charly.yml under the workdir that
// declares the named check-bed entity (the by-name resolution fallback).
func findBedEntity(workdir, name string) string {
	found := ""
	_ = filepath.Walk(workdir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "charly.yml") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(b), name+":") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
