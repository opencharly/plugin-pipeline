package pluginpipeline

import "testing"

// TestPublishGate_NoValidation: the cold-read's NO_VALIDATION verdict gates
// the publish — the second condition of the publish skip_when, unit-locked.
func TestPublishGate_NoValidation(t *testing.T) {
	rc := &runCtx{pr: "10115", calver: "2026.253.1", workdir: t.TempDir(), env: map[string]string{}, ledger: newLedger()}
	rc.ledger.put(&StageResult{ID: "cold-read", Kind: "agent", Status: "ok", Outputs: map[string]any{"verdict": "NO_VALIDATION"}})
	// the publish skip_when: "$env.EVAL_PUBLISH != approve || @cold-read.verdict == NO_VALIDATION"
	cond := "$env.EVAL_PUBLISH != approve || @cold-read.verdict == NO_VALIDATION"
	if !rc.evalCond(cond) {
		t.Fatal("a NO_VALIDATION cold-read verdict must skip the publish")
	}
	// a PASS verdict with approval set must NOT skip
	rc.ledger.put(&StageResult{ID: "cold-read", Kind: "agent", Status: "ok", Outputs: map[string]any{"verdict": "PASS"}})
	rc.env["EVAL_PUBLISH"] = "approve"
	if rc.evalCond(cond) {
		t.Fatal("a PASS verdict with approval must not skip the publish")
	}
}
