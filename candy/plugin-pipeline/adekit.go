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
	"path/filepath"
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

// bedPlanOps parses the rendered bed's charly.yml and returns its plan steps as
// spec.Op values (the check steps' fields map directly onto the Op: the
// command/stdout/eventually/retry_interval/context/id + the assert intent).
func bedPlanOps(workdir, pr, bed string) ([]spec.Op, error) {
	if bed == "" {
		bed = "check-omarchy-pr-" + pr + "-vm"
	}
	bedFile := filepath.Join(workdir, "pr-beds", "pr-"+pr, "charly.yml")
	if strings.HasSuffix(bed, "-control") {
		bedFile = filepath.Join(workdir, "pr-beds", "pr-"+pr+"-control", "charly.yml")
	}
	if _, err := os.Stat(bedFile); err != nil {
		if found := findBedEntity(workdir, bed); found != "" {
			bedFile = found
		} else {
			return nil, err
		}
	}
	raw, err := os.ReadFile(bedFile)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Plan []map[string]any `yaml:"plan"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	ops := make([]spec.Op, 0, len(doc.Plan))
	for _, step := range doc.Plan {
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
		for k, v := range step {
			if k == "check" {
				if s, ok := v.(string); ok {
					op.Description = s
				}
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
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
