package pluginpipeline

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// verbInputDefs is this plugin's seven per-verb input defs, in a fixed order so a
// failure names the def in a stable position.
//
// Every name carries the `#Pipeline` prefix, and that is a CONTRACT, not a style
// choice — see the header below and TestVerbInputDefsArePrefixed.
var verbInputDefs = []string{
	"#PipelineAgentInput", "#PipelineProbeInput", "#PipelineAdeInput",
	"#PipelineGenerateInput", "#PipelineEmitInput", "#PipelineMediaInput",
	"#PipelineGateInput",
}

// verb_input_defs_test.go — the anti-drift lock on the per-verb input KEY
// SPELLINGS, and on the def NAMES' collision-freedom.
//
// schema/pipeline.cue declares the per-verb input shapes for the six stage
// verbs plus the deterministic gate. A migrator copies these exact top-level key
// names VERBATIM into the lifted `verb:` bodies, so a renamed, added or dropped
// field silently breaks every migrated plan with no compile error at the seam.
// So this test freezes each def's top-level field set to the spelling the
// migrator relies on, and pins that each def is declared once: CUE would quietly
// UNIFY two same-named defs, so a second declaration hides behind the first.
//
// TWO DIFFERENT COLLISIONS — the second is why the names are prefixed.
// The duplicate-declaration check below can only see THIS file. The loader splices
// every LOADED plugin's served schema into ONE CUE instance, and CUE unifies
// same-named defs rather than erroring, so the same silent unification also
// happens ACROSS plugins — where this file cannot see it. That one was real, and
// a near-miss: plugin-agent is a released plugin declaring
// `{Class: "kind", Word: "agent", InputDef: "#AgentInput"}` with an incompatible
// shape (`command: [string, ...string]` + `prompt_via`), so an un-prefixed
// `#AgentInput` here would have unified with it and rejected EVERY
// `agent: {prompt: …}` step — silently, with no error at this seam. The prefix is
// the fix; TestVerbInputDefsArePrefixed is the guard. The loader-side class fix
// (namespace the defs, or reject a cross-plugin collision loudly) is tracked as
// opencharly/charly#770; until it lands, a NEW def added here takes the same prefix.
func TestVerbInputDefsFrozenFields(t *testing.T) {
	local, err := os.ReadFile(filepath.Join("schema", "pipeline.cue"))
	if err != nil {
		t.Fatalf("reading this plugin's schema: %v", err)
	}
	src := string(local)

	want := map[string][]string{
		"#PipelineAgentInput":    {"cache", "llm", "max_turns", "outputs", "prompt", "repo", "redo", "skill", "skills", "tools"},
		"#PipelineProbeInput":    {"input", "media", "outputs", "redo", "verbs"},
		"#PipelineAdeInput":      {"bed", "fail_on", "redo"},
		"#PipelineGenerateInput": {"negate_checks", "out", "report", "template", "validate", "vars"},
		"#PipelineEmitInput":     {"format", "out", "report", "schema", "validate", "value", "vars"},
		"#PipelineMediaInput":    {"assemble", "dir", "files", "media", "transcode"},
		"#PipelineGateInput":     {"condition"},
	}
	for _, def := range verbInputDefs {
		if n := strings.Count(src, def+":"); n != 1 {
			t.Errorf("%s declared %d times; want exactly 1 — CUE unifies duplicate defs silently", def, n)
		}
		got := topLevelFields(t, src, def)
		// Set comparison: the frozen list records WHICH keys exist, not their
		// authored order, and topLevelFields returns its own (sorted) order.
		wantFields := append([]string(nil), want[def]...)
		sort.Strings(wantFields)
		if strings.Join(got, ",") != strings.Join(wantFields, ",") {
			t.Errorf("%s top-level fields drifted.\n got: %v\nwant: %v\n"+
				"A migrator copies these keys verbatim into the lifted verb bodies; change the frozen list deliberately if the move is intended.", def, got, wantFields)
			continue
		}
		t.Logf("%s frozen fields match: %v", def, got)
	}
}

// TestVerbInputDefsArePrefixed pins the COLLISION-FREEDOM of the seven verb input
// def names — the property TestVerbInputDefsFrozenFields structurally cannot check,
// because the collision it guards against comes from a DIFFERENT plugin.
//
// Each name must carry the `#Pipeline` prefix AND the un-prefixed spelling must be
// absent from the schema. Dropping the prefix re-arms a silent cross-plugin
// unification (CUE unifies same-named defs rather than erroring), so this fails
// here, at the authoring seam, instead of in a downstream project's validation.
func TestVerbInputDefsArePrefixed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("schema", "pipeline.cue"))
	if err != nil {
		t.Fatalf("reading this plugin's schema: %v", err)
	}
	src := string(raw)
	for _, def := range verbInputDefs {
		if !strings.HasPrefix(def, "#Pipeline") {
			t.Errorf("%s does not carry the #Pipeline prefix; it shares ONE CUE namespace with every other loaded plugin", def)
			continue
		}
		bare := "#" + strings.TrimPrefix(def, "#Pipeline")
		if strings.Contains(src, "\n"+bare+":") {
			t.Errorf("the schema declares the un-prefixed %s. It shares ONE CUE instance (and so ONE def namespace) with every other loaded plugin, and CUE would unify it with a same-named def from another plugin SILENTLY. Keep the #Pipeline prefix — see the header of verb_input_defs_test.go.", bare)
		}
	}
}
