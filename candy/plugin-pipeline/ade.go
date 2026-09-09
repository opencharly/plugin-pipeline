package pluginpipeline

// ade.go — the org-wide ADE evaluation stage. The rendered evaluation bed's plan
// runs against the LIVE VENUE through the SDK's single dial (InvokeProvider), the
// same dispatch the org's check engine uses — no exec'd charly binary, no custom
// bed contracts. The bed's `agent-check:` prose steps are graded by the pipeline's
// agent runtime (the ADE contract); the deterministic verb steps (command/record/
// spice/run) dispatch to their providers. The stage's verdict (PASS/FAIL) drives
// the report's frontmatter.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// adeVerdict evaluates the rendered bed's plan in the venue: dispatch the verb
// steps via InvokeProvider; grade the agent-check steps with the agent runtime.
// Returns the deterministic verdict + the per-step summary.
func adeVerdict(ctx context.Context, ex *sdk.Executor, bedAbs string, rc *runCtx) (string, string, error) {
	steps, err := bedPlanSteps(bedAbs)
	if err != nil {
		return "NO_VALIDATION", "", err
	}
	if len(steps) == 0 {
		return "NO_VALIDATION", "bed has no plan steps", nil
	}
	failed := 0
	var summaries []string
	for _, s := range steps {
		kind := strings.TrimSpace(s["kind"])
		id := strings.TrimSpace(s["id"])
		if id == "" {
			id = fmt.Sprintf("step-%d", len(summaries)+1)
		}
		switch kind {
		case "agent-check":
			prose := strings.TrimSpace(s["check"])
			if prose == "" {
				prose = strings.TrimSpace(s["text"])
			}
			// the ADE: the live agent grades the step's prose against the venue.
			resp, aerr := runAgent(ctx, adeGraderPrompt(prose), "Grade this check against the venue evidence.", []string{"pr", "ledger"})
			if aerr != nil {
				failed++
				summaries = append(summaries, id+": agent-error "+aerr.Error())
				continue
			}
			if strings.Contains(resp, "\"pass\"") || strings.Contains(resp, "PASS") {
				summaries = append(summaries, id+": pass")
			} else {
				failed++
				summaries = append(summaries, id+": fail ("+clip(resp, 120)+")")
			}
		default:
			// verb steps (command/record/spice/run...) dispatch over the dial.
			res, ok := dispatchStep(ctx, ex, s, rc)
			if !ok {
				failed++
				summaries = append(summaries, id+": fail ("+clip(res.Message, 160)+")")
				continue
			}
			if res.Status == spec.StatusPass {
				summaries = append(summaries, id+": pass")
			} else {
				failed++
				summaries = append(summaries, id+": fail ("+clip(res.Message, 160)+")")
			}
		}
	}
	verdict := "PASS"
	if failed > 0 {
		verdict = "FAIL"
	}
	return verdict, strings.Join(summaries, "\n"), nil
}

// bedPlanSteps parses the rendered bed file and returns the eval bed's plan steps.
func bedPlanSteps(bedAbs string) ([]map[string]string, error) {
	raw, err := os.ReadFile(bedAbs)
	if err != nil {
		return nil, fmt.Errorf("ade: read bed: %w", err)
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("ade: parse bed: %w", err)
	}
	var m map[string]yaml.Node
	for name, n := range doc {
		if strings.HasPrefix(name, "check-omarchy-pr-") && strings.HasSuffix(name, "-vm") {
			_ = n.Decode(&m)
			break
		}
	}
	vm := m["vm"]
	var vmm map[string]yaml.Node
	_ = vm.Decode(&vmm)
	plan := vmm["plan"]
	var planNodes []yaml.Node
	_ = plan.Decode(&planNodes)
	var out []map[string]string
	for _, pn := range planNodes {
		var pm map[string]yaml.Node
		if err := pn.Decode(&pm); err != nil {
			continue
		}
		step := map[string]string{}
		for k, v := range pm {
			step[k] = strings.TrimSpace(v.Value)
		}
		if _, ok := step["kind"]; !ok {
			// the plan steps use the keyword position (check:/run:/agent-check:)
			if _, hasCheck := step["check"]; hasCheck {
				step["kind"] = "check"
			}
			if _, hasRun := step["run"]; hasRun {
				step["kind"] = "run"
			}
			if _, hasAC := step["agent-check"]; hasAC {
				step["kind"] = "agent-check"
			}
		}
		if k := step["kind"]; k != "" {
			out = append(out, step)
		}
	}
	return out, nil
}

// dispatchStep: invoke a check verb over the plugin's single dial (the org's
// mechanism — mirrors plugin-check's RunVerb + the venue descriptor threading).
func dispatchStep(ctx context.Context, ex *sdk.Executor, step map[string]string, rc *runCtx) (spec.CheckResult, bool) {
	if ex == nil {
		return spec.CheckResult{Status: spec.StatusFail, Message: "no host executor (ADE dispatch needs the dial)"}, true
	}
	word := step["verb"]
	if word == "" {
		// command:/record:/spice: steps carry the verb as their keyword's value
		for _, k := range []string{"command", "record", "spice", "run"} {
			if v, ok := step[k]; ok && v != "" {
				word = k
				break
			}
		}
	}
	if word == "" {
		return spec.CheckResult{Status: spec.StatusFail, Message: "step has no verb"}, true
	}
	paramsJSON, _ := json.Marshal(step)
	envJSON, _ := json.Marshal(adeCheckEnv(rc))
	// the host's dial already routes by the venue the invoke was issued under;
	// no local executor materialization is needed for the dispatch.
	opts := sdk.InvokeProviderOpts{}
	resultJSON, err := ex.InvokeProvider(ctx, "verb", word, sdk.OpRun, paramsJSON, envJSON, opts)
	if err != nil {
		return spec.CheckResult{Status: spec.StatusFail, Message: err.Error()}, true
	}
	var res spec.CheckResult
	if len(resultJSON) > 0 {
		_ = json.Unmarshal(resultJSON, &res)
	}
	return res, true
}

// adeCheckEnv: the venue env snapshot for the dispatched verbs (the run's env +
// the venue identity the host needs to route the step).
func adeCheckEnv(rc *runCtx) spec.CheckEnv {
	e := spec.CheckEnv{Mode: "live"}
	if rc != nil {
		if v := rc.env["IMAGE"]; v != "" {
			e.Box = v
		}
		if v := rc.env["EVAL_CHANNEL"]; v != "" {
			e.Instance = v
		}
	}
	return e
}

func adeGraderPrompt(prose string) string {
	return "You are the ADE in-venue grader. Grade this agent-check against the " +
		"live system evidence: " + clip(prose, 600) + " Reply with exactly: pass or fail."
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var _ = filepath.Join
