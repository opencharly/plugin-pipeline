package pluginpipeline

import (
	"context"
	"strings"
	"testing"
)

// TestTypedDecode_ContractViolations: the typed decoder rejects out-of-contract
// replies with the exact field + expected type — the informed redo signal.
func TestTypedDecode_ContractViolations(t *testing.T) {
	// a valid reply decodes
	v, err := decodeTypedOutput(`{"class": "visual", "tier": "vm", "checks": [{"id": "a", "assertion": "grep -q x /f"}]}`, "class", map[string]any{"type": "enum", "enum": []any{"system", "packaging", "visual", "skip"}})
	if err != nil || v != "visual" {
		t.Fatalf("valid enum decode: v=%v err=%v", v, err)
	}
	// an out-of-enum value is rejected with the allowed list
	_, err = decodeTypedOutput(`{"class": "bogus"}`, "class", map[string]any{"type": "enum", "enum": []any{"system", "packaging", "visual"}})
	if err == nil {
		t.Fatal("out-of-enum value must be rejected")
	}
	// a missing field is rejected with the contract
	_, err = decodeTypedOutput(`{"other": 1}`, "class", map[string]any{"type": "string"})
	if err == nil {
		t.Fatal("a missing declared output must be rejected")
	}
	// a string_list validates
	v, err = decodeTypedOutput(`{"files": ["a", "b"]}`, "files", map[string]any{"type": "string_list"})
	if err != nil || len(v.([]string)) != 2 {
		t.Fatalf("string_list decode: v=%v err=%v", v, err)
	}
	// a non-JSON reply is rejected informatively
	_, err = decodeTypedOutput("just prose, no object", "class", map[string]any{"type": "string"})
	if err == nil {
		t.Fatal("a prose-only reply must be rejected")
	}
}

// TestVerdictGate_SkipWhen: the designed seam — a FAIL verdict + skip_when on a
// downstream stage records it as skipped, never as ok. The regression lock for
// the computed-but-dropped verdict (RCA 2026.252.2250).
func TestVerdictGate_SkipWhen(t *testing.T) {
	rc := &runCtx{pr: "10115", calver: "2026.252.1", workdir: t.TempDir(), env: map[string]string{}, ledger: newLedger()}
	rc.ledger.put(&StageResult{ID: "eval", Kind: "ade", Status: "ok", Outputs: map[string]any{"verdict": "FAIL", "summary": "check-run exit 2"}})
	// the report stage declares skip_when: "@eval.verdict != PASS"
	res := &StageResult{ID: "report", Kind: "agent", Status: "ok"}
	if sw := "@eval.verdict != PASS"; sw != "" {
		if rc.evalCond(sw) {
			res.Status = "skipped"
			res.Message = "skipped: " + sw
		}
	}
	if res.Status != "skipped" {
		t.Fatalf("a FAIL verdict must gate the report stage to skipped, got %s", res.Status)
	}
	// and a PASS verdict lets it run
	rc.ledger.put(&StageResult{ID: "eval", Kind: "ade", Status: "ok", Outputs: map[string]any{"verdict": "PASS"}})
	res2 := &StageResult{ID: "report", Kind: "agent", Status: "ok"}
	if sw := "@eval.verdict != PASS"; sw != "" {
		if rc.evalCond(sw) {
			res2.Status = "skipped"
		}
	}
	if res2.Status != "ok" {
		t.Fatalf("a PASS verdict must let the report run, got %s", res2.Status)
	}
}

// TestLedgerFacts: the structured context injection — the agent narrates from
// facts, never from path-guessing.
func TestLedgerFacts(t *testing.T) {
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"plan-json": map[string]any{"class": "visual"}}})
	l.put(&StageResult{ID: "eval", Kind: "ade", Status: "ok", Outputs: map[string]any{"verdict": "FAIL", "summary": "check-run exit 2"}})
	f := l.facts()
	if !strings.Contains(f, "triage [agent ok]") || !strings.Contains(f, "eval [ade ok]") {
		t.Fatalf("facts must carry every stage: %q", f)
	}
	if !strings.Contains(f, "verdict = \"FAIL\"") {
		t.Fatalf("facts must carry the verdict: %q", f)
	}
}

var _ = context.Background
