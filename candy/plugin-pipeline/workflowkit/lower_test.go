package workflowkit

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// lower_test.go — the Lower golden tests. They assert the generated TEXT only, so the
// gate needs no live engine: a lowering regression (a dropped ref, a mis-resugared plan,
// a lost `--output`) shows up as a diff.

// updateGoldens regenerates testdata/*.golden when set (`go test -run TestLower -update`).
var updateGoldens = false

func TestMain(m *testing.M) {
	flag.BoolVar(&updateGoldens, "update", false, "regenerate testdata/*.golden")
	flag.Parse()
	os.Exit(m.Run())
}

// e2ePipeline mirrors the workflow the RDD executed live end-to-end: a run step producing
// JSON, a plan step consuming it, an approval gate, and a when-guarded final step.
func e2ePipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "end-to-end lowering fixture",
		Args: map[string]spec.TaskParamSpec{
			"who": {Description: "who to greet", Default: "world"},
		},
		Steps: []params.PipelineStep{
			{Id: "fetch", Run: `printf '{"name":"ok","n":3}'`},
			{Id: "probe", Plan: []spec.Step{
				{Run: "the ref reaches the charly step shell-safely", Op: spec.Op{Command: `printf "NAME=%s\n" "$fetch.json.name"`}},
				{Run: "a plugin verb dispatches through the core registry", Op: spec.Op{Plugin: "task", PluginInput: map[string]any{"task": "hello"}}},
				{Run: "a passed-through env var expands", Op: spec.Op{Command: `echo "N=$fetch.json.n"`}},
			}},
			{Id: "confirm", Approval: "Proceed with ${who}?"},
			{Id: "final", When: "$confirm.approved", Run: `echo '{"done":true}'`},
		},
	}
}

// TestLowerCarriesLanesStep pins that the CONCURRENT loop arm survives the lowering with
// the keys that make it an arm: `Lower` copies lobster-native step keys verbatim, so the
// collection expression, the pool width and the item binding must arrive untouched. A
// dropped key would be a lowering regression the validator cannot see (it runs on the
// authored body, not on the emitted file), so this asserts the EMITTED YAML, not prose.
func TestLowerCarriesLanesStep(t *testing.T) {
	p := &params.Pipeline{
		Description: "lanes lowering fixture",
		Steps: []params.PipelineStep{
			{Id: "seed", Run: `printf '{"items":["a","b","c"]}'`},
			{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2, ItemVar: "it",
				Steps: []params.PipelineSubStep{{Id: "s", Run: `echo "$it"`}}},
		},
	}
	lobster, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/lanes", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	var flow struct {
		Steps []map[string]any `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &flow); err != nil {
		t.Fatalf("the lowered workflow is not YAML: %v", err)
	}
	if len(flow.Steps) != 2 {
		t.Fatalf("lowered steps = %d, want 2", len(flow.Steps))
	}
	lane := flow.Steps[1]
	if got := lane["lanes"]; got != "$seed.json.items" {
		t.Errorf("lowered lanes = %v, want the collection expression $seed.json.items", got)
	}
	if got := lane["batch_size"]; got != 2 {
		t.Errorf("lowered batch_size = %v, want 2 (the pool width)", got)
	}
	if got := lane["item_var"]; got != "it" {
		t.Errorf("lowered item_var = %v, want it", got)
	}
	if subs, ok := lane["steps"].([]any); !ok || len(subs) != 1 {
		t.Errorf("lowered steps body = %v, want one sub-step", lane["steps"])
	}
}

func TestLowerGolden(t *testing.T) {
	p := e2ePipeline()
	lobster, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/e2e", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	checkGolden(t, "e2e.workflow.lobster.golden", lobster)
	checkGolden(t, "e2e.charly.yml.golden", charly)
}

// TestLowerRefsBraceForm pins the FORM of the ref the lowering writes into a generated
// plan, and proves it through the REAL expander — kit.ExpandOpVars, whose grammar is the
// exported kit.TestVarRefPattern — rather than through a local copy of that grammar, which
// would only re-state it. The expander is BRACE-ONLY, so a ref consumed as a non-shell verb
// input must be emitted as ${REF_n}: the bare $REF_n form is not a ref at all and passes
// through as literal text, which is the defect this pins.
func TestLowerRefsBraceForm(t *testing.T) {
	p := &params.Pipeline{
		Description: "ref-form fixture",
		Steps: []params.PipelineStep{
			{Id: "fetch", Run: `printf '{"name":"ok"}'`},
			{Id: "probe", Plan: []spec.Step{
				// a NON-SHELL verb input carrying the ref (a plugin verb's opaque field).
				{Run: "the ref reaches a plugin verb input", Op: spec.Op{Plugin: "task", PluginInput: map[string]any{"task": "$fetch.json.name"}}},
			}},
		},
	}
	_, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/ref", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	plan := string(charly)
	braceRef := "${REF_1}"
	if !strings.Contains(plan, braceRef) {
		t.Errorf("generated plan does not carry the brace form %s:\n%s", braceRef, plan)
	}
	if strings.Contains(plan, "$REF_1") {
		t.Errorf("generated plan still carries the bare form $REF_1 (never expanded):\n%s", plan)
	}
	// The emitted ref must be matched by the REAL expander's grammar — kit.TestVarRefPattern,
	// read off the generated artifact itself. A local copy of the pattern matched against the
	// literal constant would only re-state the grammar and could never fail.
	if refs := kit.TestVarRefPattern.FindAllString(plan, -1); !slices.Contains(refs, braceRef) {
		t.Fatalf("generated plan's refs %v do not include %s, the form kit.ExpandOpVars expands:\n%s", refs, braceRef, plan)
	}
	// And it must actually RESOLVE through that expander, in the field a plugin verb reads (the
	// opaque PluginInput map, not a shell string): this is the behaviour the lowering change is for.
	op := &spec.Op{Plugin: "task", PluginInput: map[string]any{"task": braceRef}}
	if missing := kit.ExpandOpVars(op, map[string]string{"REF_1": "ok"}); len(missing) != 0 {
		t.Fatalf("%s did not resolve in kit.ExpandOpVars: unresolved %v", braceRef, missing)
	}
	if got := op.PluginInput["task"]; got != "ok" {
		t.Errorf("plugin input = %v, want the expanded value \"ok\"", got)
	}
	// The control the fix exists for: the BARE form is not a reference at all, so it passes
	// through as literal text — which is exactly why the plan must not carry it.
	bare := &spec.Op{Plugin: "task", PluginInput: map[string]any{"task": "$REF_1"}}
	if missing := kit.ExpandOpVars(bare, map[string]string{"REF_1": "ok"}); len(missing) != 0 {
		t.Fatalf("the bare form must not parse as a reference: unexpected unresolved %v", missing)
	}
	if got := bare.PluginInput["task"]; got != "$REF_1" {
		t.Errorf("bare $REF_1 must pass through as literal text, got %v", got)
	}
}

// TestLowerInputSchemaKey pins the ONE key whose spelling differs between the authored
// pipeline and the lobster file. The authoring def spells the input gate's schema
// `response_schema` (this repo's schema/pipeline.cue #PipelineInput, whose Go field carries
// `yaml:"response_schema"`); the lobster file spells it `responseSchema` — what upstream
// lobster reads, transcribed into plugin-lobster's #LobsterInput, whose loader REQUIRES it
// (a step whose input object lacks `responseSchema` is rejected at load with
// "input.responseSchema must be an object"). So the lowering has to translate this one
// key: emitted with the authoring spelling, an input gate never reaches the engine.
func TestLowerInputSchemaKey(t *testing.T) {
	p := &params.Pipeline{
		Description: "input-gate fixture",
		Steps: []params.PipelineStep{
			{Id: "ask", Input: params.PipelineInput{
				Prompt:         "which branch?",
				ResponseSchema: map[string]any{"type": "object"},
			}},
		},
	}
	lobster, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/ask", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	plan := string(lobster)
	// The engine's key, and the only form any lobster consumer reads.
	if !strings.Contains(plan, "responseSchema:") {
		t.Errorf("the lobster file does not carry `responseSchema`, the key the engine requires:\n%s", plan)
	}
	// The authoring spelling must not survive into the lobster file.
	if strings.Contains(plan, "response_schema") {
		t.Errorf("the lobster file carries the AUTHORING spelling `response_schema`, which the engine's loader rejects:\n%s", plan)
	}
}

// redoPipeline is the migrated-stage shape: a lobster step whose plan is ONE
// `{<verb>: {…, redo: {…}}}` element (what plugin-migrate's reshapePipelineVerbStage emits).
// The redo spec lives in the verb body and must be COPIED onto the lobster step. It covers
// BOTH marshalled element shapes, because the verb body has to be found by KEY and not by
// position: `oracle` carries a `Run` description — the ordinary authored step, which marshals
// as the two-pair `{run: <desc>, task: {…}}` element (spec.Step emits its intent keyword
// first) — while `renode` does not, the one-pair `{task: {…}}` element. A positional lookup
// passes exactly one of the two and silently drops the redo on the other.
func redoPipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "redo-carry fixture",
		Steps: []params.PipelineStep{
			{Id: "oracle", Plan: []spec.Step{
				{Run: "rewind the oracle", Op: spec.Op{Plugin: "task", PluginInput: map[string]any{
					"task": "oracle",
					"redo": map[string]any{
						"max":            4,
						"escalate_after": 5,
						"triggers":       map[string]any{"redo-plan": "oracle"},
					},
				}}},
			}},
			{Id: "renode", Plan: []spec.Step{
				{Op: spec.Op{Plugin: "task", PluginInput: map[string]any{
					"task": "renode",
					"redo": map[string]any{
						"max":      2,
						"triggers": map[string]any{"redo-plan": "renode"},
					},
				}}},
			}},
			{Id: "plain", Plan: []spec.Step{
				{Op: spec.Op{Plugin: "task", PluginInput: map[string]any{"task": "plain"}}},
			}},
		},
	}
}

// TestLowerRedoCarry proves the redo spec is COPIED, not moved: the engine reads it off
// the lobster STEP (which would otherwise be `{id, run}` only, since `plan:` is deleted),
// while the VERB still reads `redo.on_fail` from its own input. Both plan-element shapes are
// asserted separately, so a regression to a positional verb-body lookup fails here.
func TestLowerRedoCarry(t *testing.T) {
	lobster, charly, err := Lower(redoPipeline(), "/proj", "/proj/.opencharly/pipelines/redo", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	checkGolden(t, "redo.workflow.lobster.golden", lobster)
	checkGolden(t, "redo.charly.yml.golden", charly)

	// BOTH halves must carry it — either alone would pass under a wrong implementation.
	if !strings.Contains(string(charly), "redo:") {
		t.Errorf("generated charly verb input lost the redo spec (move, not copy):\n%s", charly)
	}
	ls := string(lobster)
	if n := strings.Count(ls, "redo:"); n != 2 {
		t.Errorf("want the redo spec on BOTH redo steps of the lobster file, got %d `redo:` keys:\n%s", n, ls)
	}
	// Per-step, so each element shape is pinned on its own.
	io, ir, ip := strings.Index(ls, "id: oracle"), strings.Index(ls, "id: renode"), strings.Index(ls, "id: plain")
	if io < 0 || ir < 0 || ip < 0 || io >= ir || ir >= ip {
		t.Fatalf("the lobster file does not carry the three steps in order:\n%s", ls)
	}
	if !strings.Contains(ls[io:ir], "redo:") {
		t.Errorf("the step with a `Run` description did not get the redo spec (two-pair element):\n%s", ls[io:ir])
	}
	if !strings.Contains(ls[ir:ip], "redo:") {
		t.Errorf("the description-free step did not get the redo spec (one-pair element):\n%s", ls[ir:ip])
	}
	if strings.Contains(ls[ip:], "redo:") {
		t.Errorf("a plan step with no redo gained a `redo:` key:\n%s", ls[ip:])
	}
}

// TestLowerNoRedoEmitsNoKey pins the defensive half: no redo child → no `redo:` key
// anywhere (never an empty `redo: {}`), so a plain plan lowers byte-identically to before.
func TestLowerNoRedoEmitsNoKey(t *testing.T) {
	lobster, charly, err := Lower(e2ePipeline(), "/proj", "/proj/.opencharly/pipelines/e2e", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if strings.Contains(string(lobster), "redo") {
		t.Errorf("lowered a step with no redo but emitted a `redo` key:\n%s", lobster)
	}
	if strings.Contains(string(charly), "redo") {
		t.Errorf("generated charly.yml with no redo gained a `redo` key:\n%s", charly)
	}
}

func TestLowerCharlyStep(t *testing.T) {
	p := &params.Pipeline{
		Description: "charly-arm fixture",
		Steps: []params.PipelineStep{
			{Id: "build", Charly: []string{"box", "build", "my box"}},
			{Id: "after", When: "$build.exit_code == 0", Run: "true"},
		},
	}
	lobster, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/ci", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	checkGolden(t, "charly-step.workflow.lobster.golden", lobster)
	checkGolden(t, "charly-step.charly.yml.golden", charly)
}

func TestLowerNestedPlan(t *testing.T) {
	p := &params.Pipeline{
		Description: "nested plan fixture",
		Steps: []params.PipelineStep{
			{Id: "setup", Run: "true"},
			{Id: "fan", Parallel: params.PipelineParallel{
				Wait: "all",
				Branches: []params.PipelineSubStep{
					{Id: "b1", Plan: []spec.Step{
						{Run: "branch one", Op: spec.Op{Plugin: "task", PluginInput: map[string]any{"task": "one"}}},
					}},
					{Id: "b2", Run: "echo $setup.exit_code"},
				},
			}},
		},
	}
	lobster, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/fan", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	checkGolden(t, "nested.workflow.lobster.golden", lobster)
	checkGolden(t, "nested.charly.yml.golden", charly)
}

func TestLowerRejectsInvalid(t *testing.T) {
	// a step with two exec arms never lowers.
	p := &params.Pipeline{Steps: []params.PipelineStep{{Id: "bad", Run: "true", Charly: []string{"status"}}}}
	if _, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/x", "/bin/charly"); err == nil {
		t.Fatal("Lower accepted a step with two execution arms")
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updateGoldens {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (run `go test ./workflowkit -update` to regenerate): %v", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s mismatch:\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}
