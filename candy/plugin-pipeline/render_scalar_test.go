package pluginpipeline

import "testing"

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
