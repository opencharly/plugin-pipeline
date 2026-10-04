package pluginpipeline

import (
	"context"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_ade.go — the lifted `ade` verb. The plan stage's ADE dispatch MOVED here
// from the retired in-process executor when the stage kind became a reachable verb: it is
// registered as {Class: "verb", Word: "ade", InputDef: "#PipelineAdeInput"} and
// dispatched by Invoke (a
// `<word>: <input>` step in any plan). The ADE machinery (runAdeBedKit,
// adeVerdict, the bed-entity lookup) stays in ade.go/adekit.go. ONE body, one caller (R3).

// runVerbAde is the verb handler: the {verdict, summary} outputs plus, for a
// declared fail_on verdict, a *verbFail carrying the setup-defect TRIGGER — a
// lane defect that restarts the chain with NO error (never fail-hard).
func runVerbAde(in params.PipelineAdeInput, e *verbEnv) (map[string]any, error) {
	return runAdeStage(e.ctxOf(), e, e.stageRaw(in))
}

// adeLaneHead: the lane's head sha for the external check-run fallback.
//
// It is a MAP READ of the run context (`rc.env["PR_HEAD_SHA"]`), never
// `resolveRefs("$env.PR_HEAD_SHA")`. refs.go does carry the `$env.X` grammar that
// reads this key, but that grammar is DEAD on this path: a verb reached standalone
// is wired to the IDENTITY resolver (verb_env.go:standaloneVerbEnv — the caller's
// plan already owns ref resolution), and that is the only production `verbEnv`
// constructor in the package. So `resolveRefs` hands back the LITERAL
// `$env.PR_HEAD_SHA`, and the fallback would render
// `--var PR_HEAD_SHA=$env.PR_HEAD_SHA` — a string that LOOKS like a head and is
// therefore carried into the bed's evidence as one.
//
// The predecessor read os.Getenv("PR_HEAD_SHA"), the process-global the concurrent
// lanes share, and the live lane logged `--var PR_HEAD_SHA=` while its run context
// carried a real sha (RCA 2026.252.2210). The retired in-process executor instead
// BOUND this key per run (headSHA(pr, repo), an operator pin winning); this method
// is that same lookup, restored.
//
// A standalone dispatch carries no per-run map, so the read yields "": the honest
// signal that no lane head reached the fallback. A missing run context is honest;
// a literal ref is a wrong answer that looks like a real one.
func (e *verbEnv) adeLaneHead() string {
	if e == nil {
		return ""
	}
	return e.env["PR_HEAD_SHA"]
}

// runAdeStage runs the rendered ORACLE bed through the host's compiled-in
// check-run ONCE (the org R10 machinery + its ADE agent-check grading + the
// --var per-PR passthrough). The deterministic exit contract maps to the report
// verdict. (RCA: the external CLI dispatch has no reverse-channel executor — see
// ade.go header.)
func runAdeStage(ctx context.Context, rc *verbEnv, raw map[string]any) (map[string]any, error) {
	var verdict, summary string
	var aerr error
	// the DECLARED bed (resolved) — the control bed runs the same ADE
	// machinery on its own entity; the hardcoded -vm name is gone.
	bed := rc.resolveRefs(asString(raw["bed"]))
	if rc.ex != nil {
		// the compiled-in placement: drive the bed's plan in-process (no charly spawn)
		verdict, summary, _, aerr = runAdeBedKit(ctx, rc.pr, bed, rc.workdir, rc.ex)
	} else {
		// the un-compiled placement: the external charly check-run fallback.
		// The lane's head comes from the run context, NEVER the process env —
		// the batch lanes are concurrent and the process env carries the
		// operator's value or nothing (RCA: the live lane logged
		// `--var PR_HEAD_SHA=`).
		verdict, summary, aerr = adeVerdict(ctx, rc.pr, rc.adeLaneHead(), bed, rc.workdir)
	}
	if aerr != nil {
		return nil, aerr
	}
	outputs := map[string]any{"verdict": verdict, "summary": summary}
	// fail_on: the declared verdicts are LANE DEFECTS, not eval outcomes —
	// the stage fails with the redo trigger (the SETUP_DEFECT path) instead
	// of flowing a worthless verdict downstream. The verdict is a GATE now.
	for _, fv := range strList(raw["fail_on"]) {
		if verdict == fv {
			return outputs, &verbFail{msg: summary, trigger: "setup-defect"}
		}
	}
	return outputs, nil
}
