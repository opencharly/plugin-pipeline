package pluginpipeline

// adekit.go — the COMPILED-IN bed drive (plan §2.4, todo #34): when the host
// executor is present (the welded/compiled-in placement), the ADE stage drives
// the rendered bed's plan IN-PROCESS through the kit runner + the shared
// checkkit pieces (the SDK's one home for the grammar + the verb resolver) —
// no charly spawn, no exec. The external charly fallback stays for the
// un-compiled placement (the plugin's CLI mode has no reverse-channel executor).

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/checkkit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// kitExec adapts the host executor to the kit's CheckExecutor seam (the Kind
// method the spec/exec re-export does not carry).
type kitExec struct {
	*sdk.Executor
	kind string
}

func (e *kitExec) Kind() string { return e.kind }

// kitVerbs adapts the checkkit resolver to the kit's VerbResolver seam (the
// RunProvisionAct leg the do:act state-provision verbs need).
type kitVerbs struct {
	*checkkit.VerbResolver
}

func (v *kitVerbs) RunProvisionAct(ctx context.Context, op *spec.Op, verb string) (spec.CheckResult, bool) {
	return v.RunVerb(ctx, op)
}

// bedPlanOps parses the named bed entity out of the project's discovered
// charly.yml files and returns its plan steps as spec.Op values (the check
// steps' fields map directly onto the Op: the command/stdout/eventually/
// retry_interval/context/id + the assert intent).
//
// The bed is resolved BY ENTITY NAME (the loader's own discovery) — never from a
// hardcoded `pr-beds/` layout (the layout is the lane's choice). The entity body
// is NAME-FIRST (`<bed>: { vm: { plan: [...] } }`), so the plan is nested under
// the kind key; parsing only a flat top-level `plan:` read ZERO ops and reported
// a vacuous PASS (RCA: the flat-shape fixture never matched a real bed).
func bedPlanOps(workdir, pr, bed string) ([]spec.Op, error) {
	if bed == "" {
		// #AdeStage.bed is schema-required; an empty bed is a caller defect, not
		// a defaultable input. The predecessor substituted a hardcoded
		// `check-omarchy-pr-<pr>-vm` — the lane-coupling this change removes.
		return nil, fmt.Errorf("ade: bed required (the stage's bed: must resolve)")
	}
	bedFile := findBedEntity(workdir, bed)
	if bedFile == "" {
		return nil, fmt.Errorf("ade: bed entity %q not found under %s", bed, workdir)
	}
	raw, err := os.ReadFile(bedFile)
	if err != nil {
		return nil, err
	}
	plan, err := entityPlan(raw, bed)
	if err != nil {
		return nil, err
	}
	ops := make([]spec.Op, 0, len(plan))
	for _, step := range plan {
		op := spec.Op{IntentDo: string(spec.DoAssert)}
		if v, ok := step["id"].(string); ok {
			op.ID = v
		}
		if v, ok := step["command"].(string); ok {
			op.Command = v
		}
		if v, ok := step["eventually"].(string); ok {
			op.Eventually = spec.Duration(v)
		}
		if v, ok := step["retry_interval"].(string); ok {
			op.RetryInterval = spec.Duration(v)
		}
		if v, ok := step["context"]; ok {
			if ctxs, ok := v.([]any); ok {
				for _, c := range ctxs {
					if s, ok := c.(string); ok {
						op.Context = append(op.Context, spec.Context(s))
					}
				}
			}
		}
		if v, ok := step["stdout"].(map[string]any); ok {
			if m, ok := v["matches"].(string); ok {
				op.Stdout = spec.MatcherList{{Op: "matches", Value: m}}
			}
		}
		// the check verb's name (the "check:" value) is the step's description
		if v, ok := step["check"].(string); ok {
			op.Description = v
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// entityPlan extracts the `plan:` step list from a NAME-FIRST entity body:
// `<bed>: { <kind>: { plan: [...] } }` (vm/local/pod/...). The kind key is any
// single child holding a `plan`.
//
// An entity that EXISTS but carries no plan steps is a HARD error, never a nil
// plan: a zero-op bed would map to a vacuous `PASS code=0` in runAdeBedKit —
// exactly the silent-success class this fix removes. The legacy flat top-level
// `plan:` shape is NOT supported (it never matched a real bed; R5).
func entityPlan(raw []byte, name string) ([]map[string]any, error) {
	var top map[string]any
	if err := yaml.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	body, ok := top[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ade: entity %q not found in the bed file", name)
	}
	for _, v := range body {
		if m, ok := v.(map[string]any); ok {
			if p, ok := m["plan"].([]any); ok {
				rows := planRows(p)
				if len(rows) == 0 {
					return nil, fmt.Errorf("ade: entity %q has an empty plan — a zero-op bed is a vacuous PASS", name)
				}
				return rows, nil
			}
		}
	}
	return nil, fmt.Errorf("ade: entity %q has no plan: steps (a zero-op bed is a vacuous PASS)", name)
}

func planRows(p []any) []map[string]any {
	out := make([]map[string]any, 0, len(p))
	for _, e := range p {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// runAdeBedKit drives the rendered bed's plan in-process and maps the results to
// the deterministic verdict (all pass → PASS; any fail → FAIL; else NO_VALIDATION).
func runAdeBedKit(ctx context.Context, pr, bed, workdir string, ex *sdk.Executor) (string, string, int, error) {
	ops, err := bedPlanOps(workdir, pr, bed)
	if err != nil {
		return "NO_VALIDATION", "ade: plan: " + err.Error(), 0, err
	}
	r := kit.NewRunner(kit.RunnerConfig{
		Exec:       &kitExec{Executor: ex, kind: "vm"},
		Mode:       kit.ModeLive,
		Env:        envMap(),
		HasRuntime: true,
		Verbs:      &kitVerbs{VerbResolver: &checkkit.VerbResolver{Ex: ex, Env: spec.CheckEnv{Mode: "live"}}},
		Grammar:    checkkit.PlanGrammar{},
	})
	results := r.Run(ctx, ops)
	var fails, skips int
	var msgs []string
	for _, res := range results {
		name := res.Verb
		if res.Op != nil && res.Op.Description != "" {
			name = res.Op.Description
		}
		switch res.Status {
		case spec.StatusPass:
		case spec.StatusSkip:
			skips++
		default:
			fails++
			msgs = append(msgs, fmt.Sprintf("%s: %s", name, res.Message))
		}
	}
	if fails > 0 {
		return "FAIL", fmt.Sprintf("%d/%d steps failed: %s", fails, len(results), strings.Join(msgs, "; ")), 2, nil
	}
	if skips > 0 && fails == 0 {
		return "NO_VALIDATION", fmt.Sprintf("%d/%d steps skipped", skips, len(results)), 3, nil
	}
	return "PASS", fmt.Sprintf("%d/%d steps passed", len(results), len(results)), 0, nil
}
