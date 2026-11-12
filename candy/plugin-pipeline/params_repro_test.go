package pluginpipeline

// params_repro_test.go — the committed `params/cue_types_gen.go` IS the artifact of
// the schema, and this is the gate that keeps it one.
//
// The repo's `ci.yml` used to reproduce it with `cue exp gengotypes` over a `cat` of
// the schema file. That shape CANNOT judge this schema any more: the schema imports
// `github.com/opencharly/spec/schema`, and the `cue` CLI resolves modules only
// through an OCI registry — it has no way to reach the contract module the Go binary
// already links. So the gate moves to the canonical pipeline itself,
// `spec/schemaparams.Generate` (the SAME function `charly candy params` calls), which
// composes the schema through `schemacompose` and generates in process. Two
// properties, one test: the committed file is CURRENT, and a second run is
// byte-identical.

import (
	"bytes"
	"os"
	"testing"

	"github.com/opencharly/spec/schemaparams"
)

func TestGeneratedParamsAreCurrentAndReproducible(t *testing.T) {
	committed, err := os.ReadFile("params/cue_types_gen.go")
	if err != nil {
		t.Fatalf("reading the committed params: %v", err)
	}
	first, err := schemaparams.Generate("schema", "params")
	if err != nil {
		t.Fatalf("regenerating params from schema/: %v", err)
	}
	if !bytes.Equal(first, committed) {
		t.Fatalf("params/cue_types_gen.go is STALE: regenerate it with `charly candy params plugin-pipeline` (schema/pipeline.cue is the source; the file is generated, never hand-edited)")
	}
	second, err := schemaparams.Generate("schema", "params")
	if err != nil {
		t.Fatalf("second regeneration: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the schema→params pipeline is NOT reproducible: two runs of spec/schemaparams.Generate differ")
	}
}
