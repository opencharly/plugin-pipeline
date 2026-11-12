package pluginpipeline

// pipeline_schema_corpus_test.go — the closedness corpus for the `kind: pipeline`
// AUTHORED body, which is THIS repo's own CUE (`schema/pipeline.cue`'s
// `#PipelineValue` / `#PipelineApproval` / `#PipelineSchedule` / `#PipelineStep`).
//
// MOVED HERE FROM opencharly/spec AS PART OF spec#304 unit 1. While the body defs lived
// in the contract module, `spec/pipeline_test.go` and `spec/workflow_envelope_test.go`
// held these cases. Unit 1 moved the defs into this plugin's schema (leg 2) and unit 1's
// last leg deletes them from `spec`, so the corpus moves with the defs it judges — a def
// that no longer exists cannot be covered from the repo that no longer declares it, and
// deleting the corpus instead would be a silent coverage loss.
//
// WHY IT IS A CLOSEDNESS CORPUS AND NOT A DECODE ROUND-TRIP (R2): the closure lives ONLY
// in CUE. `cue exp gengotypes` renders every one of these fields as a plain Go `string` /
// `[]Step`, so a Go-level round-trip cannot tell a closed body from an open one — a test
// that only decodes VALID documents passes identically with the closedness reverted and
// therefore proves nothing. Every reject case below unifies a CONCRETE value into the def
// and demands the same judgement the host's value gate makes on an authored charly.yml,
// so it FAILS the moment a field is reopened or a retired alias is re-accepted.
//
// The schema is composed the way THIS plugin serves it — `schemacompose.ServedSchema`
// over the embedded `schema/` (the same call `schema_test.go` makes) — so the corpus
// exercises the shipped unit, never a hand-written excerpt of it.

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

// TestPipelineValueClosedness is the authored `kind: pipeline` body's closedness corpus.
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
		// --- accept: the lobster form ---
		{
			"shell step accepted",
			`{description: "d", steps: [{id: "s1", run: "echo hi"}]}`,
			false,
		},
		{
			"full flow keys accepted",
			`{description: "d", steps: [{
				id: "s1", run: "echo hi",
				when: "$prev.json.ok == true",
				env: {FOO: "bar", BAR: "${pr}"},
				cwd: "/tmp", stdin: "$prev.stdout",
				timeout_ms: 5000, on_error: "continue",
				retry: {max: 3, delay_ms: 100, factor: 2.0},
				approval: {message: "ok?", timeout_ms: 60000},
			}]}`,
			false,
		},
		{
			"terse approval union accepted",
			`{description: "d", steps: [{id: "s1", run: "echo hi", approval: true}]}`,
			false,
		},
		{
			"parallel accepted",
			`{description: "d", steps: [{id: "s1", parallel: {wait: "any", branches: [
				{id: "b1", run: "echo a"}, {id: "b2", run: "echo b"},
			]}}]}`,
			false,
		},
		{
			"for_each with companions accepted",
			`{description: "d", steps: [{
				id: "s1", for_each: "$prev.json.items",
				item_var: "it", index_var: "i", batch_size: 2, pause_ms: 10,
				steps: [{id: "inner", run: "echo $it"}],
			}]}`,
			false,
		},
		{
			"lanes with the pooled fan-out companions accepted",
			`{description: "d", steps: [{
				id: "s1", lanes: "$prev.json.items",
				item_var: "it", index_var: "i", batch_size: 2,
				steps: [{id: "inner", run: "echo $it"}],
			}]}`,
			false,
		},

		// --- accept: the charly form (the FULL #Step grammar, inside plan:) ---
		{
			"plan with a builtin verb accepted",
			`{description: "d", steps: [{id: "s1", plan: [{run: "echo hi"}, {mkdir: "/tmp/wf"}]}]}`,
			false,
		},
		{
			"plan check intent accepted",
			`{description: "d", steps: [{id: "s1", plan: [{check: "api is up", timeout: "30s"}]}]}`,
			false,
		},
		{
			"plan agent-run intent accepted",
			`{description: "d", steps: [{id: "s1", plan: [{"agent-run": "summarise the diff"}]}]}`,
			false,
		},
		{
			"argv step accepted",
			`{description: "d", steps: [{id: "s1", charly: ["task", "list"]}]}`,
			false,
		},
		{
			"argv step as a bare string accepted",
			`{description: "d", steps: [{id: "s1", charly: "task list"}]}`,
			false,
		},

		// --- accept: the workflow level ---
		{
			"workspace fields + a schedule trigger accepted",
			`{
				description: "d",
				engine: "lobster",
				args: {pr: {description: "the PR", required: true}},
				env: {CI: "1"},
				cwd: "/work",
				cost_limit: 2.5,
				triggers: [{manual: true}, {schedule: {cron: "0 9 * * 1-5", timezone: "UTC", persistent: true, args: {branch: "main"}}}],
				config: {llm: {model: "x"}, redo: {max: 1}},
				entities: {"my-task": {task: {description: "t", plan: [{run: "true"}]}}},
				steps: [{id: "s1", run: "echo hi"}],
			}`,
			false,
		},

		// --- reject: exactly-one exec arm is a GO rule, but the arms must not silently
		//     co-exist in a way the schema blesses ---
		{
			"two lobster exec arms are a Go rule, so CUE must NOT reject them here",
			`{description: "d", steps: [{id: "s1", run: "echo hi", pipeline: "other"}]}`,
			false, // documented: the XOR is this repo's workflowkit.ValidatePipeline's job (see the schema header)
		},

		// --- reject ---
		{
			"unknown step key rejected",
			`{description: "d", steps: [{id: "s1", run: "echo hi", bogus: 1}]}`,
			true,
		},
		{
			"the retired `stages:` key rejected",
			`{description: "d", stages: [{kind: "command", command: "echo hi"}]}`,
			true,
		},
		{
			"the retired `command:` alias rejected",
			`{description: "d", steps: [{id: "s1", command: "echo hi"}]}`,
			true,
		},
		{
			"the retired `skip_when:` alias rejected",
			`{description: "d", steps: [{id: "s1", run: "echo hi", skip_when: "@x.y == 1"}]}`,
			true,
		},
		{
			"unknown workspace key rejected",
			`{description: "d", concurrency: {lanes: 2}, steps: [{id: "s1", run: "echo hi"}]}`,
			true, // `concurrency:` is CLI/`for_each` semantics now, never an entity key
		},
		{
			"a step with no id rejected",
			`{description: "d", steps: [{run: "echo hi"}]}`,
			true,
		},
		{
			"a step with no exec arm is a Go rule too, so CUE must NOT reject it here either",
			`{description: "d", steps: [{id: "s1", when: "true"}]}`,
			false, // workflowkit.ValidatePipeline rejects both zero arms and two arms; see the schema header
		},
		{
			"an empty steps list rejected",
			`{description: "d", steps: []}`,
			true,
		},
		{
			"an empty charly argv rejected",
			`{description: "d", steps: [{id: "s1", charly: []}]}`,
			true,
		},
		{
			"a bad on_error value rejected",
			`{description: "d", steps: [{id: "s1", run: "echo hi", on_error: "explode"}]}`,
			true,
		},
		{
			"a zero timeout_ms rejected",
			`{description: "d", steps: [{id: "s1", run: "echo hi", timeout_ms: 0}]}`,
			true,
		},
		{
			"a schedule trigger with no cron rejected",
			`{description: "d", triggers: [{schedule: {timezone: "UTC"}}], steps: [{id: "s1", run: "echo hi"}]}`,
			true,
		},
		{
			"a parallel with no branches rejected",
			`{description: "d", steps: [{id: "s1", parallel: {}}]}`,
			true,
		},
		{
			"a nested for_each in a substep rejected",
			`{description: "d", steps: [{id: "s1", for_each: "$x", steps: [
				{id: "inner", run: "echo hi", for_each: "$y"},
			]}]}`,
			true, // #PipelineSubStep is non-recursive by construction (lobster's own rule)
		},
		{
			"a nested lanes in a substep rejected",
			`{description: "d", steps: [{id: "s1", for_each: "$x", steps: [
				{id: "inner", run: "echo hi", lanes: "$y", batch_size: 2},
			]}]}`,
			true, // #PipelineSubStep is non-recursive by construction (lobster's own rule)
		},
		{
			"an empty lanes operand rejected",
			`{description: "d", steps: [{id: "s1", lanes: "", batch_size: 2}]}`,
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

// TestAuthoredSurfaceCarriesTheRuntimeArgList holds the authored defs this repo now owns
// to the admission the engine wire gives them — moved here from spec's
// `workflow_envelope_test.go` with the defs.
//
// AN ARG IS THE ONLY CHANNEL a runtime value reaches an engine through, and an authored
// `#PipelineSchedule.args` / a step's `workflow_args` is normalized VERBATIM into the
// engine's `map[string]any`, so a scalar-only spelling here would be a second rule for
// one question. The bound is pinned in both directions: a nested map or a list of lists
// has no rendering into a per-arg binding, so it must be rejected here rather than
// validate and then be dropped at execution time.
//
// The last case is the contract that keeps the engine envelope IR-only: the authored
// `approval:` must NOT gain the engine's enrichment fields. If someone later mirrors
// them here, that IS an authored wire-key change (every authored document stops
// unifying against the CLOSED schema), and this test says so first.
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
		{
			"a sub-workflow step carries a list arg",
			"#PipelineStep",
			`{id: "s1", workflow: "child", workflow_args: {prs: ["101", "102"]}}`,
			false,
		},
		{
			"a sub-workflow step still rejects a nested map arg",
			"#PipelineStep",
			`{id: "s1", workflow: "child", workflow_args: {prs: {nested: "x"}}}`,
			true,
		},
		{
			"the authored approval def does not gain the engine fields",
			"#PipelineApproval",
			`{message: "ok?", items: [1], approval_id: "a1b2c3d4"}`,
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

// The authored schedule's generated field must carry the RUNTIME-value type, not a
// string: this assignment fails to compile if the def's `@go(Args,type=map[string]any)`
// annotation ever narrows back to a scalar map. (The same compile-time assertion lived in
// spec's `workflow_arg_list_wire_test.go`; it moved here with the def.)
var _ map[string]any = params.PipelineSchedule{}.Args
