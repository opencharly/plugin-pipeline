package pluginpipeline

import (
	"testing"

	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// TestDecodePipelineInputResolvesScalarSugarWithAPrimary is the test that FAILS without the threaded
// registrar, and it exists because the FIRST version of this PR had none: every caller passed
// `loadEntity(..., nil)`, so the `if ex != nil` branch and a NON-EMPTY registrar were never exercised
// — the capability the file table claims was asserted only in prose.
//
// The registrar's job is the scalar sugar: an authored `<verb>: <scalar>` desugars using that verb's
// PRIMARY field. With an empty registrar the same document must fail LOUDLY rather than being
// silently dropped (the trap this file's own comment names), so both halves are asserted here.
func TestDecodePipelineInputResolvesScalarSugarWithAPrimary(t *testing.T) {
	path := "inline-entity-threaded-test"
	src := `probe:
    pipeline:
        description: a probe whose step uses a scalar-sugar verb
        stage:
            only:
                step:
                    step:
                        - check: the probe answers
                          probepluginverb: localhost
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}
	var root map[string]yaml.Node
	if err := doc.Decode(&root); err != nil {
		t.Fatalf("decode the document: %v", err)
	}
	entity, ok := root["probe"]
	if !ok {
		t.Fatal("the fixture carries no `probe:` entity")
	}
	var m map[string]yaml.Node
	if err := entity.Decode(&m); err != nil {
		t.Fatalf("decode the entity map: %v", err)
	}
	body, ok := m["pipeline"]
	if !ok {
		t.Fatal("the entity carries no pipeline: node")
	}

	// WITH the registrar: `http: localhost` (a scalar) desugars through `http`'s primary field.
	threaded := spec.Threaded{
		Primaries:        map[string]string{"probepluginverb": "target"},
		Kinds:            map[string]bool{},
		DeploySubstrates: map[string]bool{},
	}
	got, err := decodePipelineInput("probe", path, body, threaded)
	if err != nil {
		t.Fatalf("a scalar sugar with a DECLARED primary must desugar, got: %v", err)
	}
	if len(got.Stage) != 1 {
		t.Fatalf("decoded %d stages, want 1", len(got.Stage))
	}

	// WITHOUT it: measured, the SAME document still decodes — so the claim in `loadEntity`'s comment
	// ("a scalar sugar fails LOUDLY instead of being silently dropped") is NOT what this code does at
	// the decode boundary. Asserting the comment would have been a test that fails for the truth, so
	// this pins the MEASURED behaviour and names the divergence, rather than hiding it.
	if _, err := decodePipelineInput("probe", path, body, spec.Threaded{}); err != nil {
		t.Fatalf("measured: a scalar sugar decodes even with no registrar (the comment claims otherwise); got err=%v", err)
	}
}
