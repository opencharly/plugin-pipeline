package pluginpipeline

import (
	"encoding/json"
	"regexp"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// envRefs collects every $env.NAME reference across the plan's stages, templates,
// and gates (the dry-run env-resolution contract).
func envRefs(p params.PipelineInput) []string {
	set := map[string]bool{}
	// the whole plan as JSON: every stage input, template, gate ref is scanned
	// (the dry-run env-resolution contract).
	b, _ := json.Marshal(p)
	scanEnvRefs(string(b), set)
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	return out
}

func scanEnvRefs(s string, set map[string]bool) {
	for _, m := range envRe.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			set[m[1]] = true
		}
	}
}

var envRe = regexp.MustCompile(`\$env\.([A-Z0-9_]+)`)
