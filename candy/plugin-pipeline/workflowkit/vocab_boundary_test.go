package workflowkit

import (
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// TestLowerMapsTheLobsterVocabulary pins opencharly/plugin-pipeline#38 at the boundary where it went
// wrong. The authored schema and the engine's schema use different words for the same idea, and nothing
// in the lowering translated them: an authored `on_error: fail` — a word the schema explicitly offers —
// reached an engine whose enum accepts only `stop`, and silently behaved as `continue`. A second, quieter
// instance: `wait` carries a DEFAULT on the authored side and none on the engine's, so an unauthored
// `wait:` arrived empty against an enum with nothing to fall back to.
//
// Carried here from opencharly/sdk#368 (the fix was written against `sdk/workflowkit/lower.go`; unit 1
// moved the lowering into this repo, so the fix moved with it and #368 was closed as superseded).
func TestLowerMapsTheLobsterVocabulary(t *testing.T) {
	p := &params.Pipeline{
		Description: "vocabulary boundary fixture",
		Steps: []params.PipelineStep{
			{Id: "a", OnError: "fail", Run: `echo a`},
			{Id: "b", OnError: "continue", Run: `echo b`},
			{Id: "par", Parallel: params.PipelineParallel{Branches: []params.PipelineSubStep{{Id: "b1", Run: `echo b1`}}}},
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
	if !strings.Contains(out, "wait: all") {
		t.Errorf("an unauthored `wait:` must be materialised — the engine's enum declares no default:\n%s", out)
	}
}

// TestLowerLeavesAnUnknownWordAlone pins the other half: the mapper renames ONE known word and never
// guesses at a value it does not recognise. The engine's loader is the authority on whether a word is one
// it knows, so a typo must reach it and fail loudly there rather than being silently rewritten here.
func TestLowerLeavesAnUnknownWordAlone(t *testing.T) {
	p := &params.Pipeline{
		Description: "unknown word fixture",
		Steps:       []params.PipelineStep{{Id: "a", OnError: "not_a_word", Run: `echo a`}},
	}
	lobster, _, err := Lower(p, "/proj", "/proj/.opencharly/pipelines/unk", "/usr/local/bin/charly")
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	if !strings.Contains(string(lobster), "on_error: not_a_word") {
		t.Errorf("an unrecognised word must pass through untouched:\n%s", lobster)
	}
}
