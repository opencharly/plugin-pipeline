package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/opencharly/spec/proto"
	"gopkg.in/yaml.v3"
)

// probes.go — verb:pipeline: the deterministic probe verbs. Each probe is a pure
// function (input) -> (ok, message); offline-capable (unit fixtures supply inputs).

func runVerb(req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var params map[string]any
	if len(req.GetParamsJson()) > 0 {
		_ = json.Unmarshal(req.GetParamsJson(), &params)
	}
	word := req.GetReserved()
	in, _ := params["plugin_input"].(map[string]any)
	ok, msg := runProbe(word, in)
	reply := map[string]any{"status": "pass"}
	if !ok {
		reply = map[string]any{"status": "fail", "message": msg}
	}
	b, _ := json.Marshal(reply)
	return &pb.InvokeReply{ResultJson: b}, nil
}

func s(v any) string {
	if x, ok := v.(string); ok {
		return x
	}
	return ""
}
func i(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}
func ss(v any) []string {
	if x, ok := v.([]any); ok {
		var out []string
		for _, e := range x {
			if z, ok := e.(string); ok {
				out = append(out, z)
			}
		}
		return out
	}
	return nil
}
func mm(v any) map[string]any { m, _ := v.(map[string]any); return m }

// runProbeV returns (ok, message, value) — the value powers probe stage outputs
// (e.g. resolve_channel returns the resolved channel).
func runProbeV(word string, input map[string]any, rc *runCtx) (bool, string, any) {
	if input == nil {
		input = map[string]any{}
	}
	switch word {
	case "media_gate":
		ok, msg := probeMediaGate(input, rc)
		return ok, msg, nil
	case "artifact":
		ok, msg := probeArtifact(input)
		return ok, msg, nil
	case "expect_exit":
		ok, msg := probeExpectExit(input)
		return ok, msg, nil

	case "lock_audit":
		ok, msg := probeLockAudit(input)
		return ok, msg, nil
	case "evidence_audit":
		if ok, msg := probeMediaGate(input, rc); !ok {
			return ok, msg, nil
		}
		ok, msg := probeLockAudit(input)
		return ok, msg, nil
	case "golden_present":
		ok, msg := probeGoldenPresent(input)
		return ok, msg, nil
	case "head_freshness":
		ok, msg := probeHeadFreshness(input)
		return ok, msg, nil
	case "lanes_ok":
		ok, msg := probeSequencing(input)
		return ok, msg, nil
	case "sequencing":
		ok, msg := probeSequencing(input)
		return ok, msg, nil
	case "config_audit":
		ok, msg := probeConfigAudit(input)
		return ok, msg, nil
	case "ledger_gate":
		ok, msg, val := probeLedgerGate(input, rc)
		return ok, msg, val
	case "resolve_channel":
		ok, msg, ch := probeResolveChannelV(input)
		return ok, msg, ch
	}
	return false, "unknown probe " + word, nil
}

func probeResolveChannelV(input map[string]any) (bool, string, any) {
	channels := mm(input["channels"])
	// the ORACLE's chosen channel key — the config oracle decides the venue,
	// the registry is the deterministic mapping. The env-selected default is
	// GONE (the EVAL_CHANNEL era is over).
	key := s(input["channel"])
	if key == "" {
		key = "stable"
	}
	if len(channels) == 0 {
		return false, "resolve_channel: no channels registry", nil
	}
	entry, ok := channels[key]
	if !ok {
		return false, "resolve_channel: channel not in registry: " + key, nil
	}
	// the VALUE is the registry ENTRY (golden/provision fields), so the probe
	// stage can expose them as outputs and the lane can render the bed's from:.
	return true, "", entry
}

// runProbe: the legacy/CLI wrapper — no run context, the env fallback only.
func runProbe(word string, input map[string]any) (bool, string) {
	ok, msg, _ := runProbeV(word, input, nil)
	return ok, msg
}

func probeMediaGate(input map[string]any, rc *runCtx) (bool, string) {
	dir := s(input["dir"])
	files := ss(input["files"])
	min := mm(input["min"])
	_ = rc
	if dir == "" || len(files) == 0 {
		return false, "media_gate: dir + files required"
	}
	for _, f := range files {
		// accept both pr-<pr>.<ext> (the media stage naming) and the bare <ext>
		cands := []string{filepath.Join(dir, "pr-"+prN(rc)+"."+f), filepath.Join(dir, f)}
		ok := false
		for _, p := range cands {
			if st, err := os.Stat(p); err == nil {
				ok = true
				if want := i(min[f]); want > 0 && st.Size() < int64(want) {
					return false, fmt.Sprintf("media_gate: size %d < min %d", st.Size(), want)
				}
				break
			}
		}
		if !ok {
			return false, fmt.Sprintf("media_gate: missing %s (or pr-<pr>%s)", filepath.Join(dir, f), f)
		}
	}
	return true, ""
}

// prN: the current run's pr for the media naming — the RUN CONTEXT first
// (the probe layer's member of the RCA 2026.252.2210 env-race class: the
// env fell back to the batch process-global, so the media gate checked
// pr-<env>.<ext> instead of pr-<lane>.<ext>), the env only for CLI/legacy.
func prN(rc *runCtx) string {
	if rc != nil && rc.pr != "" {
		return rc.pr
	}
	if p := os.Getenv("EVAL_PR_NUMBER"); p != "" {
		return p
	}
	if p := os.Getenv("PR_NUMBER"); p != "" {
		return p
	}
	return "unknown"
}

var lockRe = regexp.MustCompile("(?i)database is locked|failed to get.*write.*lock")

func probeLockAudit(input map[string]any) (bool, string) {
	trees := ss(input["trees"])
	for _, tree := range trees {
		hits := 0
		_ = filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || hits > 3 {
				return nil
			}
			if lockRe.MatchString(readHead(path)) {
				hits++
			}
			return nil
		})
		if hits > 0 {
			return false, fmt.Sprintf("lock_audit: %d incident(s) under %s", hits, tree)
		}
	}
	return true, ""
}

func readHead(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	return string(b)
}

func probeGoldenPresent(input map[string]any) (bool, string) {
	golden := s(input["golden"])
	if golden == "" {
		golden = filepath.Join(os.Getenv("HOME"), ".local/share/charly/vm/charly-check-omarchy-eval-base-inst/snapshots/golden/disk.qcow2")
	}
	st, err := os.Stat(golden)
	if err != nil || st.Size() == 0 {
		return false, "golden missing or empty: " + golden
	}
	out, _ := exec.Command("fuser", golden).Output()
	if len(out) > 0 {
		return false, "golden held (fuser): " + golden
	}
	// the PROVENANCE cache: rebuild the chain only when the source iso changed.
	// The rebuild recipe writes <disk>.pipeline-ref with the iso sha256 it was
	// built from; a mismatch (or missing marker) means the golden is stale.
	if ref := s(input["ref"]); ref != "" {
		b, rerr := os.ReadFile(golden + ".pipeline-ref")
		if rerr != nil || strings.TrimSpace(string(b)) != ref {
			return false, "golden stale: built from a different iso (run the rebuild recipe - it writes the built-iso marker)"
		}
	}
	return true, ""
}

func probeHeadFreshness(input map[string]any) (bool, string) {
	planSha := s(input["plan_sha"])
	pr := strconv.Itoa(i(input["pr"]))
	repo := s(input["repo"])
	if planSha == "" || repo == "" {
		return false, "head_freshness: plan_sha + repo required"
	}
	out, err := exec.Command("gh", "api", fmt.Sprintf("/repos/%s/pulls/%s", repo, pr), "--jq", ".head.sha").Output()
	if err != nil {
		return false, "head_freshness: gh failed"
	}
	if strings.TrimSpace(string(out)) != planSha {
		return false, "head_freshness: plan head stale"
	}
	return true, ""
}

var guestVmRe = regexp.MustCompile("guest=charly-check-omarchy-pr-[0-9]+-vm")

func probeSequencing(input map[string]any) (bool, string) {
	out, _ := exec.Command("ps", "aux").Output()
	if len(guestVmRe.FindAll(out, -1)) > 0 {
		return false, "sequencing: live batch VMs present"
	}
	return probeGoldenPresent(input)
}

func probeResolveChannel(input map[string]any) (bool, string) {
	channels := mm(input["channels"])
	def := s(input["default"])
	if def == "" {
		def = "stable"
	}
	if len(channels) == 0 {
		return false, "resolve_channel: no channels registry"
	}
	if _, ok := channels[def]; !ok {
		return false, "resolve_channel: default not in registry: " + def
	}
	return true, ""
}

func probeConfigAudit(input map[string]any) (bool, string) {
	bed := s(input["bed"])
	if bed == "" {
		return false, "config_audit: bed required"
	}
	b, err := os.ReadFile(bed)
	if err != nil {
		return false, "config_audit: bed unreadable"
	}
	if !strings.Contains(string(b), "pr-apply") {
		return false, "config_audit: pr-apply seam missing"
	}
	return true, ""
}

// probeLedgerGate: the deterministic worthless-eval catch. Computes from the
// evidence tree, never from prose:
//   - executed_checks: the count of EXECUTED steps in the eval bed's latest run
//     (the behavior-* + pr-tests + pr-apply steps — the recording steps are
//     NOT verification; media presence never implies verification).
//   - control_ok: the control bed's latest run passed completely (every
//     negated check ok = every knownRed claim proven).
//   - media_ok: the media gate on the run's media dir.
//
// The gate FAILS when executed_checks == 0 or the control did not pass — the
// lane classifies SETUP_DEFECT, never a publish.
func probeLedgerGate(input map[string]any, rc *runCtx) (bool, string, any) {
	bed := s(input["bed"])
	controlBed := s(input["control_bed"])
	mediaDir := s(input["media_dir"])
	if bed == "" || controlBed == "" {
		return false, "ledger_gate: bed + control_bed required", nil
	}
	executed := countExecutedSteps(rc, bed)
	controlOK := controlPassed(rc, controlBed)
	mediaOK := true
	if mediaDir != "" {
		ok, msg := probeMediaGate(map[string]any{"dir": mediaDir, "files": []any{"cast", "gif", "mjpeg", "mp4", "png"}, "min": map[string]any{"cast": 200, "gif": 1024, "mjpeg": 4096, "mp4": 4096, "png": 1024}}, rc)
		mediaOK = ok
		if !ok {
			return false, "ledger_gate: media gate: " + msg, nil
		}
	}
	val := map[string]any{"executed_checks": executed, "control_ok": controlOK, "media_ok": mediaOK}
	if executed == 0 {
		return false, "ledger_gate: zero executed checks in the eval run — the eval verified nothing (SETUP_DEFECT, never a publish)", val
	}
	if !controlOK {
		return false, "ledger_gate: the control bed did not pass — a knownRed claim is unproven (a FAKE assertion)", val
	}
	return true, "", val
}

// countExecutedSteps: the executed step count of the bed's LATEST run — the
// steps that actually ran (ok or fail), excluding the recording loop.
func countExecutedSteps(rc *runCtx, bed string) int {
	base := filepath.Join(rc.workdir, ".check", bed)
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	latest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(base, latest, "summary.yml"))
	if err != nil {
		return 0
	}
	var sum struct {
		Steps []struct {
			Name string `yaml:"name"`
			OK   bool   `yaml:"ok"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(b, &sum); err != nil {
		return 0
	}
	n := 0
	for _, st := range sum.Steps {
		if strings.HasPrefix(st.Name, "check ") && !strings.Contains(st.Name, "recording") && !strings.Contains(st.Name, "SPICE") && !strings.Contains(st.Name, "screenshot") && !strings.Contains(st.Name, "GIF") && !strings.Contains(st.Name, "MP4") {
			n++
		}
	}
	return n
}

// controlPassed: the control bed's latest run passed completely.
func controlPassed(rc *runCtx, bed string) bool {
	base := filepath.Join(rc.workdir, ".check", bed)
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	latest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(base, latest, "summary.yml"))
	if err != nil {
		return false
	}
	var sum struct {
		Steps []struct {
			OK bool `yaml:"ok"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(b, &sum); err != nil {
		return false
	}
	for _, st := range sum.Steps {
		if !st.OK {
			return false
		}
	}
	return len(sum.Steps) > 0
}
