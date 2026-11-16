package workflowkit

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// lower_test.go — the stage lowering's golden and behavioural tests (P26 unit 2,
// opencharly/plugin-pipeline#66).
//
// TWO KINDS OF PROOF LIVE HERE, and they are deliberately different:
//
//   - the GOLDENS assert the generated TEXT (the .lobster workflow and the generated
//     charly.yml), so a lowering regression shows up as a diff and the gate needs no engine;
//   - the ORDERING tests assert the one property a golden cannot: that the emission order
//     IS the `after:` graph, and that a cycle is refused naming its stages. They are
//     written so they FAIL if the ordering is ignored (opencharly/plugin-pipeline#66's R7
//     requirement) — see TestLowerOrdersStagesByAfter's note on why the fixture's names are
//     chosen the way they are.

// updateGoldens regenerates testdata/*.golden when set (`go test -run TestLower -update`).
var updateGoldens = false

func TestMain(m *testing.M) {
	flag.BoolVar(&updateGoldens, "update", false, "regenerate testdata/*.golden")
	flag.Parse()
	os.Exit(m.Run())
}

// --- fixture builders ---------------------------------------------------------

// stepStage is a stage whose arm is a list of charly plan steps.
func stepStage(steps ...spec.Step) spec.Stage {
	return spec.Stage{Step: spec.StageStep{Step: steps}}
}

// ranAfter attaches `after:` edges, in the order given.
func ranAfter(st spec.Stage, names ...string) spec.Stage {
	for _, n := range names {
		st.After = append(st.After, spec.StageName(n))
	}
	return st
}

// charlyCmd is one `run:` plan step carrying a host command.
func charlyCmd(intent, command string) spec.Step {
	return spec.Step{Run: intent, Op: spec.Op{Command: command}}
}

// e2ePipeline mirrors a realistic routine: a producer step, a consumer step that reads the
// producer's output (a REF), a deployment stage that realizes a bed and runs its checks, a
// sub-pipeline stage, and an ask before the final step.
func e2ePipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "end-to-end lowering fixture",
		Args: map[string]spec.TaskParamSpec{
			"who": {Description: "who to greet", Default: "world"},
		},
		Stage: map[string]spec.Stage{
			"fetch": stepStage(charlyCmd("fetch the facts", `printf '{"name":"ok","n":3}'`)),
			"probe": ranAfter(stepStage(
				charlyCmd("the ref reaches the charly step shell-safely", `printf "NAME=%s\n" "$fetch.json.name"`),
				charlyCmd("a passed-through env var expands", `echo "N=$fetch.json.n"`),
			), "fetch"),
			"bed": ranAfter(spec.Stage{Deployment: spec.StageDeployment{
				Deployment: []spec.StageRef{"hello-pod"},
			}}, "probe"),
			"verify": ranAfter(spec.Stage{Pipeline: spec.StagePipeline{Pipeline: "verify"}}, "bed"),
			"confirm": ranAfter(spec.Stage{Ask: spec.StageAsk{
				Prompt: "Proceed with ${who}?",
			}}, "verify"),
			"final": ranAfter(stepStage(charlyCmd("the flow is done", `echo '{"done":true}'`)), "confirm"),
		},
	}
}

// charlyStepPipeline is the single-command form: one stage, one step.
func charlyStepPipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "charly-step fixture",
		Stage: map[string]spec.Stage{
			"only": stepStage(charlyCmd("run the command", `echo hello`)),
		},
	}
}

// chainPipeline is an ORDERING-SENSITIVE fixture whose stage names sort in the opposite
// order to the graph: `alpha` runs after `zulu`, so a lowering that emitted the map's
// sorted names (or Go's map order) would be wrong, and only the `after:` graph gets it
// right. `middle` sits between them.
func chainPipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "ordering chain fixture",
		Stage: map[string]spec.Stage{
			"alpha":  ranAfter(stepStage(charlyCmd("last", `echo alpha`)), "middle"),
			"middle": ranAfter(stepStage(charlyCmd("second", `echo middle`)), "zulu"),
			"zulu":   stepStage(charlyCmd("first", `echo zulu`)),
		},
	}
}

// retryPipeline carries the CONTRACT's retry (#Retry{attempt, interval}) on a stage, which
// the lowering translates onto the engine's own retry object.
func retryPipeline() *params.Pipeline {
	st := stepStage(charlyCmd("retry me", `echo retry`))
	st.Retry = spec.Retry{Attempt: 3, Interval: "5s"}
	st.Timeout = "90s"
	st.On_error = "fail"
	return &params.Pipeline{
		Description: "retry fixture",
		Stage:       map[string]spec.Stage{"flaky": st},
	}
}

// lanePipeline runs one stage once per item of the `beds` parameter.
func lanePipeline() *params.Pipeline {
	st := stepStage(charlyCmd("check the bed", `charly check run "$LANE_ITEM"`))
	st.Lane = spec.Lane{Over: "beds"}
	st.Concurrency = 2
	return &params.Pipeline{
		Description: "lane fixture",
		Args:        map[string]spec.TaskParamSpec{"beds": {Description: "the beds to check"}},
		Stage:       map[string]spec.Stage{"bed": st},
	}
}

// --- the ORDERING proof (R7) --------------------------------------------------

// TestLowerOrdersStagesByAfter is the test opencharly/plugin-pipeline#66 requires: a
// two-stage pipeline whose second stage declares `after: [<first>]`, asserted on the
// EMITTED STEP ORDER.
//
// IT FAILS IF THE ORDERING IS IGNORED, by construction of the names: the producer is
// `zulu` and the consumer is `alpha`, so both of the orders a lowering could fall back to
// — the map's sorted names, or Go's randomised map iteration — put `alpha` first on at
// least the sorted path, and the assertion demands `zulu` first. (The iteration order path
// is caught by running the same assertion over several Lower calls: see
// TestLowerOrdersStagesByAfterIsStable.)
func TestLowerOrdersStagesByAfter(t *testing.T) {
	lobster, _, err := Lower(chainPipeline(), "/proj", "/proj/.opencharly/pipelines/chain", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	var flow struct {
		Steps []struct {
			ID string `yaml:"id"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &flow); err != nil {
		t.Fatalf("the lowered workflow is not YAML: %v", err)
	}
	var got []string
	for _, s := range flow.Steps {
		got = append(got, s.ID)
	}
	want := []string{"zulu", "middle", "alpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lowered stage order = %v, want %v (the `after:` graph: zulu -> middle -> alpha; a\n"+
			"lowering that ignores `after:` emits alpha first because the map is not ordered)\n%s", got, want, lobster)
	}
}

// TestLowerOrdersStagesByAfterIsStable re-runs the same lowering several times: a Go map
// iterates in a randomised order, so an implementation that emitted map order would pass
// the single-shot assertion above roughly once in six runs and fail the rest. This is the
// half of the proof that catches "it happened to work".
func TestLowerOrdersStagesByAfterIsStable(t *testing.T) {
	for i := 0; i < 8; i++ {
		lobster, _, err := Lower(chainPipeline(), "/proj", "/proj/.opencharly/pipelines/chain", "/usr/local/bin/charly")
		if err != nil {
			t.Fatalf("Lower: %v", err)
		}
		var flow struct {
			Steps []struct {
				ID string `yaml:"id"`
			} `yaml:"steps"`
		}
		if err := yaml.Unmarshal(lobster, &flow); err != nil {
			t.Fatalf("Lower: %v", err)
		}
		var got []string
		for _, s := range flow.Steps {
			got = append(got, s.ID)
		}
		if strings.Join(got, ",") != "zulu,middle,alpha" {
			t.Fatalf("run %d: lowered stage order = %v, want [zulu middle alpha]", i, got)
		}
	}
}

// TestLowerRejectsStageCycle proves a cycle is a LOUD load error that NAMES the stages in
// it — not a silent pick of one of the cyclic orders.
func TestLowerRejectsStageCycle(t *testing.T) {
	p := &params.Pipeline{
		Description: "cycle fixture",
		Stage: map[string]spec.Stage{
			"alpha": ranAfter(stepStage(charlyCmd("a", `echo a`)), "beta"),
			"beta":  ranAfter(stepStage(charlyCmd("b", `echo b`)), "alpha"),
		},
	}
	_, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/cycle", "/usr/local/bin/charly")
	if err == nil {
		t.Fatal("Lower accepted a stage cycle: want a loud error naming the stages in it")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the cycle error does not name stage %q: %v", name, err)
		}
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the error does not say what is wrong (want the word `cycle`): %v", err)
	}
}

// TestValidateRejectsStageCycle pins the same rule at the LOAD gate: a cycle is a document
// defect, so `pipeline validate` must refuse it too.
func TestValidateRejectsStageCycle(t *testing.T) {
	p := &params.Pipeline{
		Description: "cycle fixture",
		Stage: map[string]spec.Stage{
			"alpha": ranAfter(stepStage(charlyCmd("a", `echo a`)), "beta"),
			"beta":  ranAfter(stepStage(charlyCmd("b", `echo b`)), "alpha"),
		},
	}
	err := ValidatePipeline(p)
	if err == nil {
		t.Fatal("ValidatePipeline accepted a stage cycle")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("the validation error does not name both cyclic stages: %v", err)
	}
}

// --- goldens ------------------------------------------------------------------

// TestLowerGolden pins the generated TEXT of the four fixtures.
func TestLowerGolden(t *testing.T) {
	cases := []struct {
		name string
		p    *params.Pipeline
		dir  string
	}{
		{"charly-step", charlyStepPipeline(), "charly-step"},
		{"e2e", e2ePipeline(), "e2e"},
		{"nested", chainPipeline(), "nested"},
		{"redo", retryPipeline(), "redo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join("/proj/.opencharly/pipelines", tc.dir)
			lobster, charly, err := Lower(tc.p, "/proj", dir, "/usr/local/bin/charly")
			if err != nil {
				t.Fatalf("Lower: %v", err)
			}
			checkGolden(t, tc.name+".workflow.lobster.golden", lobster)
			checkGolden(t, tc.name+".charly.yml.golden", charly)
		})
	}
}

// --- arm behaviour ------------------------------------------------------------

// TestLowerDeploymentRunsChecks pins the deployment arm's lowering: one `charly check run
// <ref>` per named deployment, carried by the generated task (DESIGN.md:2470's idiom).
func TestLowerDeploymentRunsChecks(t *testing.T) {
	p := &params.Pipeline{
		Description: "deployment fixture",
		Stage: map[string]spec.Stage{
			"bed": {Deployment: spec.StageDeployment{Deployment: []spec.StageRef{"hello-pod", "hello-vm"}}},
		},
	}
	lobster, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/bed", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	out := string(lobster) + string(charly)
	for _, want := range []string{"charly check run hello-pod", "charly check run hello-vm"} {
		if !strings.Contains(out, want) {
			t.Errorf("the lowered pair does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(lobster), "deployment:") {
		t.Errorf("the arm word must not reach the engine as a key (it is an authoring word):\n%s", lobster)
	}
}

// TestLowerAskIsAnApprovalGate pins the ask arm's ONE engine gate: the prompt reaches the
// engine as its approval message, and the engine's own gate (needs_approval / resume
// --approve) answers it. The engine's INPUT gate is deliberately not used — its response is
// validated by a JSON SCHEMA while the contract's answer channel carries a matcher.
func TestLowerAskIsAnApprovalGate(t *testing.T) {
	p := &params.Pipeline{
		Description: "ask fixture",
		Stage: map[string]spec.Stage{
			"confirm": {Ask: spec.StageAsk{Prompt: "which branch?"}},
		},
	}
	lobster, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/ask", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	out := string(lobster)
	if !strings.Contains(out, "approval: which branch?") {
		t.Errorf("the ask prompt must reach the engine as its approval message:\n%s", out)
	}
	if strings.Contains(out, "input:") {
		t.Errorf("the ask must not lower to the engine's INPUT gate (a JSON-Schema response):\n%s", out)
	}
}

// TestLowerLaneUsesTheContractItemName pins DESIGN.md:3014's lane rule: the item is
// `LANE_ITEM`, the pool width is `concurrency`, and there is no author-chosen item/index
// name and no pause.
func TestLowerLaneUsesTheContractItemName(t *testing.T) {
	lobster, _, err := Lower(lanePipeline(), "/proj", "/proj/.opencharly/pipelines/lane", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	var flow struct {
		Steps []map[string]any `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &flow); err != nil {
		t.Fatalf("the lowered workflow is not YAML: %v", err)
	}
	if len(flow.Steps) != 1 {
		t.Fatalf("lowered steps = %d, want 1", len(flow.Steps))
	}
	lane := flow.Steps[0]
	if got := lane["lanes"]; got != "${beds}" {
		t.Errorf("lowered lanes = %v, want the parameter reference ${beds}", got)
	}
	if got := lane["item_var"]; got != "LANE_ITEM" {
		t.Errorf("lowered item_var = %v, want LANE_ITEM (D-FLOW-1's fixed name)", got)
	}
	if got := fmt.Sprint(lane["batch_size"]); got != "2" {
		t.Errorf("lowered batch_size = %v, want 2 (concurrency)", got)
	}
	for _, banned := range []string{"pause_ms", "index_var"} {
		if _, ok := lane[banned]; ok {
			t.Errorf("the lowered lane carries %q, which D-FLOW-1 refuses", banned)
		}
	}
}

// TestLowerRefsBraceForm keeps the REF plumbing: a plan reference is passed to the charly
// task as `-p REF_n="$REF_n"` and the plan is rewritten to read it.
func TestLowerRefsBraceForm(t *testing.T) {
	p := &params.Pipeline{
		Description: "ref fixture",
		Stage: map[string]spec.Stage{
			"consumer": stepStage(charlyCmd("read the producer", `echo "$producer.json.name"`)),
			"producer": stepStage(charlyCmd("produce", `echo '{"name":"ok"}'`)),
		},
	}
	p.Stage["consumer"] = ranAfter(p.Stage["consumer"], "producer")

	lobster, charly, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/ref", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if !strings.Contains(string(lobster), `REF_1="$REF_1"`) {
		t.Errorf("the lobster step does not pass the ref to the charly task:\n%s", lobster)
	}
	if !strings.Contains(string(charly), "REF_1") && !strings.Contains(string(charly), "${REF_1}") {
		t.Errorf("the generated plan does not read the ref:\n%s", charly)
	}
}

// TestLowerEmitsEngineNumberTypes decodes the lowered file into the ENGINE's OWN field
// types. It is the boundary check R7 asks for: `timeout_ms`, `retry.max`, `retry.delay_ms`
// and `batch_size` are Go ints in the engine, so a quoted number is an artifact the engine
// refuses at load — the golden text alone cannot tell `90000` from `"90000"`, and the first
// version of this lowering emitted the quoted form.
func TestLowerEmitsEngineNumberTypes(t *testing.T) {
	type retry struct {
		Max     int `yaml:"max"`
		DelayMs int `yaml:"delay_ms"`
	}
	lobster, _, err := Lower(retryPipeline(), "/proj", "/proj/.opencharly/pipelines/redo", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	var flow struct {
		Steps []struct {
			TimeoutMs int   `yaml:"timeout_ms"`
			Retry     retry `yaml:"retry"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &flow); err != nil {
		t.Fatalf("the lowered workflow does not decode into the engine's number types: %v\n%s", err, lobster)
	}
	if len(flow.Steps) != 1 || flow.Steps[0].TimeoutMs != 90000 || flow.Steps[0].Retry.Max != 3 || flow.Steps[0].Retry.DelayMs != 5000 {
		t.Fatalf("lowered numbers = %+v, want timeout 90000 / max 3 / delay 5000", flow.Steps)
	}

	lobster, _, err = Lower(lanePipeline(), "/proj", "/proj/.opencharly/pipelines/lane", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower(lane): %v", err)
	}
	var laneFlow struct {
		Steps []struct {
			BatchSize int `yaml:"batch_size"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(lobster, &laneFlow); err != nil {
		t.Fatalf("the lowered lane does not decode its pool width as a number: %v\n%s", err, lobster)
	}
	if len(laneFlow.Steps) != 1 || laneFlow.Steps[0].BatchSize != 2 {
		t.Fatalf("lowered batch_size = %+v, want 2", laneFlow.Steps)
	}
}

// --- loud refusals ------------------------------------------------------------

// TestLowerRefusesUnprojectableStage refuses rather than drops, for every stage field the
// engine has no word for. Each case asserts the STAGE NAME is in the message, so an author
// knows which stage to change.
func TestLowerRefusesUnprojectableStage(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*params.Pipeline)
		want   string
	}{
		{
			name: "when-matcher",
			mutate: func(p *params.Pipeline) {
				st := stepStage(charlyCmd("x", `echo x`))
				st.When = spec.StageWhen{"flag": spec.Matcher{Op: "equals", Value: "on"}}
				p.Stage["guarded"] = st
			},
			want: "when:",
		},
		{
			name: "retry-until",
			mutate: func(p *params.Pipeline) {
				st := stepStage(charlyCmd("x", `echo x`))
				st.Retry = spec.Retry{Attempt: 2, Until: "10m"}
				p.Stage["bounded"] = st
			},
			want: "retry.until",
		},
		{
			name: "ask-unanswered",
			mutate: func(p *params.Pipeline) {
				p.Stage["confirm"] = spec.Stage{Ask: spec.StageAsk{
					Prompt: "proceed?",
					Answer: spec.Answer{Unanswered: "cancelled"},
				}}
			},
			want: "unanswered",
		},
		{
			name: "ask-expect",
			mutate: func(p *params.Pipeline) {
				p.Stage["confirm"] = spec.Stage{Ask: spec.StageAsk{
					Prompt: "proceed?",
					Answer: spec.Answer{Expect: spec.Matcher{Op: "equals", Value: "yes"}},
				}}
			},
			want: "expect",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &params.Pipeline{Description: "refusal fixture", Stage: map[string]spec.Stage{
				"only": stepStage(charlyCmd("x", `echo x`)),
			}}
			tc.mutate(p)
			_, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/refusal", "/usr/local/bin/charly")
			if err == nil {
				t.Fatalf("Lower accepted a stage field it cannot express")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}
}

// TestLowerRejectsInvalid keeps the validator wired into the lowering: a bad body never
// reaches the emitter.
func TestLowerRejectsInvalid(t *testing.T) {
	p := &params.Pipeline{
		Description: "invalid fixture",
		Stage: map[string]spec.Stage{
			"empty": {},
		},
	}
	if _, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/bad", "/usr/local/bin/charly"); err == nil {
		t.Fatal("Lower accepted a stage with no arm")
	}
}

// checkGolden compares (or, under -update, writes) one golden file.
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
