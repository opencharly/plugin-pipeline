package pluginpipeline

import (
	"fmt"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_gate.go — the lifted `gate` verb: the plan stage's deterministic
// condition check MOVED here from executor.go's runStage when the stage kind
// became a reachable verb. It is registered as
// {Class: "verb", Word: "gate", InputDef: "#PipelineGateInput"} and dispatched
// either by runStage (a kind:pipeline stage) or by Invoke (a `<word>: <input>`
// step in any plan). ONE body, two callers (R3).

// runVerbGate is the verb handler: a false condition is a *verbFail whose
// Message is the bare condition text while the returned error names the stage
// ("gate <id>: <msg>") — exactly the pair the original arm reported.
func runVerbGate(in params.PipelineGateInput, e *verbEnv) (map[string]any, error) {
	return runGateStage(e.runCtx(), e.stageID(), e.stageRaw(in))
}

// runGateStage evaluates the stage's condition against the ledger and fails with
// the human-readable reason. No outputs.
func runGateStage(rc *runCtx, id string, raw map[string]any) (map[string]any, error) {
	if ok, msg := evalCondition(rc.resolveRefs(asString(raw["condition"]))); !ok {
		return nil, &verbFail{msg: msg, err: fmt.Errorf("gate %s: %s", id, msg)}
	}
	return nil, nil
}
