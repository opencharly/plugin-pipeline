package pluginpipeline

// schema_test.go — the served-schema contract of THIS plugin, proved at the two
// boundaries the contract actually has.
//
// The repo's other gates do not cover this: `charly box validate` checks the candy
// manifest and its `plugin:` block — and it does NOT compile the served schema, so
// it stays green over a dangling definition AND over an unresolvable import — while
// `go test` of the verbs exercises the providers, never the schema. A plugin whose
// served schema stopped composing, or whose `#PipelineAgentInput` stopped
// referencing the contract's `#LLMSpec`, would keep every one of those green while
// failing at start-up on the host. So this file composes the schema the way each
// half of the contract composes it, and judges the SAME authored documents through
// both:
//
//   - SERVE side — `NewMeta().Describe` → `sdk.BuildCapabilities` →
//     `schemacompose.ServedSchema`, exactly the call the provider makes when it
//     advertises its capabilities;
//   - HOST side — the `Capabilities.schema_cue` body that travelled over the wire,
//     composed by `schemacompose.ServedText` under the DERIVED module declaration
//     (charly's registerPluginUnitSchema calls the same function on that same text).
//
// The fixture is the equivalence record AND the repo's own authored documents: the
// agent-stage shapes this plugin's conformance tests author (every llm field, the
// outputs/cache/redo/skills blocks) plus the shapes it must keep rejecting. It is
// the same document set the cutover measured against `origin/main`'s schema, where
// ZERO verdicts moved — six accepted, ten rejected, both directions.
//
// The mirror this file replaces (schema_parity_test.go) existed only to keep the
// local copy of the contract's six LLM defs honest. The import makes drift
// impossible, so the guard retires with the copy: there is nothing left to guard.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/schemacompose"
	"github.com/opencharly/spec/spec"
)

// The plugin's params must carry the CONTRACT's wire type, not a copy of it: this
// assignment fails to compile if the schema ever stops importing schema.#LLMSpec
// and goes back to declaring a local twin.
var _ spec.LLMSpec = params.PipelineAgentInput{}.Llm

const agentInputDef = "#PipelineAgentInput"

// llmVocabularyDefs are the six definitions that moved to the contract module.
// None of them may be declared by this plugin's schema any more.
var llmVocabularyDefs = []string{
	"#LLMSpec",
	"#LLMParams",
	"#LLMResponseFormat",
	"#LLMReasoning",
	"#LLMStreamOptions",
	"#LLMNamedToolChoice",
}

// servedUnit composes the plugin's embedded schema the way the SERVE side does.
func servedUnit(t *testing.T) cue.Value {
	t.Helper()
	v, err := schemacompose.ServedSchema(nil, schemaFS, "schema")
	if err != nil {
		t.Fatalf("the serve side cannot compose candy/plugin-pipeline/schema: %v", err)
	}
	return v
}

// hostUnit takes the capabilities the provider actually advertises over Describe,
// then composes the schema body the host receives the way the HOST gate does.
func hostUnit(t *testing.T) (string, cue.Value) {
	t.Helper()
	caps, err := NewMeta().Describe(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if caps == nil {
		t.Fatal("Describe returned no capabilities")
	}
	body := caps.GetSchemaCue()
	if strings.TrimSpace(body) == "" {
		t.Fatal("the plugin advertises an EMPTY schema_cue")
	}
	v, err := schemacompose.ServedText(nil, body)
	if err != nil {
		t.Fatalf("the host side cannot compose the served schema_cue: %v", err)
	}
	return body, v
}

// topLevelDefs returns the top-level definition names a composed unit declares.
func topLevelDefs(t *testing.T, v cue.Value) map[string]bool {
	t.Helper()
	it, err := v.Fields(cue.Definitions(true))
	if err != nil {
		t.Fatalf("reading the composed unit's definitions: %v", err)
	}
	out := map[string]bool{}
	for it.Next() {
		out[it.Selector().String()] = true
	}
	return out
}

// judge applies the SAME rule the host's validator applies (plugin_loader.go:
// `def.Unify(input).Validate(cue.Concrete(true))`) and reports "<nil>" for an
// accepted document.
func judge(t *testing.T, unit cue.Value, doc string) string {
	t.Helper()
	d := unit.LookupPath(cue.ParsePath(agentInputDef))
	if d.Err() != nil {
		t.Fatalf("composed unit has no %s: %v", agentInputDef, d.Err())
	}
	in := cuecontext.New().CompileBytes([]byte(doc))
	if in.Err() != nil {
		t.Fatalf("fixture document does not compile: %v\n%s", in.Err(), doc)
	}
	if err := d.Unify(in).Validate(cue.Concrete(true)); err != nil {
		return err.Error()
	}
	return "<nil>"
}

// agentInputFixture is the equivalence record: the agent-stage documents this
// repo's own conformance tests author (every llm field, outputs/cache/redo/skills)
// plus every shape the input def accepted or rejected before the six LLM defs moved
// behind the import.
var agentInputFixture = []struct {
	name  string
	doc   string
	valid bool
}{
	{
		name:  "minimal agent stage",
		doc:   `{"prompt":"review the diff and report regressions"}`,
		valid: true,
	},
	{
		name:  "entity llm block (endpoint + model + key + params)",
		doc:   `{"prompt":"q","llm":{"base_url":"http://localhost:11434/v1","model":"qwen3:8b","api_key":"$env.OPENAI_API_KEY","params":{"max_tokens":300,"reasoning_effort":"none"}}}`,
		valid: true,
	},
	{
		name:  "the full #LLMParams surface (response_format + tool_choice + extra)",
		doc:   `{"prompt":"q","llm":{"params":{"temperature":0.2,"top_p":0.9,"seed":7,"stop":["\n\n"],"response_format":{"type":"json_schema","json_schema":{"name":"v","schema":{"type":"object"},"strict":true}},"tool_choice":"auto","extra":{"service_tier":"flex"}}}}`,
		valid: true,
	},
	{
		name:  "typed outputs + tools + skills + cache + redo",
		doc:   `{"prompt":"q","skills":{"corpus":"x"},"tools":["get_pr_meta"],"outputs":{"verdict":{"type":"string"}},"max_turns":3,"cache":{"path":"/plan.yml","key":"k"},"redo":{"max":2}}`,
		valid: true,
	},
	{
		name:  "empty llm block",
		doc:   `{"prompt":"q","llm":{}}`,
		valid: true,
	},
	{
		name:  "connection knobs (timeout, idle_timeout, retries, headers)",
		doc:   `{"prompt":"q","llm":{"timeout":"10m","idle_timeout":"45s","max_retries":2,"headers":{"HTTP-Referer":"https://example.test"}}}`,
		valid: true,
	},
	{
		name:  "missing prompt",
		doc:   `{"skills":{"corpus":"x"}}`,
		valid: false,
	},
	{
		name:  "unknown key at the input level",
		doc:   `{"prompt":"q","bogus":1}`,
		valid: false,
	},
	{
		name:  "unknown key inside llm",
		doc:   `{"prompt":"q","llm":{"bogus":1}}`,
		valid: false,
	},
	{
		name:  "string max_retries (the contract's field is an int)",
		doc:   `{"prompt":"q","llm":{"max_retries":"2"}}`,
		valid: false,
	},
	{
		name:  "temperature out of the contract's 0..2 bound",
		doc:   `{"prompt":"q","llm":{"params":{"temperature":3}}}`,
		valid: false,
	},
	{
		name:  "bad reasoning_effort",
		doc:   `{"prompt":"q","llm":{"params":{"reasoning_effort":"extreme"}}}`,
		valid: false,
	},
	{
		name:  "string prompt",
		doc:   `{"prompt":1}`,
		valid: false,
	},
	{
		name:  "max_turns must be positive",
		doc:   `{"prompt":"q","max_turns":0}`,
		valid: false,
	},
	{
		name:  "unknown output type",
		doc:   `{"prompt":"q","outputs":{"v":{"type":"number"}}}`,
		valid: false,
	},
	{
		name:  "string tools",
		doc:   `{"prompt":"q","tools":"x"}`,
		valid: false,
	},
}

// TestServedSchemaLoadsAndCarriesTheImport is the load proof R7 asks for at this
// boundary: the schema COMPOSES as one CUE module instance (the serve-side compose
// fails loudly if the import cannot resolve), the wire body carries that import,
// and the definitions this plugin declares are its OWN — the six the contract owns
// are gone rather than kept beside the import.
func TestServedSchemaLoadsAndCarriesTheImport(t *testing.T) {
	serveDefs := topLevelDefs(t, servedUnit(t))
	if !serveDefs[agentInputDef] {
		t.Fatalf("the served schema declares no %s: %v", agentInputDef, serveDefs)
	}
	for _, deleted := range llmVocabularyDefs {
		if serveDefs[deleted] {
			t.Errorf("the served schema still DECLARES %s — the restated twin must be deleted, not kept beside the import", deleted)
		}
	}

	body, host := hostUnit(t)
	if !strings.Contains(body, `import "github.com/opencharly/spec/schema"`) {
		t.Errorf("the served schema_cue does not import the contract module:\n%s", body)
	}
	hostDefs := topLevelDefs(t, host)
	if !hostDefs[agentInputDef] {
		t.Fatalf("the host-composed unit declares no %s: %v", agentInputDef, hostDefs)
	}
	for _, deleted := range llmVocabularyDefs {
		if hostDefs[deleted] {
			t.Errorf("the host-composed unit DECLARES %s — the wire body must carry only the plugin's own defs", deleted)
		}
	}
}

// TestAgentInputFixtureJudgedIdenticallyByBothHalves is the equivalence proof:
// the authored documents are judged by the value the serve side composes AND by
// the value the host composes from the wire, both must agree, and both must agree
// with the verdicts the schema gave before the cutover (six accepted, ten
// rejected).
func TestAgentInputFixtureJudgedIdenticallyByBothHalves(t *testing.T) {
	serve := servedUnit(t)
	_, host := hostUnit(t)

	accepted, rejected := 0, 0
	for _, tc := range agentInputFixture {
		gotServe := judge(t, serve, tc.doc)
		gotHost := judge(t, host, tc.doc)
		t.Logf("%-58s accept=%v verdict=%s", tc.name, tc.valid, gotServe)
		if gotServe != gotHost {
			t.Errorf("%s: the two halves disagree\n serve: %s\n host:  %s\n doc: %s", tc.name, gotServe, gotHost, tc.doc)
		}
		if tc.valid {
			accepted++
			if gotServe != "<nil>" {
				t.Errorf("%s must be ACCEPTED, got: %s", tc.name, gotServe)
			}
			continue
		}
		rejected++
		if gotServe == "<nil>" {
			t.Errorf("%s must be REJECTED, but the schema accepted it", tc.name)
		}
	}
	if accepted != 6 || rejected != 10 {
		t.Fatalf("the fixture must keep its measured shape (6 accept / 10 reject), got %d/%d", accepted, rejected)
	}
	t.Logf("fixture: %d accepted, %d rejected, both halves identical", accepted, rejected)
}

// TestImportIsEnforcedByTheImportedDefinition proves the import is LIVE rather
// than decorative: documents that violate the CONTRACT's own constraints are
// rejected, and the rejection names the imported definition's field.
func TestImportIsEnforcedByTheImportedDefinition(t *testing.T) {
	serve := servedUnit(t)
	if got := judge(t, serve, `{"prompt":"q","llm":{"params":{"temperature":9}}}`); got == "<nil>" || !strings.Contains(got, "temperature") {
		t.Errorf("a temperature above the contract's bound must be rejected naming the field, got: %s", got)
	}
	if got := judge(t, serve, `{"prompt":"q","llm":{"bogus":1}}`); got == "<nil>" || !strings.Contains(got, "bogus") {
		t.Errorf("an undeclared llm key must be rejected naming the field, got: %s", got)
	}
	if got := judge(t, serve, `{"prompt":"q","llm":{"base_url":"http://localhost:11434/v1","model":"m","params":{"temperature":0.5,"seed":1}}}`); got != "<nil>" {
		t.Fatalf("a conforming llm block must pass: %s", got)
	}
}

// TestUnservedImportFailsLoudly is the ANTI-EXPERIMENT: an import of a module the
// contract does not serve must make the compose fail, naming the unresolvable
// package AND the schema file — proving the import is resolved by the live
// composer and not silently ignored. The same call is what BuildCapabilities makes
// before a plugin advertises any capability, so this is the start-up failure a bad
// import produces.
//
// The file it names is the CONCATENATED `schema/plugin.cue` (schemaconcat joins the
// directory's files into the one body the wire carries, exactly as the host
// composes it), not the authoring file: that is the composed unit's identity, and
// the error carries the import's own position inside it.
func TestUnservedImportFailsLoudly(t *testing.T) {
	bad := fstest.MapFS{
		"schema/bad.cue": &fstest.MapFile{Data: []byte(`import "github.com/opencharly/not-a-served-module"

#Bad: {a: string}
`)},
	}
	_, err := schemacompose.ServedSchema(nil, bad, "schema")
	if err == nil {
		t.Fatal("an import of an unserved module composed successfully — the import is not being resolved")
	}
	if !strings.Contains(err.Error(), "not-a-served-module") {
		t.Errorf("the compose failure must name the unresolvable import, got: %v", err)
	}
	if !strings.Contains(err.Error(), "schema/plugin.cue") {
		t.Errorf("the compose failure must name the schema file it judged, got: %v", err)
	}
}

// TestParamsTypesAreTheContractTypes states the same relation in one place a
// reader can read without a compiler: the generated field IS the contract's type.
func TestParamsTypesAreTheContractTypes(t *testing.T) {
	var in params.PipelineAgentInput
	in.Llm = spec.LLMSpec{Model: "m"}
	in.Llm.Params = spec.LLMParams{User: "u"}
	if got := fmt.Sprintf("%T", in.Llm); got != "spec.LLMSpec" {
		t.Fatalf("params.PipelineAgentInput.Llm is %s, want spec.LLMSpec", got)
	}
	if got := fmt.Sprintf("%T", in.Llm.Params); got != "spec.LLMParams" {
		t.Fatalf("params.PipelineAgentInput.Llm.Params is %s, want spec.LLMParams", got)
	}
}
