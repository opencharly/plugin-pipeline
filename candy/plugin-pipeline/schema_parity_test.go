package pluginpipeline

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// schema_parity_test.go — the anti-drift guard for this plugin's MIRROR of the
// shared LLM vocabulary.
//
// schema/pipeline.cue carries #LLMSpec/#LLMParams (and the #LLM* companions) as a
// MIRROR of spec/schema/llm.cue. That duplication is sanctioned (the plugin schema
// must compile STANDALONE while spec's defs generate Go — the shipped precedent is
// plugin-desktop-kind's schema/desktop.cue), but the two can DRIFT, and the failure
// is confusing: the host accepts a field the plugin rejects with
// `#LLMSpec.<field>: field not allowed`.
//
// The def names are the #LLM-prefixed ones so the host's base ++ plugin splice (ONE
// CUE instance) cannot collide with another plugin's schema — a generic
// #ResponseFormat would silently unify with any same-named def elsewhere.
//
// This test asserts each mirrored def's TOP-LEVEL field set equals spec's copy,
// read from the ACTUAL spec schema. A field added to spec/schema/llm.cue and not
// mirrored here fails HERE, next to the fix.
func TestPipelineSchemaMirrorsSpecLLMDefs(t *testing.T) {
	local, err := os.ReadFile(filepath.Join("schema", "pipeline.cue"))
	if err != nil {
		t.Fatalf("reading this plugin's schema: %v", err)
	}
	specSchema := readSpecLLMSchema(t)
	for _, def := range []string{"#LLMSpec", "#LLMParams", "#LLMResponseFormat", "#LLMReasoning", "#LLMStreamOptions"} {
		got := topLevelFields(t, string(local), def)
		want := topLevelFields(t, specSchema, def)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s drifted from spec's copy.\n got: %v\nwant: %v\n"+
				"If spec added a field, mirror it here; if spec removed one, remove it here.", def, got, want)
		}
	}
}

// TestPipelineSchemaHasNoGenericLLMDefNames: the mirrored defs must be #LLM-prefixed
// (splice-safety). A generic #ResponseFormat/#Reasoning would silently unify with a
// same-named def from another plugin's served schema.
func TestPipelineSchemaHasNoGenericLLMDefNames(t *testing.T) {
	local, err := os.ReadFile(filepath.Join("schema", "pipeline.cue"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"#ResponseFormat:", "#Reasoning:", "#StreamOptions:", "#NamedToolChoice:"} {
		if strings.Contains(string(local), bad) {
			t.Errorf("generic def %s present — the mirrored LLM defs must be #LLM-prefixed to avoid a cross-plugin splice collision", bad)
		}
	}
}

// topLevelFields returns the sorted top-level field names of a CUE def. The scan is
// BOUNDED (it stops when the def's own brace closes) so it cannot collect the next
// def's fields — the bug that made an unbounded parser compare two identical
// inflated sets and never catch drift.
func topLevelFields(t *testing.T, src, def string) []string {
	t.Helper()
	idx := strings.Index(src, def+":")
	if idx < 0 {
		t.Fatalf("def %s not found in source", def)
	}
	body := src[idx:]
	start := strings.Index(body, "{")
	if start < 0 {
		t.Fatalf("def %s has no body", def)
	}
	depth := 0
	fieldRe := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\??\s*:`)
	set := map[string]bool{}
	opened := false
	for _, line := range strings.Split(body[start:], "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") && trimmed != "" {
			if depth == 1 {
				if m := fieldRe.FindStringSubmatch(trimmed); m != nil {
					set[m[1]] = true
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth > 0 {
			opened = true
		}
		if opened && depth == 0 {
			break
		}
	}
	if len(set) == 0 {
		t.Fatalf("no fields parsed for %s (parser drift?)", def)
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readSpecLLMSchema reads spec/schema/llm.cue from a sibling checkout (the umbrella)
// or an explicit override.
func readSpecLLMSchema(t *testing.T) string {
	t.Helper()
	var candidates []string
	if mod := os.Getenv("CHARLY_SPEC_SCHEMA"); mod != "" {
		candidates = append(candidates, mod)
	}
	candidates = append(candidates, filepath.Join("..", "..", "..", "spec", "schema", "llm.cue"))
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil {
			return string(b)
		}
	}
	t.Skip("spec/schema/llm.cue not reachable (set CHARLY_SPEC_SCHEMA to enable the parity check)")
	return ""
}
