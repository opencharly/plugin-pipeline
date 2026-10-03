package pluginpipeline

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// verb_input_defs_test.go — the anti-drift lock on the per-verb input KEY
// SPELLINGS.
//
// schema/pipeline.cue declares the per-verb input shapes for the six stage
// verbs plus the deterministic gate. A migrator copies these exact top-level key
// names VERBATIM into the lifted `verb:` bodies, so a renamed, added or dropped
// field silently breaks every migrated plan with no compile error at the seam.
// So this test freezes each def's top-level field set to the spelling the
// migrator relies on, and pins that each def is declared once: CUE would quietly
// UNIFY two same-named defs, so a second declaration hides behind the first.
func TestVerbInputDefsFrozenFields(t *testing.T) {
	local, err := os.ReadFile(filepath.Join("schema", "pipeline.cue"))
	if err != nil {
		t.Fatalf("reading this plugin's schema: %v", err)
	}
	src := string(local)

	want := map[string][]string{
		"#AgentInput":    {"cache", "llm", "max_turns", "outputs", "prompt", "repo", "redo", "skill", "skills", "tools"},
		"#ProbeInput":    {"input", "media", "outputs", "redo", "verbs"},
		"#AdeInput":      {"bed", "fail_on", "redo"},
		"#GenerateInput": {"negate_checks", "out", "report", "template", "validate", "vars"},
		"#EmitInput":     {"format", "out", "report", "schema", "validate", "value", "vars"},
		"#MediaInput":    {"assemble", "dir", "files", "media", "transcode"},
		"#GateInput":     {"condition"},
	}
	// A fixed order so a failure names the def in a stable position.
	for _, def := range []string{
		"#AgentInput", "#ProbeInput", "#AdeInput", "#GenerateInput",
		"#EmitInput", "#MediaInput", "#GateInput",
	} {
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
