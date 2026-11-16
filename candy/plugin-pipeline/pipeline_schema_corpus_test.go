package pluginpipeline

// pipeline_schema_corpus_test.go — the closedness corpus for the `kind: pipeline`
// AUTHORED body, which is THIS repo's own CUE (`schema/pipeline.cue`'s `#PipelineValue` /
// `#PipelineSchedule`) plus the CONTRACT's stage vocabulary it imports
// (`spec/schema/stage.cue`: `#Stage` / `#StageAlt` / `#StageAsk` / `#Retry` / `#Lane`).
//
// P26 UNIT 2 RESHAPED IT: the authored body is `stage:` (a MAP of stages), and the stage's
// fields are the contract's — there is no authored lobster step grammar left to cover, so
// the cases that pinned `steps:`/`#PipelineStep`/`#PipelineApproval` retire WITH those
// defs (R5). What replaces them covers the surface that exists: the four arms, every
// scheduling field, and the closedness that a typo must keep failing.
//
// WHY IT IS A CLOSEDNESS CORPUS AND NOT A DECODE ROUND-TRIP (R2): the closure lives ONLY
// in CUE. `cue exp gengotypes` renders every one of these fields as a plain Go `string` /
// `[]Step`, so a Go-level round-trip cannot tell a closed body from an open one — a test
// that only decodes VALID documents passes identically with the closedness reverted and
// therefore proves nothing. Every reject case below unifies a CONCRETE value into the def
// and demands the same judgement the host's value gate makes on an authored charly.yml,
// so it FAILS the moment a field is reopened or a retired alias is re-accepted.
//
// The schema is composed the way THIS plugin serves it — `schemacompose.ServedSchema` over
// the embedded `schema/` (the same call `schema_test.go` makes) — so the corpus exercises
// the shipped unit, never a hand-written excerpt of it.

import (
	"testing"

	"cuelang.org/go/cue"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// pipelineValueUnify unifies a concrete value into a named def of the composed unit and
// reports the resulting error (nil = accepted).
func pipelineValueUnify(t *testing.T, schema cue.Value, defName, value string) error {
	t.Helper()
	def := schema.LookupPath(cue.ParsePath(defName))
	if err := def.Err(); err != nil {
		t.Fatalf("lookup %s: %v", defName, err)
	}
	val := schema.Context().CompileString(value)
	if err := val.Err(); err != nil {
		t.Fatalf("compile value: %v", err)
	}
	return def.Unify(val).Validate(cue.Concrete(false), cue.Final())
}

// TestPipelineValueClosedness is the authored `kind: pipeline` body's closedness corpus:
// the four arms and every scheduling field are ADMITTED, and every shape the contract or
// this repo's body closes is REFUSED.
func TestPipelineValueClosedness(t *testing.T) {
	schema := servedUnit(t)
	def := schema.LookupPath(cue.ParsePath("#PipelineValue"))
	if err := def.Err(); err != nil {
		t.Fatalf("lookup #PipelineValue: %v", err)
	}

	for _, tc := range []struct {
		name   string
		value  string
		reject bool
	}{
		// --- admitted: the four arms ---
		{
			"a step arm",
			`{description: "d", stage: {build: {step: {step: [{run: "build it", command: "make"}]}}}}`,
			false,
		},
		{
			"a pipeline arm",
			`{description: "d", stage: {verify: {pipeline: {pipeline: "verify"}}}}`,
			false,
		},
		{
			"a deployment arm",
			`{description: "d", stage: {bed: {deployment: {deployment: ["hello-pod"]}}}}`,
			false,
		},
		{
			"an ask arm with its answer channel",
			`{description: "d", stage: {confirm: {ask: {prompt: "publish?", answer: {expect: {equals: "yes"}}}}}}`,
			false,
		},
		// --- admitted: the scheduling fields, in the contract's spelling ---
		{
			"every scheduling field",
			`{description: "d", args: {beds: {description: "x"}},
			  stage: {gate: {
				after: ["build"], when: {publish: {equals: "yes"}},
				timeout: "30m", retry: {attempt: 3, interval: "5s"},
				on_error: "continue", lane: {over: "beds"}, concurrency: 2,
				step: {step: [{run: "check", command: "charly check run $LANE_ITEM"}]},
			  }}}`,
			false,
		},
		{
			"an empty stage map is a Go rule, not a CUE one",
			`{description: "d", stage: {}}`,
			false,
		},
		{
			"two arms is a Go rule too, so CUE must NOT reject it here",
			`{description: "d", stage: {s: {step: {step: [{run: "x", command: "true"}]}, pipeline: {pipeline: "other"}}}}`,
			false,
		},

		// --- refused: what the closure is for ---
		{"no stage at all rejected", `{description: "d"}`, true},
		{"no description rejected", `{stage: {s: {step: {step: [{run: "x", command: "true"}]}}}}`, true},
		{
			"an unknown stage field rejected",
			`{description: "d", stage: {s: {step: {step: [{run: "x", command: "true"}]}, bogus: 1}}}`,
			true,
		},
		{
			"the retired `steps:` body rejected",
			`{description: "d", steps: [{id: "s1", run: "true"}]}`,
			true,
		},
		{
			"the retired per-step `id:` rejected",
			`{description: "d", stage: {s: {id: "s1", step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"the retired expression `when:` rejected",
			`{description: "d", stage: {s: {when: "$x.y == 1", step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"an unknown on_error word rejected",
			`{description: "d", stage: {s: {on_error: "explode", step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"an unknown retry field rejected",
			`{description: "d", stage: {s: {retry: {max: 3}, step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"a timeout without a unit rejected",
			`{description: "d", stage: {s: {timeout: "30", step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"a lane without its parameter rejected",
			`{description: "d", stage: {s: {lane: {}, step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
		{
			"an ask without an answer channel rejected",
			`{description: "d", stage: {s: {ask: {prompt: "publish?"}}}}`,
			true,
		},
		{
			"an unknown unanswered outcome rejected",
			`{description: "d", stage: {s: {ask: {prompt: "p?", answer: {unanswered: "maybe"}}}}}`,
			true,
		},
		{
			"an unknown body field rejected",
			`{description: "d", concurrency: {lanes: 2}, stage: {s: {step: {step: [{run: "x", command: "true"}]}}}}`,
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The value must be compiled in the SAME context as the schema; unifying
			// across contexts is not meaningful.
			val := schema.Context().CompileString(tc.value)
			if err := val.Err(); err != nil {
				t.Fatalf("compile value: %v", err)
			}
			got := def.Unify(val).Validate(cue.Concrete(false), cue.Final())

			if tc.reject && got == nil {
				t.Errorf("ACCEPTED a value the schema must reject:\n%s", tc.value)
			}
			if !tc.reject && got != nil {
				t.Errorf("REJECTED a valid value: %v\n%s", got, tc.value)
			}
		})
	}
}

// TestAuthoredSurfaceCarriesTheRuntimeArgList holds the authored def this repo still owns
// to the admission the engine wire gives it.
//
// AN ARG IS THE ONLY CHANNEL a runtime value reaches an engine through, and the authored
// `#PipelineSchedule.args` is normalized VERBATIM into the engine's `map[string]any`, so a
// scalar-only spelling here would be a second rule for one question. The bound is pinned in
// both directions: a nested map has no rendering into a per-arg binding, so it must be
// rejected here rather than validate and then be dropped at execution time.
//
// THE STEP CASES RETIRED WITH THE LOBSTER STEP GRAMMAR (P26 unit 2): `#PipelineStep` and
// `#PipelineApproval` no longer exist, so `workflow_args` and the approval enrichment
// fields have no authored home to pin.
func TestAuthoredSurfaceCarriesTheRuntimeArgList(t *testing.T) {
	schema := servedUnit(t)

	for _, tc := range []struct {
		name   string
		def    string
		value  string
		reject bool
	}{
		{
			"the AUTHORED schedule carries a list arg",
			"#PipelineSchedule",
			`{cron: "0 * * * *", args: {prs: ["101"]}}`,
			false,
		},
		{
			"a nested map arg is rejected on the authored schedule too",
			"#PipelineSchedule",
			`{cron: "0 * * * *", args: {prs: {nested: "x"}}}`,
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pipelineValueUnify(t, schema, tc.def, tc.value)
			if tc.reject && got == nil {
				t.Errorf("%s ACCEPTED a value it must reject:\n%s", tc.def, tc.value)
			}
			if !tc.reject && got != nil {
				t.Errorf("%s REJECTED a valid value: %v\n%s", tc.def, got, tc.value)
			}
		})
	}
}

// The authored schedule's generated field must carry the RUNTIME-value type, not a string:
// this assignment fails to compile if the def's `@go(Args,type=map[string]any)` annotation
// ever narrows back to a scalar map.
var _ map[string]any = params.PipelineSchedule{}.Args
