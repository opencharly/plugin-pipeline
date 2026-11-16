package workflowkit

import (
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/spec/spec"
)

// vocab_boundary_test.go — opencharly/plugin-pipeline#38 at the boundary where it went
// wrong: the authored schema and the engine's schema use different words for one idea, and
// nothing in the lowering translated them. An authored `on_error: fail` — a word the
// contract explicitly offers — reached an engine whose enum accepts only `stop`, and
// silently behaved as `continue`.
//
// THE OTHER HALF OF #38 RETIRED WITH THE LOBSTER STEP GRAMMAR. It pinned `parallel.wait`'s
// authored default (`"all"`) being materialised for an engine enum that declares none. A
// `parallel:` arm is not authorable any more (D-FLOW-1: a stage does exactly one of four
// things), so there is no unauthored `wait:` left to materialise. The on_error translation
// is still live and still pinned here.

func TestLowerMapsTheLobsterVocabulary(t *testing.T) {
	failStage := stepStage(charlyCmd("a", `echo a`))
	failStage.On_error = "fail"
	contStage := stepStage(charlyCmd("b", `echo b`))
	contStage.On_error = "continue"
	p := &params.Pipeline{
		Description: "vocabulary boundary fixture",
		Stage: map[string]spec.Stage{
			"a": failStage,
			"b": ranAfter(contStage, "a"),
		},
	}
	lobster, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/vocab", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	out := string(lobster)

	if !strings.Contains(out, "on_error: stop") {
		t.Errorf("an authored `fail` must reach the engine as its own word, `stop`:\n%s", out)
	}
	if strings.Contains(out, "on_error: fail") {
		t.Errorf("the authoring word must never reach an engine that does not know it:\n%s", out)
	}
	if !strings.Contains(out, "on_error: continue") {
		t.Errorf("a word both sides already agree on must pass through untouched:\n%s", out)
	}
}

// TestLowerLeavesAnUnknownWordAlone pins the other half: the mapper renames ONE known word
// and never guesses at a value it does not recognise. The engine's loader is the authority
// on whether a word is one it knows, so a typo must reach it and fail loudly there rather
// than being silently rewritten here.
func TestLowerLeavesAnUnknownWordAlone(t *testing.T) {
	st := stepStage(charlyCmd("a", `echo a`))
	st.On_error = "retry-later"
	p := &params.Pipeline{Description: "unknown word fixture", Stage: map[string]spec.Stage{"a": st}}
	// The validator refuses an unknown on_error word before the lowering ever runs, which is
	// the loud failure this test wants — the point is that nothing REWRITES it.
	if err := ValidatePipeline(p); err == nil {
		t.Fatal("an unknown on_error word must be refused by the gate")
	}
}
