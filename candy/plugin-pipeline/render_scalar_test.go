package pluginpipeline

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// yamlScalar must make arbitrary authored check prose/assertions safe as YAML.
// Regression: the check prose was emitted as a bare scalar, so a prose containing
// ": " (e.g. "declares `_watch: true`") produced invalid YAML and — because the
// bed-render validates the whole project — failed every concurrent lane.
func TestYamlScalar(t *testing.T) {
	cases := map[string]string{
		"plain":              "'plain'",
		"declares `x: true`": "'declares `x: true`'",
		"it's here":          "'it''s here'",
		"a\nb":               "'a b'",
		"a\r\nb":             "'a b'",
	}
	for in, want := range cases {
		if got := yamlScalar(in); got != want {
			t.Errorf("yamlScalar(%q) = %q, want %q", in, got, want)
		}
	}
}

// The exact oracle prose that broke the 16-lane run must parse as YAML once
// scalar-quoted; the bare emission (the pre-fix form) must NOT parse — the
// discriminator for the regression.
func TestYamlScalar_RealProseParses(t *testing.T) {
	prose := "the generated extension manifest declares `_watch: true` on the Omarchy theme contribution"
	var got map[string]any
	if err := yaml.Unmarshal([]byte("check: "+yamlScalar(prose)+"\n"), &got); err != nil {
		t.Fatalf("quoted prose does not parse: %v", err)
	}
	if got["check"] != prose {
		t.Fatalf("round-trip lost the prose: %q", got["check"])
	}
	var bad map[string]any
	if err := yaml.Unmarshal([]byte("check: "+prose+"\n"), &bad); err == nil {
		t.Fatal("the BARE (pre-fix) prose parsed — the test does not discriminate the regression")
	}
}
