package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"gopkg.in/yaml.v3"
)

// --- the per-marker render transform (${checks:negate} / ${checks:json}) ------
//
// ONE template must render BOTH the treatment bed and its negative-control twin
// into a single file: the `${checks}` marker renders the checks, `${checks:negate}`
// renders them negated, and `${checks:json}` renders the raw JSON (a record).

func TestGeneratePerMarkerNegate(t *testing.T) {
	wd := t.TempDir()
	checks := []any{
		map[string]any{"id": "c1", "what": "the file exists", "assertion": "test -f /usr/share/omarchy/x"},
	}
	stage := params.Stage{
		"id": "bed-render", "kind": "generate",
		"template": "plan:\n  ${checks}\ncontrol:\n  ${checks:negate}\n",
		"vars":     map[string]any{"checks": "@triage.checks"},
		"out":      wd + "/beds.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"checks": checks}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "bed-render", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "beds.yml"))
	got := string(b)
	if !strings.Contains(got, "command: 'test -f /usr/share/omarchy/x'") {
		t.Fatalf("treatment check not rendered:\n%s", got)
	}
	if !strings.Contains(got, "command: '! ( test -f /usr/share/omarchy/x )'") {
		t.Fatalf("negated control check not rendered:\n%s", got)
	}
	// the treatment must NOT be negated, and the control MUST be: the two blocks
	// differ by construction.
	if strings.Count(got, "! ( ") != 1 {
		t.Fatalf("expected exactly one negated block, got:\n%s", got)
	}
}

func TestGenerateMarkerJSON(t *testing.T) {
	wd := t.TempDir()
	checks := []any{map[string]any{"id": "c1", "what": "x", "assertion": "true", "knownRed": true}}
	stage := params.Stage{
		"id": "record", "kind": "generate",
		"template": "checks: ${checks:json}\n",
		"vars":     map[string]any{"checks": "@triage.checks"},
		"out":      wd + "/eval.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"checks": checks}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "record", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "eval.yml"))
	if !strings.Contains(string(b), `"assertion":"true"`) {
		t.Fatalf("json marker did not render the raw object:\n%s", string(b))
	}
}

// The transform stripping must not corrupt the block INDENT lookup: the marker
// with its `:negate` suffix is what sits on the template line, so a naive strip
// before indent lookup would match the wrong (stripped) token and mis-indent the
// continuation lines. This pins the indented nested case.
func TestGeneratePerMarkerNegate_IndentPreserved(t *testing.T) {
	wd := t.TempDir()
	checks := []any{map[string]any{"id": "c1", "what": "w", "assertion": "test -f /x"}}
	stage := params.Stage{
		"id": "g", "kind": "generate",
		"template": "outer:\n  inner:\n    plan:\n      ${checks:negate}\n",
		"vars":     map[string]any{"checks": "@triage.checks"},
		"out":      wd + "/o.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"checks": checks}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "g", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "o.yml"))
	// the continuation `id:` line must be indented under `- check:` at the
	// marker's indent (6 spaces) + 2 for the list item's fields.
	if !strings.Contains(string(b), "\n        id: behavior-1\n") {
		t.Fatalf("continuation not indented to the marker:\n%s", string(b))
	}
	if !strings.Contains(string(b), "command: '! ( test -f /x )'") {
		t.Fatalf("negate transform lost:\n%s", string(b))
	}
}

func TestGenerateMarkerYAML(t *testing.T) {
	wd := t.TempDir()
	steps := []any{map[string]any{"id": "pr-apply", "name": "apply PR", "status": "ok"}}
	stage := params.Stage{
		"id": "record", "kind": "generate",
		"template": "eval_steps:\n  ${steps:yaml}\n",
		"vars":     map[string]any{"steps": "@gate.eval_steps"},
		"out":      wd + "/record.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "gate", Kind: "probe", Status: "ok", Outputs: map[string]any{"eval_steps": steps}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "record", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "record.yml"))
	// discriminator: `:yaml` must render a BLOCK list (JSON would also parse,
	// so round-tripping alone does not prove the transform ran).
	if strings.Contains(string(b), `[{"`) || strings.Contains(string(b), `{"id"`) {
		t.Fatalf(":yaml fell through to scalar JSON instead of a YAML block:\n%s", string(b))
	}
	var doc struct {
		EvalSteps []struct {
			ID     string `yaml:"id"`
			Status string `yaml:"status"`
		} `yaml:"eval_steps"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("rendered :yaml is not valid YAML: %v\n%s", err, string(b))
	}
	if len(doc.EvalSteps) != 1 || doc.EvalSteps[0].ID != "pr-apply" || doc.EvalSteps[0].Status != "ok" {
		t.Fatalf("the structured rows did not round-trip: %+v", doc.EvalSteps)
	}
}

func TestGenerateMarkerIndent(t *testing.T) {
	wd := t.TempDir()
	body := "line one\nline two\n\nline four"
	stage := params.Stage{
		"id": "record", "kind": "generate",
		"template": "report: |\n  ${body:indent}\n",
		"vars":     map[string]any{"body": "@report.tests"},
		"out":      wd + "/record.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "report", Kind: "agent", Status: "ok", Outputs: map[string]any{"tests": body}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "record", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "record.yml"))
	var doc struct {
		Report string `yaml:"report"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("rendered block scalar is not valid YAML: %v\n%s", err, string(b))
	}
	if doc.Report != body+"\n" {
		// the YAML `|` literal block keeps its final newline — that is the
		// contract, not a loss of structure.
		t.Fatalf("the block scalar lost its structure:\n%q\nwant\n%q", doc.Report, body+"\n")
	}
}

func TestGenerateMarkerBullets(t *testing.T) {
	wd := t.TempDir()
	sugg := []any{"do X", "fix Y"}
	stage := params.Stage{
		"id": "record", "kind": "generate",
		"template": "report: |\n  ## Suggestions\n  ${s:bullets}\n",
		"vars":     map[string]any{"s": "@cold.suggestions"},
		"out":      wd + "/record.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "cold", Kind: "agent", Status: "ok", Outputs: map[string]any{"suggestions": sugg}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "record", stage, l); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(wd, "record.yml"))
	var doc struct {
		Report string `yaml:"report"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, string(b))
	}
	if !strings.Contains(doc.Report, "- do X\n") || !strings.Contains(doc.Report, "- fix Y") {
		t.Fatalf("bullets not rendered:\n%s", doc.Report)
	}
}

// An UNKNOWN transform must hard-error, never silently render the unchanged
// value (a `:negte` typo would otherwise make the control an exact copy of the
// treatment — a vacuous assertion-integrity proof).
func TestGenerateUnknownTransformErrors(t *testing.T) {
	wd := t.TempDir()
	checks := []any{map[string]any{"id": "c1", "what": "w", "assertion": "test -f /x"}}
	stage := params.Stage{
		"id": "g", "kind": "generate",
		"template": "plan:\n  ${checks:negte}\n",
		"vars":     map[string]any{"checks": "@triage.checks"},
		"out":      wd + "/o.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"checks": checks}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "g", stage, l); err == nil {
		t.Fatal("an unknown marker transform must error, not render a silent no-op")
	}
}

// :negate on a non-list marker has no negation semantics — it must error.
func TestGenerateNegateOnNonListErrors(t *testing.T) {
	wd := t.TempDir()
	stage := params.Stage{
		"id": "g", "kind": "generate",
		"template": "x: ${v:negate}\n",
		"vars":     map[string]any{"v": "@triage.what"},
		"out":      wd + "/o.yml",
	}
	l := newLedger()
	l.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{"what": "a string"}})
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{}, ledger: l}
	if _, err := rc.runStage(nil, "generate", "g", stage, l); err == nil {
		t.Fatal(":negate on a non-list must error")
	}
}

// --- the agent-stage committed-plan cache ------------------------------------
//
// On a freshness hit (the committed plan exists and its head equals the run's
// head) the declared outputs are READ and the agent NEVER runs; a new head is a
// miss that re-authors. The LLM is pointed at a server that fails the test if it
// is ever called, so a hit proves the agent was skipped.

func TestAgentCacheHitSkipsAgent(t *testing.T) {
	wd := t.TempDir()
	plan := "head: abc123\nclass: system\nchannel: edge\ngolden: check-omarchy-eval-edge-inst\nchecks:\n  - {id: c1, what: x, assertion: 'true', knownRed: true}\n"
	if err := os.WriteFile(filepath.Join(wd, "eval.yml"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := params.Stage{
		"id": "oracle", "kind": "agent", "prompt": "unused",
		"outputs": map[string]any{
			"class":   map[string]any{"type": "string"},
			"channel": map[string]any{"type": "string"},
			"golden":  map[string]any{"type": "string"},
			"checks":  map[string]any{"type": "object"},
		},
		"cache": map[string]any{"path": "eval.yml", "key": "$env.PR_HEAD_SHA"},
	}
	t.Setenv("EVAL_LLM_BASE_URL", "http://127.0.0.1:1") // a hit must never dial
	t.Setenv("PR_HEAD_SHA", "abc123")
	l := newLedger()
	rc := &runCtx{pr: "7", calver: "c", workdir: wd, env: map[string]string{"PR_HEAD_SHA": "abc123"}, ledger: l}
	res, err := rc.runStage(nil, "agent", "oracle", stage, l)
	if err != nil || res.Status != "ok" {
		t.Fatalf("agent cache hit: %v %v", res, err)
	}
	if res.Outputs["cache_hit"] != true {
		t.Fatalf("expected a cache hit, got %v", res.Outputs)
	}
	if res.Outputs["golden"] != "check-omarchy-eval-edge-inst" {
		t.Fatalf("the cached golden was not surfaced: %v", res.Outputs["golden"])
	}
	if res.Outputs["checks"] == nil {
		t.Fatalf("the cached checks were not surfaced: %v", res.Outputs)
	}
}

func TestAgentCacheMissOnNewHead(t *testing.T) {
	wd := t.TempDir()
	plan := "head: old999\nclass: system\n"
	if err := os.WriteFile(filepath.Join(wd, "eval.yml"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	// a miss: readAgentCache must report hit=false (the agent then runs, which
	// this test does not exercise — it asserts the cache decision alone).
	rc := &runCtx{pr: "7", workdir: wd, env: map[string]string{"PR_HEAD_SHA": "new111"}}
	stage := map[string]any{"cache": map[string]any{"path": "eval.yml", "key": "$env.PR_HEAD_SHA"}}
	_, hit, err := readAgentCache(rc, stage)
	if err != nil || hit {
		t.Fatalf("stale head must MISS: hit=%v err=%v", hit, err)
	}
	// and a missing file is a miss, not an error.
	rc2 := &runCtx{pr: "8", workdir: t.TempDir(), env: map[string]string{"PR_HEAD_SHA": "x"}}
	_, hit2, err2 := readAgentCache(rc2, stage)
	if err2 != nil || hit2 {
		t.Fatalf("missing plan must MISS cleanly: hit=%v err=%v", hit2, err2)
	}
}

// A cache hit whose committed plan MISSES a declared output must ERROR — never
// return ok with the output silently absent (which would render an empty bed var).
func TestAgentCacheHitMissingOutputErrors(t *testing.T) {
	wd := t.TempDir()
	// valid YAML, matching head, but no `golden` field.
	if err := os.WriteFile(filepath.Join(wd, "eval.yml"), []byte("head: abc123\nclass: system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := map[string]any{
		"cache": map[string]any{"path": "eval.yml", "key": "$env.PR_HEAD_SHA"},
		"outputs": map[string]any{
			"class":  map[string]any{"type": "string"},
			"golden": map[string]any{"type": "string"},
		},
	}
	rc := &runCtx{pr: "7", workdir: wd, env: map[string]string{"PR_HEAD_SHA": "abc123"}}
	_, hit, err := readAgentCache(rc, stage)
	if err == nil || hit {
		t.Fatalf("a partial committed plan must error (hit=%v err=%v)", hit, err)
	}
}

// A cache hit whose committed value violates its declared #OutputType must ERROR.
func TestAgentCacheHitBadTypeErrors(t *testing.T) {
	wd := t.TempDir()
	// `tests` is declared string_list but the plan carries a bare string.
	if err := os.WriteFile(filepath.Join(wd, "eval.yml"), []byte("head: abc123\ntests: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := map[string]any{
		"cache":   map[string]any{"path": "eval.yml", "key": "$env.PR_HEAD_SHA"},
		"outputs": map[string]any{"tests": map[string]any{"type": "string_list"}},
	}
	rc := &runCtx{pr: "7", workdir: wd, env: map[string]string{"PR_HEAD_SHA": "abc123"}}
	if _, hit, err := readAgentCache(rc, stage); err == nil || hit {
		t.Fatalf("a bad-typed cached value must error (hit=%v err=%v)", hit, err)
	}
}

// A nil run context with a cache block must MISS cleanly, never panic.
func TestAgentCacheNilCtxMisses(t *testing.T) {
	stage := map[string]any{"cache": map[string]any{"path": "eval.yml", "key": "$env.PR_HEAD_SHA"}}
	if _, hit, err := readAgentCache(nil, stage); err != nil || hit {
		t.Fatalf("nil rc must miss cleanly (hit=%v err=%v)", hit, err)
	}
}

// --- the ledger_gate media-min single source ---------------------------------

func TestMediaGateSpecUsesPipelineMin(t *testing.T) {
	rc := &runCtx{media: map[string]any{
		"files": []any{"cast", "mp4"},
		"min":   map[string]any{"cast": 7, "mp4": 9},
	}}
	files, mins := rc.mediaGateSpec()
	if len(files) != 2 || files[0] != "cast" || files[1] != "mp4" {
		t.Fatalf("files = %v", files)
	}
	if mins["cast"] != 7 || mins["mp4"] != 9 {
		t.Fatalf("mins = %v (the pipeline media.min must win)", mins)
	}
	// no run context: the built-in defaults still cover every file, with the
	// EXACT engine values (mp4 4096 pinned so a silent loosening cannot recur).
	defFiles, defMins := (&runCtx{}).mediaGateSpec()
	if len(defFiles) != 5 || defMins["mp4"] == nil {
		t.Fatalf("defaults = %v %v", defFiles, defMins)
	}
	if defMins["mp4"] != 4096 || defMins["cast"] != 200 || defMins["gif"] != 1024 || defMins["mjpeg"] != 4096 || defMins["png"] != 1024 {
		t.Fatalf("engine default min sizes changed: %v", defMins)
	}
}
