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
func runAdeBed(ctx context.Context, pr, bed, workdir string) (string, string, int, error) {
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
		"--var", "PR_HEAD_SHA=" + os.Getenv("PR_HEAD_SHA"),
		"--keep-venue",
	}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	cmd := exec.CommandContext(ctx, charlyBin, args...)
	cmd.Env = os.Environ()
	out, rerr := cmd.CombinedOutput()
	summary := strings.TrimSpace(string(out))
	if len(summary) > 400 {
		summary = summary[len(summary)-400:]
	}
	code := 0
	if rerr != nil {
		if ee, ok := rerr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return "NO_VALIDATION", "ade: run: " + rerr.Error(), 0, rerr
		}
	}
	verdict := adeVerdictForExit(code)
	return verdict, summary, code, nil
}

// adeVerdict resolves the rendered bed for the lane's PR and runs the org
// check-run; returns the deterministic verdict + the run summary.
func adeVerdict(ctx context.Context, pr, bed, workdir string) (string, string, error) {
	if bed == "" {
		bed = "check-omarchy-pr-" + pr + "-vm"
	}
	bedFile := filepath.Join(workdir, "pr-beds", "pr-"+pr, "charly.yml")
	if strings.HasSuffix(bed, "-control") {
		bedFile = filepath.Join(workdir, "pr-beds", "pr-"+pr+"-control", "charly.yml")
	}
	if _, err := os.Stat(bedFile); err != nil {
		// the by-name fallback (plan §2.1): resolve ANY existing check-bed entity
		// from the project's discovered files (the imports' check-bed entities).
		if found := findBedEntity(workdir, bed); found != "" {
			bedFile = found
		} else {
			return "NO_VALIDATION", "ade: rendered bed missing: " + bedFile, err
		}
	}
	verdict, summary, code, err := runAdeBed(ctx, pr, bed, workdir)
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
