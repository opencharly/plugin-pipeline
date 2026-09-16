package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestEmitStage_SchemaFirstRecord is the R10 coverage for the `emit` stage: a
// structured value is validated against a CUE def BEFORE it is written, so the
// artifact can never be invalid. The bug this replaces: a `generate` template
// emitted `what: Docs-only change: …` — a scalar containing ": " is invalid
// YAML, and the lane's own committed record then failed to re-read.
func TestEmitStage_SchemaFirstRecord(t *testing.T) {
	wd := t.TempDir()
	schemaSrc := `#Rec: close({
	pr!: string, repo!: string, head!: string, generated!: string,
	oracle!: {class!: string, golden!: string, sha!: string, what!: string, drive!: string,
		files!: [...string], tests!: [...string],
		checks!: [...{id!: string, what!: string, assertion!: string, knownRed!: bool}]},
	control!: {ok!: bool, steps!: [...{id!: string, name!: string, status!: string}]},
	eval!: {verdict!: string, executed_checks!: int, steps!: [...{id!: string, name!: string, status!: string}]},
	gates!: {control_ok!: bool, executed_checks!: int, media_ok!: bool},
	validate!: {verdict!: string, security!: string, checklist!: [...string], findings!: [...string]},
	media!: {cast!: string, gif!: string, mjpeg!: string, mp4!: string, png!: string},
	cold_read!: {verdict!: string, suggestions!: [...string]},
	report!: string,
})`
	if err := os.WriteFile(filepath.Join(wd, "rec.cue"), []byte(schemaSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := map[string]any{
		"id":     "record",
		"kind":   "emit",
		"schema": "rec.cue",
		"value": map[string]any{
			"pr":        "7",
			"repo":      "omacom/omarchy",
			"head":      "deadbeef",
			"generated": "2026.259.0000",
			"oracle": map[string]any{
				"class":  "system",
				"golden": "check-omarchy-eval-base-inst:golden",
				"sha":    "deadbeef",
				// the colon-carrying scalar that broke the hand-rolled template
				"what":  "Docs-only change: clarifies the help text",
				"drive": "omarchy toggle bar --help 2>&1 | head -20",
				"files": []any{"bin/omarchy-toggle-bar"},
				"tests": []any{},
				"checks": []any{map[string]any{
					"id": "c1", "what": "the summary line", "assertion": "grep -q x /f", "knownRed": true,
				}},
			},
			"control": map[string]any{"ok": true, "steps": []any{map[string]any{"id": "behavior-1", "name": "n", "status": "ok"}}},
			"eval":    map[string]any{"verdict": "PASS", "executed_checks": 1, "steps": []any{map[string]any{"id": "behavior-1", "name": "n", "status": "ok"}}},
			"gates":   map[string]any{"control_ok": true, "executed_checks": 1, "media_ok": true},
			"validate": map[string]any{
				"verdict": "PASS", "security": "T1 PASS | T2 PASS | T3 PASS | T4 N/A",
				"checklist": []any{"PASS — security", "N/A — attribution: upstream"},
				"findings":  []any{},
			},
			"media":     map[string]any{"cast": "media/pr-7.cast", "gif": "media/pr-7.gif", "mjpeg": "media/pr-7.mjpeg", "mp4": "media/pr-7.mp4", "png": "media/pr-7.png"},
			"cold_read": map[string]any{"verdict": "PASS", "suggestions": []any{"re-run"}},
			"report":    "# I tested omacom/omarchy#7\n\n## Tests\nall good\n\n## Suggestions\n- re-run\n",
		},
		"out": wd + "/eval.yml",
	}
	l := newLedger()
	rc := &runCtx{pr: "7", calver: "2026.259.0000", workdir: wd, env: map[string]string{}, ledger: l}
	res, err := rc.runStage(nil, "emit", "record", stage, l)
	if err != nil {
		t.Fatalf("emit stage: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("emit status = %q, want ok", res.Status)
	}
	b, err := os.ReadFile(filepath.Join(wd, "eval.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("emitted record is not valid YAML: %v\n%s", err, b)
	}
	if got := doc["oracle"].(map[string]any)["what"]; got != "Docs-only change: clarifies the help text" {
		t.Fatalf("round-trip mismatch: %v", got)
	}
}

// TestEmitStage_RejectsSchemaViolation pins the fail-hard half: a value missing
// a required field (or carrying an unknown one) must FAIL the stage, never write
// a partial artifact.
func TestEmitStage_RejectsSchemaViolation(t *testing.T) {
	wd := t.TempDir()
	stage := map[string]any{
		"id":     "record",
		"kind":   "emit",
		"schema": "#EvalRecord",
		"value":  map[string]any{"pr": "7", "unknown_field": "x"},
		"out":    wd + "/eval.yml",
	}
	l := newLedger()
	rc := &runCtx{pr: "7", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "emit", "record", stage, l); err == nil {
		t.Fatal("emit with a schema-violating value: want an error, got nil")
	}
	if _, err := os.Stat(filepath.Join(wd, "eval.yml")); err == nil {
		t.Fatal("emit wrote an artifact despite a schema violation")
	}
}

// TestDumpLedger_SchemaFirst is the R10 coverage for the ledger-dump rewrite: a
// row whose message contains ": " and a multi-line message must round-trip
// through a parser. The former hand-rolled fmt.Fprintf writer was the same class
// as the free-form record template.
func TestDumpLedger_SchemaFirst(t *testing.T) {
	wd := t.TempDir()
	l := newLedger()
	l.put(&StageResult{
		ID: "gate", Kind: "probe", Status: "ok",
		Message: "verdict: PASS — multi\nline: message",
		Outputs: map[string]any{"executed_checks": 3},
	})
	l.put(&StageResult{ID: "eval", Kind: "ade", Status: "fail", Trigger: "setup-defect", Message: "boom: x"})
	path := filepath.Join(wd, "stage-findings.yml")
	if err := dumpLedger(l, path); err != nil {
		t.Fatalf("dumpLedger: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := yaml.Unmarshal(b, &rows); err != nil {
		t.Fatalf("ledger dump is not valid YAML: %v\n%s", err, b)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2\n%s", len(rows), b)
	}
	// dumpLedger sorts by stage id, so find the row by its stage name.
	var got string
	for _, r := range rows {
		if r["stage"] == "gate" {
			got, _ = r["message"].(string)
		}
	}
	if got != "verdict: PASS — multi\nline: message" {
		t.Fatalf("multiline colon message did not round-trip: %q", got)
	}
}

// TestEmitStage_RejectsUnresolvedMarker pins the hard-error contract: a ${name}
// that is neither a var nor a resolvable ref must FAIL the stage, never ship a
// literal marker (an invalid artifact by the exact class this stage removes).
func TestEmitStage_RejectsUnresolvedMarker(t *testing.T) {
	wd := t.TempDir()
	stage := map[string]any{
		"id":     "emit",
		"kind":   "emit",
		"schema": "#T: {body!: string}",
		"value":  map[string]any{"body": "hello ${nope}"},
		"out":    wd + "/t.yml",
	}
	l := newLedger()
	rc := &runCtx{workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "emit", "emit", stage, l); err == nil {
		t.Fatal("emit with an unresolved marker: want an error, got nil")
	}
}

// TestEmitStage_RejectsUnknownTransform pins the same contract for a bad
// transform, matching the generate grammar's documented hard error.
func TestEmitStage_RejectsUnknownTransform(t *testing.T) {
	wd := t.TempDir()
	stage := map[string]any{
		"id":     "emit",
		"kind":   "emit",
		"schema": "#T: {body!: string}",
		"vars":   map[string]any{"body": "x"},
		"value":  map[string]any{"body": "${body:bogus}"},
		"out":    wd + "/t.yml",
	}
	l := newLedger()
	rc := &runCtx{workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "emit", "emit", stage, l); err == nil {
		t.Fatal("emit with an unknown transform: want an error, got nil")
	}
}

// TestEmitStage_StringLeafTemplate pins the composition path: a prose field (the
// user-voice report) is assembled from vars with the SAME per-marker grammar as
// generate, but its result is a STRUCTURED string leaf — the CUE encoder quotes
// it and block-scalars it, so a colon in the prose can never break the YAML.
func TestEmitStage_StringLeafTemplate(t *testing.T) {
	wd := t.TempDir()
	stage := map[string]any{
		"id":     "emit",
		"kind":   "emit",
		"schema": "#Item: {report!: string}",
		"vars": map[string]any{
			"body":        "I tested it: it worked.",
			"suggestions": []any{"re-run", "add a test"},
		},
		"value": map[string]any{
			"report": "## Tests\n${body:indent}\n\n## Suggestions\n${suggestions:bullets}\n",
		},
		"out": wd + "/item.yml",
	}
	l := newLedger()
	rc := &runCtx{workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "emit", "emit", stage, l); err != nil {
		t.Fatalf("emit string-leaf template: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "item.yml"))
	var doc struct {
		Report string `yaml:"report"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("emitted YAML is invalid: %v\n%s", err, b)
	}
	if !strings.Contains(doc.Report, "I tested it: it worked.") || !strings.Contains(doc.Report, "- re-run") {
		t.Fatalf("string-leaf template did not render:\n%s", doc.Report)
	}
}

// TestEmitStage_LiteralSchema pins the self-contained path: an entity may ship
// its own CUE def as a literal source string, no plugin release required.
func TestEmitStage_LiteralSchema(t *testing.T) {
	wd := t.TempDir()
	stage := map[string]any{
		"id":     "emit",
		"kind":   "emit",
		"schema": "#Thing: {name!: string, n!: int}",
		"value":  map[string]any{"name": "a: b", "n": 3},
		"out":    wd + "/thing.yml",
	}
	l := newLedger()
	rc := &runCtx{workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "emit", "emit", stage, l); err != nil {
		t.Fatalf("emit with a literal schema: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "thing.yml"))
	if !strings.Contains(string(b), `name: 'a: b'`) && !strings.Contains(string(b), `name: "a: b"`) {
		t.Fatalf("literal-schema emit did not quote the colon scalar:\n%s", b)
	}
}
