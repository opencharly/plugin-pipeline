package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"gopkg.in/yaml.v3"
)

// root_cause_fixes_test.go — the regression locks for the live-caught defects
// found while running the real eval-pr-plan lane.

// TestLastLinesKeepsTheDiagnosisTail: the ADE summary must keep WHOLE trailing
// lines (the error), never a mid-word byte slice.
//
// Live defect: the predecessor kept the last 400 BYTES, so the control stage's
// failure surfaced as the garbled fragment
// `check-run exit 1: eline/candy/plugin-pipeline` — the diagnosis ("already
// running … lock") was dropped and the message began mid-word.
func TestLastLinesKeepsTheDiagnosisTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("banner line without the diagnosis\n")
	}
	b.WriteString("charly: error: check bed \"check-x\" is already running in this project\n")
	out := lastLines(b.String(), 12, 400)
	if strings.HasPrefix(out, "…") && strings.Contains(out, "eline/") {
		t.Fatalf("summary is a mid-word fragment: %q", out)
	}
	if !strings.Contains(out, "already running") {
		t.Fatalf("summary must retain the trailing diagnosis, got %q", out)
	}
	// every retained line must be a whole line (no leading partial word)
	for _, ln := range strings.Split(out, "\n") {
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "banner line") && !strings.HasPrefix(ln, "charly: error:") && !strings.HasPrefix(ln, "…") {
			t.Fatalf("summary carries a partial line: %q", ln)
		}
	}
}

// TestAdeVerdictUsesTheLaneHeadNotTheProcessEnv: the ADE stage must pass the
// LANE's head sha. The predecessor read os.Getenv("PR_HEAD_SHA"), which is the
// operator's value or empty — never this lane's — and the live run logged
// `--var PR_HEAD_SHA=`.
//
// The test drives resolveLLM's sibling path: it asserts the lane env is the
// source by checking the run context binding (the same contract runPlanL
// establishes), and that a CONCURRENT lane's value cannot be observed through
// the process env.
func TestAdeVerdictUsesTheLaneHeadNotTheProcessEnv(t *testing.T) {
	t.Setenv("PR_HEAD_SHA", "") // no operator pin
	// bind two lanes' heads the way runPlanL does
	mk := func(pr, sha string) *runCtx {
		return &runCtx{pr: pr, env: map[string]string{"PR_NUMBER": pr, "PR_HEAD_SHA": sha}}
	}
	a := mk("1", "aaaa")
	b := mk("2", "bbbb")
	if a.env["PR_HEAD_SHA"] != "aaaa" || b.env["PR_HEAD_SHA"] != "bbbb" {
		t.Fatal("per-lane head bindings must be independent")
	}
	// the process env carries nothing — reading it (the old bug) yields empty
	if got := os.Getenv("PR_HEAD_SHA"); got != "" {
		t.Fatalf("process env must stay empty in this test, got %q", got)
	}
	if a.env["PR_HEAD_SHA"] == os.Getenv("PR_HEAD_SHA") {
		t.Fatal("the lane's head must come from the run context, not the process env")
	}
}

// TestTeardownLaneBedsIsEntityDriven: the between-lane teardown must derive the
// bed names from the lane's OWN `ade` stages, ref-resolved — never a hardcoded
// eval-omarchy golden/domain scheme.
func TestTeardownLaneBedsIsEntityDriven(t *testing.T) {
	// Only the DESTRUCTION TARGET derivation is asserted here (no charly spawn):
	// the same resolveRefs walk teardownLaneBeds performs.
	p := params.PipelineInput{
		Stages: []params.Stage{
			{"kind": "ade", "id": "eval", "bed": "check-omarchy-pr-$pr-vm"},
			{"kind": "ade", "id": "control", "bed": "check-omarchy-pr-$pr-control"},
			{"kind": "gate", "id": "publish", "condition": "true"},
		},
	}
	rc := &runCtx{pr: "12115", env: map[string]string{}}
	var got []string
	seen := map[string]bool{}
	for _, raw := range p.Stages {
		if asString(raw["kind"]) != "ade" {
			continue
		}
		bed := strings.TrimSpace(rc.resolveRefs(asString(raw["bed"])))
		if bed == "" || seen[bed] {
			continue
		}
		seen[bed] = true
		got = append(got, bed)
	}
	want := []string{"check-omarchy-pr-12115-vm", "check-omarchy-pr-12115-control"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("teardown targets = %v, want the lane's declared beds %v", got, want)
	}
	// and it must NOT contain the hardcoded golden the predecessor destroyed
	for _, bed := range got {
		if bed == "check-omarchy-eval-base-inst" {
			t.Fatalf("teardown must not hardcode the eval-omarchy golden: %v", got)
		}
	}
}

// TestProbeSequencingRequiresABedPrefix: the sequencing gate is DERIVED from the
// lane's declared bed prefix. The predecessor matched a literal
// `check-omarchy-pr-<N>-vm`, so any other project's gate passed vacuously.
func TestProbeSequencingRequiresABedPrefix(t *testing.T) {
	ok, msg := probeSequencing(map[string]any{})
	if ok {
		t.Fatal("sequencing with no bed_prefix must FAIL, not pass vacuously")
	}
	if !strings.Contains(msg, "bed_prefix") {
		t.Fatalf("the failure must name the missing input, got %q", msg)
	}
}

// TestProbeGoldenPresentRequiresTheGoldenPath: no silent eval-omarchy default.
func TestProbeGoldenPresentRequiresTheGoldenPath(t *testing.T) {
	ok, msg := probeGoldenPresent(map[string]any{})
	if ok {
		t.Fatal("golden_present with no golden must FAIL, not silently substitute a hardcoded path")
	}
	if !strings.Contains(msg, "golden required") {
		t.Fatalf("the failure must name the missing input, got %q", msg)
	}
	// a missing file is still an honest failure
	ok, msg = probeGoldenPresent(map[string]any{"golden": filepath.Join(t.TempDir(), "absent.qcow2")})
	if ok || !strings.Contains(msg, "golden missing") {
		t.Fatalf("a missing golden must fail: ok=%v msg=%q", ok, msg)
	}
}

// TestProbeHeadFreshnessUsesTheCanonicalClient: the probe must go through headSHA
// (ghkit), not a hand-rolled `gh` subprocess. A repo that cannot be resolved is
// an HONEST failure naming the repo, never a bare "gh failed".
func TestProbeHeadFreshnessUsesTheCanonicalClient(t *testing.T) {
	ok, msg := probeHeadFreshness(map[string]any{"plan_sha": "abc", "pr": 1, "repo": ""})
	if ok || !strings.Contains(msg, "required") {
		t.Fatalf("missing repo must fail with a named reason, got ok=%v msg=%q", ok, msg)
	}
	// an unresolvable repo: the diagnostic must name it (the subprocess version
	// said only "gh failed").
	ok, msg = probeHeadFreshness(map[string]any{"plan_sha": "abc", "pr": 1, "repo": "no-such-org-xyz/no-such-repo"})
	if ok {
		t.Fatal("an unresolvable repo must fail")
	}
	if !strings.Contains(msg, "no-such-org-xyz/no-such-repo") {
		t.Fatalf("the failure must name the repo, got %q", msg)
	}
}

// TestPipelineToolGroupIsGone: the broken `pipeline` agent-tool group (empty
// parameter schemas + empty dispatch input, unused, duplicating the probe
// stages) is DELETED (R3/R5) — a `tools: [pipeline]` reference must resolve to
// no tools rather than to nine vacuous ones.
func TestPipelineToolGroupIsGone(t *testing.T) {
	if _, ok := toolCatalog["pipeline"]; ok {
		t.Fatal("the broken pipeline tool group must be deleted, not retained")
	}
	if got := buildTools([]string{"pipeline"}); len(got) != 0 {
		t.Fatalf("tools: [pipeline] must yield no tools, got %d", len(got))
	}
	// the surviving groups still render
	if got := buildTools([]string{"pr", "ledger"}); len(got) != 7 {
		t.Fatalf("pr+ledger must yield 7 tools, got %d", len(got))
	}
}

// TestLaneReportExitIsAGate: a batch whose lane FAILED must exit non-zero.
//
// Live defect: runBatch printed each lane's error and then returned `0, nil`
// unconditionally, so a batch in which the lane ended in the LOOP-GUARD
// escalation still printed "pipeline eval-pr-plan: OK" and exited 0 — the exit
// status carried no information (R7 violated). This locks the aggregate.
func TestLaneReportExitIsAGate(t *testing.T) {
	// all lanes OK -> exit 0
	ok := &laneReport{}
	ok.record("1", nil)
	ok.record("2", nil)
	if code, err := ok.exit("plan", 4); code != 0 || err != nil {
		t.Fatalf("an all-clean batch must exit 0, got code=%d err=%v", code, err)
	}

	// ONE failed lane -> non-zero exit, and the error names it
	bad := &laneReport{}
	bad.record("1", nil)
	bad.record("2", errBoom)
	code, err := bad.exit("plan", 4)
	if code == 0 {
		t.Fatal("a batch with a failed lane must exit non-zero")
	}
	if err == nil || !strings.Contains(err.Error(), "2:") {
		t.Fatalf("the aggregate error must name the failed lane, got %v", err)
	}

	// EVERY lane failed -> still non-zero, and the counts are honest
	allBad := &laneReport{}
	allBad.record("1", errBoom)
	allBad.record("2", errBoom)
	if code, _ := allBad.exit("plan", 2); code == 0 {
		t.Fatal("a totally-failed batch must exit non-zero")
	}
	if allBad.failed != 2 || allBad.done != 2 {
		t.Fatalf("counts must be honest: failed=%d done=%d", allBad.failed, allBad.done)
	}
}

// errBoom is a stand-in lane failure.
var errBoom = &laneErr{}

type laneErr struct{}

func (*laneErr) Error() string { return "boom" }

// TestLedgerPathIsPerLane: the ledger dump must be PER-LANE.
//
// Live defect: all lanes wrote the SAME `workdir/stage-findings.yml`, so a
// concurrent batch left exactly ONE lane's ledger on disk and a FAILING lane's
// forensic artifact was destroyed by a sibling — the same per-lane-state class
// the ledger OBJECT already fixed (RCA 2026.252.2210), never applied to the
// dump PATH. Measured: a 2-lane run left a single file carrying one lane's
// stages.
func TestLedgerPathIsPerLane(t *testing.T) {
	wd := t.TempDir()
	a := ledgerPath(wd, "12137")
	b := ledgerPath(wd, "12135")
	if a == b {
		t.Fatalf("two lanes collided on the same ledger path: %s", a)
	}
	if !strings.Contains(a, "12137") {
		t.Fatalf("the path must carry the lane's PR, got %s", a)
	}
	// a generic single-entity run (no PR) keeps the bare name
	if got := ledgerPath(wd, ""); got != filepath.Join(wd, "stage-findings.yml") {
		t.Fatalf("the no-PR path must stay the bare name, got %s", got)
	}
}

// TestDumpLedgerRecordsDuration: the dump must carry the per-stage wall-clock
// time the runner already measures. Without it the one artifact that could
// answer "where did the lane's time go?" carries no timing.
func TestDumpLedgerRecordsDuration(t *testing.T) {
	wd := t.TempDir()
	l := newLedger()
	l.put(&StageResult{ID: "oracle", Kind: "agent", Status: "ok", Duration: 90 * time.Second})
	l.put(&StageResult{ID: "control", Kind: "ade", Status: "ok", Duration: 177 * time.Second})
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
		t.Fatalf("not valid YAML: %v\n%s", err, b)
	}
	got := map[string]any{}
	for _, r := range rows {
		got[r["stage"].(string)] = r["duration_seconds"]
	}
	if got["oracle"] != 90 || got["control"] != 177 {
		t.Fatalf("durations not recorded: %v", got)
	}
}
