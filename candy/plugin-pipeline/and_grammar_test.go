package pluginpipeline

import "testing"

// The AND grammar: the report gate "@eval.verdict != PASS && @eval.verdict !=
// FAIL" must skip ONLY when the verdict is neither PASS nor FAIL — the
// pre-AND-grammar parse treated the whole condition as one comparison against
// the literal "PASS && @eval.verdict != FAIL" string, so the gate ALWAYS
// skipped (the report/cold-read/render stages never ran on a FAIL verdict).
func TestEvalCond_AndGrammar(t *testing.T) {
	rc := &runCtx{ledger: newLedger()}
	rc.ledger.results["eval"] = &StageResult{ID: "eval", Kind: "ade", Outputs: map[string]any{"verdict": "FAIL"}}
	gate := "@eval.verdict != PASS && @eval.verdict != FAIL"
	if rc.evalCond(gate) {
		t.Fatalf("evalCond(%q) = true with verdict FAIL — the report must RUN on a FAIL verdict", gate)
	}
	rc.ledger.results["eval"] = &StageResult{ID: "eval", Kind: "ade", Outputs: map[string]any{"verdict": "NO_VALIDATION"}}
	if !rc.evalCond(gate) {
		t.Fatalf("evalCond(%q) = false with verdict NO_VALIDATION — the report must SKIP", gate)
	}
	rc.ledger.results["eval"] = &StageResult{ID: "eval", Kind: "ade", Outputs: map[string]any{"verdict": "PASS"}}
	if rc.evalCond(gate) {
		t.Fatalf("evalCond(%q) = true with verdict PASS — the report must RUN on a PASS verdict", gate)
	}
}
