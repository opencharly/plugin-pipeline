package pluginpipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// render.go — the generate stage: render an INLINE template with typed vars and
// write it to out; run the registered validator in-process (report-frontmatter).
// A marker may carry a per-marker transform (`${var:negate}` / `${var:json}` /
// `${var:yaml}`) applied to that marker alone, so ONE template renders a whole
// record (both beds + the structured results).

var tmplRe = regexp.MustCompile("\\$\\{[A-Za-z0-9_.]+(:[a-z]+)?\\}|\\$(pr|calver|workdir)|\\$env\\.([A-Z0-9_]+)|@[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+(\\.[A-Za-z0-9_-]+)?")

func (rc *runCtx) runGenerate(raw map[string]any) error {
	tmpl := s(raw["template"])
	if ref := s(raw["template"]); strings.HasPrefix(ref, "$report.") {
		if v, ok := rc.report[strings.TrimPrefix(ref, "$report.")]; ok {
			tmpl, _ = v.(string)
		}
	}
	vars := mm(raw["vars"])
	out := rc.resolveRefs(s(raw["out"]))
	if out == "" {
		return errString("generate: out required")
	}
	// relative out: paths are rooted at the RUN workdir (the pipeline may run
	// from a different cwd than the workdir, e.g. the eval lane operates on the
	// eval-omarchy worktree while the entity lives in eval-charly).
	if !filepath.IsAbs(out) {
		out = filepath.Join(rc.workdir, out)
	}
	negateAll := negateChecks(raw)
	rendered := tmplRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		// @github.com/... refs are CANDY references, never stage outputs:
		// never substitute them (the @-grammar would otherwise eat the prefix).
		if strings.HasPrefix(m, "@github") {
			return m
		}
		// the per-marker transform: ${var:negate} / ${var:json} / ${var:yaml}.
		// Applied to THIS marker alone (the stage-level negate_checks applies to
		// ALL), so ONE template can render both the treatment bed and its
		// negative control. `origMarker` keeps the full token for indent lookup.
		origMarker := m
		transform := ""
		if i := strings.LastIndex(m, ":"); i >= 0 {
			transform = strings.TrimSuffix(m[i+1:], "}")
			m = m[:i] + "}"
		}
		key := strings.Trim(m, "${}@")
		if v, ok := vars[key]; ok {
			val := rc.resolveValue(v)
			if val == nil {
				return "" // nil vars render as empty (a skip plan has no checks), never \"null\"
			}
			if transform == "json" {
				return jsonStr(val)
			}
			if transform == "yaml" {
				return renderYAMLBlock(val, origMarker, tmpl)
			}
			if a, isArr := val.([]any); isArr && len(a) > 0 {
				negate := negateAll || transform == "negate"
				if block := renderCheckBlock(a, origMarker, tmpl, negate); block != "" {
					return block
				}
			}
			return scalar(val)
		}
		return rc.resolveRefs(m)
	})
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(rendered), 0o644); err != nil {
		return err
	}
	if v := s(raw["validate"]); v != "" {
		if v == "report-frontmatter" {
			return validateFrontmatter(out)
		}
		// GENERIC validation contract: run the authored validator as a shell
		// command in the run workdir (the refs resolve: $pr/$calver/$workdir/
		// $env.NAME). Non-zero exit = the stage FAILS - the rendered artifact
		// never ships unvalidated (RCA 2026.252: the eval lane shipped
		// placeholder beds because this contract was declared but dead).
		cmd := rc.resolveRefs(v)
		c := exec.Command("bash", "-c", cmd)
		c.Dir = rc.workdir
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return errString("generate validate failed: " + err.Error())
		}
	}
	return nil
}

// oracle checks injection: the template's own-line ${checks} marker becomes
// one DETERMINISTIC command check per plan-json check — the checks ARE shell
// assertions (grep/test), not agent judgments, so they render as
// check: + id: + command: steps and EXECUTE in the venue. The agent-check
// prose emission ("verify with:", "no grader bound" SKIPs) is GONE (hard
// cutover, R5). Indentation comes from the marker line.
func renderCheckBlock(a []any, token, tmpl string, negate bool) string {
	indent := markerIndent(token, tmpl)
	out := []string{}
	for i, item := range a {
		m, _ := item.(map[string]any)
		what := s(m["what"])
		if what == "" {
			what = s(m["id"])
		}
		if what == "" {
			continue
		}
		assertion := s(m["assertion"])
		if assertion == "" {
			continue // a check without an assertion is inert — never rendered
		}
		if negate {
			// the CONTROL bed: the negated assertion must PASS on the pristine
			// golden — a check that passes without the PR is a FAKE assertion.
			assertion = "! ( " + assertion + " )"
		}
		// the FIRST line carries no indent: the marker line's own leading
		// whitespace already prefixes it in the template.
		prefix := indent
		if len(out) == 0 {
			prefix = ""
		}
		out = append(out, prefix+"- check: "+yamlScalar(what))
		out = append(out, indent+"  id: behavior-"+itoa(i+1))
		out = append(out, indent+"  context: [runtime]")
		out = append(out, indent+"  command: "+yamlScalar(assertion))
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

// renderYAMLBlock renders a structured value (a nested map/list) as a YAML block
// indented to the marker — the record-building counterpart of renderCheckBlock,
// so an eval record carries structured oracle/eval results without inline JSON.
// The first line is unprefixed (the marker line's own whitespace prefixes it).
func renderYAMLBlock(val any, token, tmpl string) string {
	b, err := yaml.Marshal(val)
	if err != nil {
		return scalar(val)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	indent := markerIndent(token, tmpl)
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// markerIndent: the whitespace preceding the token on its own template line (the
// block continuation indent).
func markerIndent(token, tmpl string) string {
	for _, l := range strings.Split(tmpl, "\n") {
		if i := strings.Index(l, token); i >= 0 {
			return l[:i]
		}
	}
	return ""
}

// yamlScalar renders s as a single-quoted YAML scalar: embedded single quotes are
// DOUBLED (”), and newlines are folded to spaces. A check's prose is arbitrary
// authored text — a bare ": " (e.g. "declares `_watch: true`") made the rendered
// bed invalid YAML, and because the bed-render validates the WHOLE project that
// one bad bed failed EVERY concurrent lane (RCA 2026.256.2218).
// RCA: this exact prose ("declares `_watch: true` …") in PR 10134 broke the
// rendered bed at yaml line 31 and failed bed-render on lanes 10199/10212/
// 10215/10228 — one bad prose string, validated project-wide, poisoned every
// concurrent lane.
func yamlScalar(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "'", "''")
	return "'" + s + "'"
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// negateChecks: the generate stage's negate_checks flag — the CONTROL bed
// renders the checks negated (the assertion-integrity proof).
func negateChecks(raw map[string]any) bool {
	if b, ok := raw["negate_checks"].(bool); ok {
		return b
	}
	return false
}

type errString string

func (e errString) Error() string { return string(e) }

func resolveTemplate(raw map[string]any) string {
	if t, ok := raw["_template"].(string); ok {
		return t
	}
	return ""
}

// validateFrontmatter: the INLINE frontmatter JSON schema (a minimal validator
// for the eval lane — the work-lane fields).
func validateFrontmatter(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	txt := string(b)
	required := []string{"verdict", "triage", "channel", "tier"}
	for _, k := range required {
		if !strings.Contains(txt, k) {
			return errString("frontmatter missing field: " + k)
		}
	}
	return nil
}
