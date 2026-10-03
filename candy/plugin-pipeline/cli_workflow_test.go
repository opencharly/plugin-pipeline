package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cli_workflow_test.go — the RUN-LEG proof for the hard cutover: `charly pipeline
// run <entity> --dry-run` resolves the authored spec.Pipeline entity, validates it
// with workflowkit.ValidatePipeline, and lowers the (workflow.lobster, charly.yml)
// pair to the generation dir WITHOUT dispatching (dispatch needs the `workflow`
// engine, which is not this plugin). No executor is needed for either leg, so the
// real runCLI entrypoint runs here exactly as it does host-side.
//
// The engine-side half (executing the lowered pair) is plugin-lobster's R10.

// mixedGrammarPlan mixes all three authored grammars in ONE pipeline: a lobster shell
// arm (`run:`, executed by the engine), a flow key (`when:`), and a single-step
// builtin-verb plan step — `plan:` with one step, the intent keyword carrying the
// description and a `<word>: <input>` sugar key selecting the plugin verb (the loader's
// documented step grammar; `gate` is this candy's own `verb:gate`).
const mixedGrammarPlan = `mixed-plan:
  pipeline:
    description: a mixed-grammar plan (lobster shell arm + single-step verb sugar + a flow key)
    steps:
      - id: build
        run: "true"
      - id: verify
        when: $build.exit_code == 0
        plan:
          - run: grade the build result
            gate:
              condition: "true"
`

const brokenPlan = `broken-plan:
  pipeline:
    description: two execution arms on one step
    steps:
      - id: s
        run: "true"
        pipeline: other
`

func writeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHARLY_PROJECT_DIR", dir)
	return dir
}

// TestCLIValidate_AcceptsMixedGrammar: the new grammar is spec.Pipeline (`steps:`),
// and a step may carry either a lobster arm (`run:`) or a builtin-verb plan step.
func TestCLIValidate_AcceptsMixedGrammar(t *testing.T) {
	writeProject(t, mixedGrammarPlan)
	code, err := runCLI([]string{"validate", "mixed-plan"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("a valid mixed-grammar plan must validate: code=%d err=%v", code, err)
	}
}

// TestCLIValidate_RejectsTwoArms: the broken fixture is rejected by
// workflowkit.ValidatePipeline with the NAMED error (the exec-arm XOR), never a
// silent pass.
func TestCLIValidate_RejectsTwoArms(t *testing.T) {
	writeProject(t, brokenPlan)
	code, err := runCLI([]string{"validate", "broken-plan"}, nil)
	if err == nil || code == 0 {
		t.Fatalf("a two-arm step must be rejected: code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "execution arms set") {
		t.Fatalf("the rejection must name the exec-arm XOR, got: %v", err)
	}
}

// TestCLIRunDryRun_LowersBothFiles: the run leg writes the pair — the lobster
// workflow (carrying the lowered `plan:` step as a `charly -C <gen> task …` run)
// and the generated charly.yml (carrying the builtin-verb task) — and stops before
// dispatch.
func TestCLIRunDryRun_LowersBothFiles(t *testing.T) {
	dir := writeProject(t, mixedGrammarPlan)
	t.Setenv("CHARLY_BIN", "/usr/bin/charly")
	code, err := runCLI([]string{"run", "mixed-plan", "--dry-run"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("dry-run lowering: code=%d err=%v", code, err)
	}
	gen := filepath.Join(dir, ".opencharly", "pipelines", "mixed-plan")
	lobster, lerr := os.ReadFile(filepath.Join(gen, "workflow.lobster"))
	if lerr != nil {
		t.Fatalf("the lobster half was not written: %v", lerr)
	}
	charly, cerr := os.ReadFile(filepath.Join(gen, "charly.yml"))
	if cerr != nil {
		t.Fatalf("the generated charly.yml was not written: %v", cerr)
	}
	// the workflow name is the generation dir's basename, and the lobster half
	// carries every step — the `plan:` step lowered to a `charly -C <gen> task …`
	// run with the charly binary BAKED IN (the engine drives the generated task).
	var flow struct {
		Name  string `yaml:"name"`
		Steps []any  `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &flow); err != nil {
		t.Fatalf("the lowered workflow is not valid YAML: %v\n%s", err, lobster)
	}
	if flow.Name != "mixed-plan" || len(flow.Steps) != 2 {
		t.Fatalf("lowered workflow = name %q steps %d, want mixed-plan/2", flow.Name, len(flow.Steps))
	}
	if !strings.Contains(string(lobster), "/usr/bin/charly") {
		t.Fatalf("the lowering must bake the charly binary into the lowered plan step's run:\n%s", lobster)
	}
	if !strings.Contains(string(lobster), "task _pipeline-mixed-plan-verify") {
		t.Fatalf("the plan step must lower to a run driving the generated charly task:\n%s", lobster)
	}
	// the builtin-verb plan step lowered to a generated charly task carrying the
	// AUTHORED sugar (gate:), and the generated file flat-imports the project's
	// charly.yml by FILE PATH.
	var genDoc map[string]any
	if err := yaml.Unmarshal(charly, &genDoc); err != nil {
		t.Fatalf("the generated charly.yml is not valid YAML: %v\n%s", err, charly)
	}
	if _, ok := genDoc["_pipeline-mixed-plan-verify"]; !ok {
		t.Fatalf("the builtin-verb plan step did not lower to a generated charly task:\n%s", charly)
	}
	if !strings.Contains(string(charly), "../../../charly.yml") {
		t.Fatalf("the generated charly.yml must flat-import the project charly.yml by relative file path:\n%s", charly)
	}
	if !strings.Contains(string(charly), "run: grade the build result") ||
		!strings.Contains(string(charly), `condition: "true"`) {
		t.Fatalf("the generated task must carry the authored plan step + its verb sugar, not an empty step:\n%s", charly)
	}
}

// TestCLIRunDryRun_UnknownEntityNamesTheCause: an entity that is not a pipeline
// is a NAMED error, never "not found".
func TestCLIRunDryRun_UnknownEntityNamesTheCause(t *testing.T) {
	dir := t.TempDir()
	body := "version: 2026.249.2125\nnot-a-plan:\n    local:\n        plan:\n            - check: x\n              command: 'true'\n"
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHARLY_PROJECT_DIR", dir)
	_, err := runCLI([]string{"run", "not-a-plan", "--dry-run"}, nil)
	if err == nil {
		t.Fatal("a wrong-kind entity must error")
	}
	if !strings.Contains(err.Error(), "has no pipeline: kind") {
		t.Fatalf("a wrong-kind entity must be diagnosed as such, got: %v", err)
	}
}
