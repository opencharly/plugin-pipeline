package workflowkit

import (
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

func TestValidatePipelineAcceptsAValidPipeline(t *testing.T) {
	if err := ValidatePipeline(e2ePipeline()); err != nil {
		t.Fatalf("ValidatePipeline rejected a valid pipeline: %v", err)
	}
	if err := ValidatePipeline(nil); err == nil {
		t.Error("ValidatePipeline accepted nil")
	}
}

// TestValidatePipelineAcceptsForEach is an EXACT-EMPTINESS check: a valid for_each step
// must validate with ZERO errors. A substring assertion is not enough here — a stray
// "execution arms set" alongside the expected text passes a Contains check while the
// pipeline is still rejected, which is exactly how a steps:/for_each double-count hides.
// `steps:` is the for_each BODY (spec groups it under "for_each companions"), never an
// arm of its own.
func TestValidatePipelineAcceptsForEach(t *testing.T) {
	cases := []struct {
		name string
		p    *params.Pipeline
	}{
		{
			name: "bare for_each",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Run: "echo x"},
				{Id: "fan", ForEach: "$a.json.items",
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
		},
		{
			name: "for_each with every companion",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Run: "echo x"},
				{Id: "fan", ForEach: "$a.json.items", ItemVar: "item", IndexVar: "i",
					BatchSize: 2, PauseMs: 100,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
		},
		{
			name: "for_each with a sub-step arm of its own",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Run: "echo x"},
				{Id: "fan", ForEach: "$a.json.items", Steps: []params.PipelineSubStep{
					{Id: "s1", Run: "true"},
					{Id: "s2", Charly: []string{"status"}},
				}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePipeline(tc.p); err != nil {
				t.Fatalf("ValidatePipeline rejected a valid %s pipeline: %v", tc.name, err)
			}
		})
	}
}

// TestValidatePipelineDecodesAuthoredLanesStep pins the AUTHORED boundary, which is the
// boundary the issue measured: the YAML a `kind: pipeline` author writes must decode onto
// the arm through the spec-pinned `lanes:` key AND pass the gate. A test that only builds
// Go structs would pass while the authored key was dropped on decode.
func TestValidatePipelineDecodesAuthoredLanesStep(t *testing.T) {
	const authored = `
description: pooled fan-out
steps:
  - id: seed
    run: printf '{"items":["a","b"]}'
  - id: lane
    lanes: $seed.json.items
    batch_size: 2
    item_var: it
    steps:
      - id: s
        run: echo "$it"
`
	var p params.Pipeline
	if err := yaml.Unmarshal([]byte(authored), &p); err != nil {
		t.Fatalf("decoding the authored pipeline: %v", err)
	}
	if len(p.Steps) != 2 {
		t.Fatalf("decoded steps = %d, want 2", len(p.Steps))
	}
	lane := p.Steps[1]
	if lane.Lanes != "$seed.json.items" || lane.BatchSize != 2 || lane.ItemVar != "it" {
		t.Fatalf("decoded lanes step = %+v, want lanes/batch_size/item_var intact", lane)
	}
	if err := ValidatePipeline(&p); err != nil {
		t.Fatalf("ValidatePipeline rejected the authored lanes step: %v", err)
	}
}

// TestValidatePipelineAcceptsLanes is the lanes twin of TestValidatePipelineAcceptsForEach,
// and it is an EXACT-EMPTINESS check for the same reason: a substring assertion would pass
// while a stray-companion error still rejected the pipeline. `lanes` is the CONCURRENT loop
// arm — it shares for_each's body/item_var/index_var shape and additionally REQUIRES the
// pool width (`batch_size`), so every accepted case below carries a positive one.
func TestValidatePipelineAcceptsLanes(t *testing.T) {
	cases := []struct {
		name string
		p    *params.Pipeline
	}{
		{
			name: "lanes with the required width and a body",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '{"items":["a","b"]}'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
		},
		{
			name: "lanes with every companion",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '{"items":["a","b"]}'`},
				{Id: "lane", Lanes: "$seed.json.items", ItemVar: "it", IndexVar: "i", BatchSize: 3,
					Steps: []params.PipelineSubStep{{Id: "s", Run: `echo "$it"`}}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePipeline(tc.p); err != nil {
				t.Fatalf("ValidatePipeline rejected a valid %s pipeline: %v", tc.name, err)
			}
		})
	}
}

func TestValidatePipelineRules(t *testing.T) {
	cases := []struct {
		name string
		p    *params.Pipeline
		want string
	}{
		{
			name: "two exec arms",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", Run: "true", Charly: []string{"status"}}}},
			want: "execution arms set",
		},
		{
			name: "no exec arm",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a"}}},
			want: "no execution arm",
		},
		{
			name: "for_each without steps",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", ForEach: "$x.y"}}},
			want: "for_each/lanes requires a non-empty steps list",
		},
		{
			name: "lanes without steps",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", Lanes: "$x.y", BatchSize: 2}}},
			want: "for_each/lanes requires a non-empty steps list",
		},
		{
			name: "lanes without the pool width",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: "true"},
				{Id: "a", Lanes: "$seed.json.items", Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
			want: "lanes requires batch_size >= 1 (the pool width)",
		},
		{
			name: "lanes with a pause",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: "true"},
				{Id: "a", Lanes: "$seed.json.items", BatchSize: 2, PauseMs: 100,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
			want: "lanes cannot define pause_ms",
		},
		{
			name: "for_each and lanes are two arms",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: "true"},
				{Id: "a", ForEach: "$seed.json.items", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
			}},
			want: "2 execution arms set (for_each, lanes)",
		},
		{
			name: "lanes collection names a later step",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Lanes: "$later.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
				{Id: "later", Run: "true"},
			}},
			want: `.lanes: reference $later.* names a step that is not declared earlier`,
		},
		{
			name: "steps without for_each",
			p: &params.Pipeline{Steps: []params.PipelineStep{{Id: "a",
				Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}}}},
			want: "steps only apply alongside for_each",
		},
		{
			name: "item_var equals index_var",
			p: &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", ForEach: "$x.y", ItemVar: "i", IndexVar: "i",
				Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}}}},
			want: "item_var and index_var must differ",
		},
		{
			name: "input and approval together",
			p: &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", Approval: "ok?",
				Input: params.PipelineInput{Prompt: "p"}}}},
			want: "input and approval are mutually exclusive",
		},
		{
			name: "duplicate id in a parallel branch",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Run: "true"},
				{Id: "p", Parallel: params.PipelineParallel{Branches: []params.PipelineSubStep{{Id: "a", Run: "true"}}}},
			}},
			want: "duplicate step id",
		},
		{
			name: "forward reference",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", When: "$b.ok", Run: "true"},
				{Id: "b", Run: "true"},
			}},
			want: "not declared earlier",
		},
		{
			name: "self reference",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", When: "$a.ok", Run: "true"}}},
			want: "not declared earlier",
		},
		{
			name: "plan reference to a later step",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "a", Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "echo $b.out"}}}},
				{Id: "b", Run: "true"},
			}},
			want: "plan: reference $b.* names a step that is not declared earlier",
		},
		{
			name: "when is not lobster grammar",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", When: "$x.y in [1]", Run: "true"}}},
			want: "is not lobster grammar",
		},
		{
			name: "parallel.wait with no branches",
			p:    &params.Pipeline{Steps: []params.PipelineStep{{Id: "a", Run: "true", Parallel: params.PipelineParallel{Wait: "any"}}}},
			want: "parallel.wait is only meaningful with non-empty parallel branches",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePipeline(tc.p)
			if err == nil {
				t.Fatalf("ValidatePipeline accepted an invalid pipeline (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidatePipeline error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidatePipelineReportsEveryProblem(t *testing.T) {
	p := &params.Pipeline{Steps: []params.PipelineStep{
		{Id: "a"},
		{Id: "b", Run: "true", Charly: []string{"status"}},
	}}
	err := ValidatePipeline(p)
	if err == nil {
		t.Fatal("expected errors")
	}
	if !strings.Contains(err.Error(), "no execution arm") || !strings.Contains(err.Error(), "execution arms set") {
		t.Errorf("ValidatePipeline must report EVERY problem, got %q", err)
	}
}

func TestValidateWorkflowCycles(t *testing.T) {
	if err := ValidateWorkflowCycles(map[string][]string{"a": {"b"}, "b": {"c"}, "c": {}}); err != nil {
		t.Fatalf("ValidateWorkflowCycles on an acyclic graph: %v", err)
	}
	err := ValidateWorkflowCycles(map[string][]string{"a": {"b"}, "b": {"a"}})
	if err == nil || !strings.Contains(err.Error(), "workflow cycle: ") {
		t.Fatalf("ValidateWorkflowCycles = %v, want a workflow cycle naming the chain", err)
	}
}

// TestValidatePipelineLoopBodyRefScope pins the SCOPE a loop body is validated against:
// the body's OWN earlier sub-steps, on top of the enclosing earlier-step set — which is
// the scope the engine already gives it at run time. plugin-lobster's `runSubSteps` runs
// a body against a per-item COPY of the enclosing result set and does `scope[sub.Id] = res`
// after each sub-step, so sub-step N reads every sub-step before it. Validating a body
// against the pipeline's TOP-LEVEL ids only made the gate STRICTER than the engine and
// refused every multi-stage loop body (opencharly/sdk#441).
//
// The accepted cases are one per way a body chains: `when:`, a desugared `plan:` element,
// and a read of the ENCLOSING scope (which must not be lost by the narrower namespace).
func TestValidatePipelineLoopBodyRefScope(t *testing.T) {
	accepted := []struct {
		name string
		p    *params.Pipeline
	}{
		{
			name: "lanes body chains a when across three sub-steps",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a","b"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2, ItemVar: "pr",
					Steps: []params.PipelineSubStep{
						{Id: "a", Run: `echo "$pr"`},
						{Id: "b", When: "$a.ok", Run: "echo b"},
						{Id: "c", When: "$b.ok", Run: "echo c"},
					}},
			}},
		},
		{
			name: "for_each body chains a when",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a","b"]'`},
				{Id: "fan", ForEach: "$seed.json.items", ItemVar: "pr",
					Steps: []params.PipelineSubStep{
						{Id: "a", Run: `echo "$pr"`},
						{Id: "b", When: "$a.ok", Run: "echo b"},
					}},
			}},
		},
		{
			name: "lanes body chains a desugared plan ref",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "oracle", Run: "echo oracle"},
						{Id: "render", Plan: []spec.Step{{Run: "render", Op: spec.Op{Command: "echo $oracle.stdout"}}}},
						{Id: "gate", Plan: []spec.Step{{Run: "gate", Op: spec.Op{Command: "echo $render.stdout"}}}},
					}},
			}},
		},
		{
			name: "body still sees the ENCLOSING earlier step",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "s", When: "$seed.ok", Run: "true"},
					}},
			}},
		},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePipeline(tc.p); err != nil {
				t.Fatalf("ValidatePipeline rejected a chained loop body (%s): %v", tc.name, err)
			}
			// Lower validates first, so a green Lower proves the WHOLE load path a
			// `kind: pipeline` author hits — the gate plus the emitted .lobster pair.
			if _, _, err := Lower(tc.p, "/proj", "/proj/.opencharly/pipelines/lane", "/usr/local/bin/charly"); err != nil {
				t.Fatalf("Lower rejected a validated chained loop body (%s): %v", tc.name, err)
			}
		})
	}

	// The AUTHORED boundary: the YAML a `kind: pipeline` author writes must decode onto the
	// body and validate — a Go-struct-only test would pass while the authored `steps:` body
	// was dropped on decode.
	t.Run("authored chained body decodes and validates", func(t *testing.T) {
		const authored = `
description: chained lane body
steps:
  - id: seed
    run: printf '["a","b"]'
  - id: lane
    lanes: $seed.json.items
    batch_size: 16
    item_var: pr
    steps:
      - id: oracle
        run: echo "$pr"
      - id: render
        when: $oracle.ok
        run: echo render
`
		var p params.Pipeline
		if err := yaml.Unmarshal([]byte(authored), &p); err != nil {
			t.Fatalf("decoding the authored pipeline: %v", err)
		}
		if len(p.Steps) != 2 || len(p.Steps[1].Steps) != 2 {
			t.Fatalf("decoded %d steps / %d sub-steps, want 2 / 2", len(p.Steps), len(p.Steps[1].Steps))
		}
		if err := ValidatePipeline(&p); err != nil {
			t.Fatalf("ValidatePipeline rejected the authored chained body: %v", err)
		}
	})

	// The CONTROLS: widening the scope must widen ONLY the scope. The earlier-step-only
	// rule still refuses a body reference to nothing, to a LATER sibling, to itself, and a
	// top-level read of a sub-step (the per-item scope never merges back into the run's).
	rejected := []struct {
		name string
		p    *params.Pipeline
		want string
	}{
		{
			name: "body references a step that does not exist",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "a", Run: "true"},
						{Id: "b", When: "$nope.ok", Run: "true"},
					}},
			}},
			want: `step "lane" sub-step "b".when: reference $nope.* names a step that is not declared earlier`,
		},
		{
			name: "body references a LATER sibling",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "a", When: "$b.ok", Run: "true"},
						{Id: "b", Run: "true"},
					}},
			}},
			want: `step "lane" sub-step "a".when: reference $b.* names a step that is not declared earlier`,
		},
		{
			name: "body references itself",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "a", When: "$a.ok", Run: "true"},
					}},
			}},
			want: `step "lane" sub-step "a".when: reference $a.* names a step that is not declared earlier`,
		},
		{
			name: "sub-step plan references a later sibling",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{
						{Id: "a", Plan: []spec.Step{{Run: "x", Op: spec.Op{Command: "echo $b.stdout"}}}},
						{Id: "b", Run: "true"},
					}},
			}},
			want: `step "lane" sub-step "a".plan: reference $b.* names a step that is not declared earlier`,
		},
		{
			name: "a TOP-LEVEL step references a sub-step",
			p: &params.Pipeline{Steps: []params.PipelineStep{
				{Id: "seed", Run: `printf '["a"]'`},
				{Id: "lane", Lanes: "$seed.json.items", BatchSize: 2,
					Steps: []params.PipelineSubStep{{Id: "s", Run: "true"}}},
				{Id: "after", When: "$s.ok", Run: "true"},
			}},
			want: `step "after".when: reference $s.* names a step that is not declared earlier`,
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePipeline(tc.p)
			if err == nil {
				t.Fatalf("ValidatePipeline accepted an invalid loop body (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidatePipeline error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestValidatePipelineNamesTheConfigKnobReplacement pins defect 1 of opencharly/sdk#437: a
// `$<knob>.*` reference inside a step is answered with the MISLEADING "names a step that is
// not declared earlier", which sends the author looking for a step to declare. There is no
// such step to declare, and there is nothing to restore: the cutover moved the former
// pipeline knob blocks into `config:` as opaque DATA and deleted the POPULATOR that made a
// knob readable (plugin-pipeline's retired executor.go), leaving readers with a nil map —
// so a reference that got past this gate would render LITERAL TEXT at run time. The honest
// answer is a refusal that NAMES THE REPLACEMENT — this test asserts the MESSAGE TEXT, not
// merely a non-nil error, because a gate that refuses with the wrong reason costs the author
// a whole round in the wrong direction.
func TestValidatePipelineNamesTheConfigKnobReplacement(t *testing.T) {
	p := &params.Pipeline{
		// `report:` is the consumer's real knob block, carried under `config:` by the
		// reshape exactly as plugin-migrate emits it.
		Config: map[string]any{"report": map[string]any{"bed_template": "bed-$pr:\n  gate:\n"}},
		Steps: []params.PipelineStep{
			{Id: "render", Plan: []spec.Step{{
				Run: "render the bed",
				Op:  spec.Op{Command: "charly box build -f $report.bed_template"},
			}}},
		},
	}
	err := ValidatePipeline(p)
	if err == nil {
		t.Fatal("ValidatePipeline accepted a config-knob reference inside a step")
	}
	got := err.Error()
	for _, want := range []string{
		`step "render".plan: reference $report.* reads the entity-level config knob "report"`,
		"config: is authored data with no expression form",
		"Author the knob's field VALUE where it is used instead",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ValidatePipeline error = %q, want it to contain %q", got, want)
		}
	}
	// The displaced message must be GONE, not merely accompanied: a reader who sees both
	// would still be told to go and declare a step.
	if strings.Contains(got, "names a step that is not declared earlier") {
		t.Errorf("ValidatePipeline answered a knob reference with the step message: %q", got)
	}
}

// TestValidatePipelineKnobNameAsAnEarlierStepIsAStepRef is the CONTROL for the classifier
// above: the SAME reference with the SAME config key is a plain step reference once a step
// of that id is declared earlier. Without this arm, a validator that hard-coded the knob
// names (or one that simply refused every unknown name with the knob message) would pass.
func TestValidatePipelineKnobNameAsAnEarlierStepIsAStepRef(t *testing.T) {
	p := &params.Pipeline{
		Config: map[string]any{"report": map[string]any{"bed_template": "x"}},
		Steps: []params.PipelineStep{
			{Id: "report", Run: "true"},
			{Id: "render", Plan: []spec.Step{{
				Run: "render the bed",
				Op:  spec.Op{Command: "charly box build -f $report.bed_template"},
			}}},
		},
	}
	if err := ValidatePipeline(p); err != nil {
		t.Fatalf("ValidatePipeline rejected a reference to a DECLARED step named like a config knob: %v", err)
	}
}

// TestValidatePipelineNamesTheEnvReplacement pins the OTHER half of the retired-grammar
// boundary: the bare `$env.NAME` spelling is NOT accepted (its value would have to be spliced
// into the expression as text), but it now refuses with the form that IS in the grammar —
// `${env.NAME}` — instead of "names a step that is not declared earlier".
func TestValidatePipelineNamesTheEnvReplacement(t *testing.T) {
	p := &params.Pipeline{Steps: []params.PipelineStep{
		{Id: "publish", When: "$env.EVAL_PUBLISH != true", Run: "true"},
	}}
	err := ValidatePipeline(p)
	if err == nil {
		t.Fatal("ValidatePipeline accepted the retired $env.NAME spelling in a when:")
	}
	got := err.Error()
	for _, want := range []string{
		`step "publish".when: reference $env.* is the retired entity-env spelling`,
		"write ${env.NAME}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ValidatePipeline error = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "names a step that is not declared earlier") {
		t.Errorf("ValidatePipeline answered the retired env spelling with the step message: %q", got)
	}
}

// TestValidatePipelineAcceptsTheRestoredEnvGuard is the END-TO-END arm for the restored
// expression: the consumer's exact migrated guard must produce ZERO errors (exact emptiness,
// not a substring — a stray second error would still reject the entity).
func TestValidatePipelineAcceptsTheRestoredEnvGuard(t *testing.T) {
	p := &params.Pipeline{Steps: []params.PipelineStep{
		{Id: "oracle", Run: "true"},
		{Id: "publish", When: "!(${env.EVAL_PUBLISH} != true)", Run: "true"},
	}}
	if err := ValidatePipeline(p); err != nil {
		t.Fatalf("ValidatePipeline rejected the migrated env guard: %v", err)
	}
}
