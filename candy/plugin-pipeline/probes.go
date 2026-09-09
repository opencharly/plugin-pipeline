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
	if x, ok := v.(float64); ok {
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
func runProbeV(word string, input map[string]any) (bool, string, any) {
	if input == nil {
		input = map[string]any{}
	}
	switch word {
	case "media_gate":
		ok, msg := probeMediaGate(input)
		return ok, msg, nil
	case "lock_audit":
		ok, msg := probeLockAudit(input)
		return ok, msg, nil
	case "evidence_audit":
		if ok, msg := probeMediaGate(input); !ok {
			return ok, msg, nil
		}
		ok, msg := probeLockAudit(input)
		return ok, msg, nil
	case "golden_present", "head_freshness", "sequencing", "lanes_ok", "config_audit":
		ok, msg := runProbe(word, input)
		return ok, msg, nil
	case "resolve_channel":
		ok, msg, ch := probeResolveChannelV(input)
		return ok, msg, ch
	}
	ok, msg := runProbe(word, input)
	return ok, msg, nil
}

func probeResolveChannelV(input map[string]any) (bool, string, any) {
	channels := mm(input["channels"])
	def := s(input["default"])
	if def == "" {
		def = "stable"
	}
	if len(channels) == 0 {
		return false, "resolve_channel: no channels registry", nil
	}
	entry, ok := channels[def]
	if !ok {
		return false, "resolve_channel: default not in registry: " + def, nil
	}
	// the VALUE is the registry ENTRY (golden/provision fields), so the probe
	// stage can expose them as outputs and the lane can render the bed's from:.
	return true, "", entry
}

func runProbe(word string, input map[string]any) (bool, string) {
	if input == nil {
		input = map[string]any{}
	}
	switch word {
	case "media_gate":
		return probeMediaGate(input)
	case "lock_audit":
		return probeLockAudit(input)
	case "evidence_audit":
		if ok, msg := probeMediaGate(input); !ok {
			return ok, msg
		}
		return probeLockAudit(input)
	case "golden_present":
		return probeGoldenPresent(input)
	case "head_freshness":
		return probeHeadFreshness(input)
	case "lanes_ok", "sequencing":
		return probeSequencing(input)
	case "resolve_channel":
		return probeResolveChannel(input)
	case "config_audit":
		return probeConfigAudit(input)

	}
	return false, "unknown probe " + word
}

func probeMediaGate(input map[string]any) (bool, string) {
	dir := s(input["dir"])
	files := ss(input["files"])
	min := mm(input["min"])
	if dir == "" || len(files) == 0 {
		return false, "media_gate: dir + files required"
	}
	for _, f := range files {
		// accept both pr-<pr>.<ext> (the media stage naming) and the bare <ext>
		cands := []string{filepath.Join(dir, "pr-"+prN()+"."+f), filepath.Join(dir, f)}
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

// prN: the current run's pr for the media naming (set by runPlan via rc).
func prN() string {
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

		// the PROVENANCE cache: rebuild the chain only when the source iso changed.
		// The rebuild recipe writes <disk>.pipeline-ref with the iso sha256 it was
		// built from; a mismatch (or missing marker) means the golden is stale.
		if ref := s(input["ref"]); ref != "" {
			b, rerr := os.ReadFile(golden + ".pipeline-ref")
			if rerr != nil || strings.TrimSpace(string(b)) != ref {
				return false, "golden stale: built from a different iso (run the rebuild recipe - it writes the built-iso marker)"
			}
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
