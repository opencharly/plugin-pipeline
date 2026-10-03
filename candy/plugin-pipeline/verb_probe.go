package pluginpipeline

import (
	"fmt"
	"path/filepath"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_probe.go — the lifted `probe` verb. The plan stage's probe LOOP MOVED here
// from the retired in-process executor when the stage kind became a reachable verb: it is
// registered as {Class: "verb", Word: "probe", InputDef: "#PipelineProbeInput"}
// and dispatched by Invoke (a
// `<word>: <input>` step in any plan). The probe IMPLEMENTATIONS (runProbeV and
// the deterministic verbs) stay in probes.go. ONE body, one caller (R3).

// runVerbProbe is the verb handler: the outputs map plus, on failure, a
// *verbFail carrying the probe message and the stage's redo trigger (the caller
// sets res.Message/Trigger from it and returns the wrapped error).
func runVerbProbe(in params.PipelineProbeInput, e *verbEnv) (map[string]any, error) {
	rc := e
	return runProbeStage(rc, e.stageRaw(in))
}

// runProbeStage runs a probe stage's declared verbs in order and returns the
// stage's Outputs map. On the first failing verb it returns the PARTIAL outputs
// (the verbs that already passed) plus the *verbFail — never the mapped/spread
// outputs, which the original arm also skipped on failure.
func runProbeStage(rc *verbEnv, raw map[string]any) (map[string]any, error) {
	verbs := strList(raw["verbs"])
	input := rc.resolveValue(anyMap(raw["input"]))
	inputMap, _ := input.(map[string]any)
	// path-valued inputs (bed/dir) are workdir-relative: root them at the
	// run workdir (the pipeline may run from a different cwd, e.g. the eval
	// lane's beds live in the eval-omarchy worktree).
	if inputMap != nil && rc.workdir != "" {
		for _, k := range []string{"bed", "dir"} {
			if v := s(inputMap[k]); v != "" && !filepath.IsAbs(v) {
				inputMap[k] = filepath.Join(rc.workdir, v)
			}
		}
	}
	var outputs map[string]any
	for _, v := range verbs {
		// per-verb scoped inputs: the entity may nest each verb's input under
		// its name (media_gate: {...}) — use that when present.
		verbInput := inputMap
		if ni, ok := inputMap[v].(map[string]any); ok {
			verbInput = ni
		}
		// the offline fixture mode (plan §2.3): a fixture: true input runs the
		// probe against canned results - no live infra - so any pipeline's
		// probes are testable offline.
		if verbInput == nil {
			verbInput = map[string]any{}
		}
		if f, _ := verbInput["fixture"].(bool); f {
			if outputs == nil {
				outputs = map[string]any{}
			}
			outputs[v] = "pass"
			continue
		}
		ok, msg, val := runProbeV(v, verbInput, rc)
		if !ok {
			return outputs, &verbFail{
				msg:     msg,
				trigger: triggerOnFail(raw, "redo-plan"),
				err:     fmt.Errorf("probe %s failed: %s", v, msg),
			}
		}
		if outputs == nil {
			outputs = map[string]any{}
		}
		outputs[v] = "pass"
		if val != nil {
			outputs["value"] = val
		}
	}
	// expose the probe VALUE: the declared outputs map to it (e.g. resolve_channel
	// -> channel), and map values spread as named outputs (golden/provision).
	for _, o := range strList(raw["outputs"]) {
		if val, found := outputs["value"]; found && o == "channel" {
			outputs[o] = val
		}
	}
	if val, found := outputs["value"]; found {
		if m, ok := val.(map[string]any); ok {
			for k, v := range m {
				outputs[k] = v
			}
		} else {
			outputs["channel"] = val
		}
	}
	delete(outputs, "value")
	return outputs, nil
}
