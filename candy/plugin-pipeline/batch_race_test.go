package pluginpipeline

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// TestBatchLaneIsolation is the regression lock for RCA 2026.252.2210: the
// batch mode ran N lanes in ONE process, and the per-lane state (the stage
// ledger + the PR identity) was carried in PACKAGE GLOBALS (curLedger) and
// os.Setenv — every lane resolved the last writer's triage outputs and PR.
// The lanes now own their runCtx (ledger + env identity); two concurrent
// lanes with DIFFERENT PRs must resolve only their own state.
func TestBatchLaneIsolation(t *testing.T) {
	mk := func(pr, files string) *runCtx {
		l := newLedger()
		rc := &runCtx{pr: pr, calver: "2026.1.1", workdir: t.TempDir(), env: map[string]string{}, ledger: l}
		rc.env["PR_NUMBER"] = pr
		rc.ledger.put(&StageResult{ID: "triage", Kind: "agent", Status: "ok", Outputs: map[string]any{
			"plan-json": map[string]any{"files": files},
		}})
		return rc
	}
	a := mk("101", "a.qml")
	b := mk("202", "b.qml")

	var wg sync.WaitGroup
	got := make([]string, 2)
	wg.Add(2)
	go func() { defer wg.Done(); got[0] = a.resolveRefs("@triage.plan-json.files $env.PR_NUMBER") }()
	go func() { defer wg.Done(); got[1] = b.resolveRefs("@triage.plan-json.files $env.PR_NUMBER") }()
	wg.Wait()

	if got[0] != "a.qml 101" {
		t.Errorf("lane A resolved %q, want its OWN triage files + PR (cross-lane state leak)", got[0])
	}
	if got[1] != "b.qml 202" {
		t.Errorf("lane B resolved %q, want its OWN triage files + PR (cross-lane state leak)", got[1])
	}
}

// TestRunPlanL_BindsLaneEnv drives the REAL run path end to end: a run whose
// generate stage renders $env.PR_NUMBER must see the LANE's PR, not the
// process env (the batch runner used to Setenv per lane — process-global,
// raced). An operator-pinned PR_HEAD_SHA still wins over the headSHA lookup.
func TestRunPlanL_BindsLaneEnv(t *testing.T) {
	os.Setenv("PR_NUMBER", "888") // a stale/raced process env must NOT leak
	defer os.Unsetenv("PR_NUMBER")
	os.Setenv("EVAL_REPO", "omacom/omarchy")
	defer os.Unsetenv("EVAL_REPO")

	wd := t.TempDir()
	p := params.PipelineInput{
		Stages: []params.Stage{
			{"kind": "generate", "id": "render", "template": "pr=$env.PR_NUMBER", "out": wd + "/out.md"},
		},
	}
	l := newLedger()
	if err := runPlanL(context.Background(), p, "77", "2026.1.1", wd, nil, l); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(wd, "out.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "pr=77" {
		t.Fatalf("the render saw %q, want the LANE's PR 77 (the process env leaked)", string(b))
	}
}

// TestPRToolRef_LaneIdentity: the pr tools must take the PR from the RUN
// CONTEXT (the lane's own identity), never from the process env the batch
// lanes raced. The env is the CLI fallback only.
func TestPRToolRef_LaneIdentity(t *testing.T) {
	os.Setenv("PR_NUMBER", "999")
	defer os.Unsetenv("PR_NUMBER")

	rc := &runCtx{pr: "101", repo: "omacom/omarchy", env: map[string]string{}}
	os.Unsetenv("EVAL_REPO") // the executor does not forward the shell env — the entity must self-contain
	os.Unsetenv("GITHUB_REPOSITORY")
	if pr, repo := prRef(rc); pr != "101" {
		t.Errorf("prRef with a lane identity = %q, want 101 (the env fallback leaked)", pr)
	} else if repo != "omacom/omarchy" {
		t.Errorf("prRef repo = %q, want the ENTITY-authored repo (the env never reaches the executor subprocess)", repo)
	}

	rcEnv := &runCtx{pr: "", env: map[string]string{}}
	if pr, _ := prRef(rcEnv); pr != "999" {
		t.Errorf("prRef without a lane identity = %q, want the env fallback 999", pr)
	}
}
