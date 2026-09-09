package pluginpipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// render.go — the generate stage: render an INLINE template with typed vars and
// write it to out; run the registered validator in-process (report-frontmatter).

var tmplRe = regexp.MustCompile("\\$\\{[A-Za-z0-9_.]+\\}|\\$(pr|calver|workdir)|\\$env\\.([A-Z0-9_]+)|@[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+(\\.[A-Za-z0-9_-]+)?")

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
	rendered := tmplRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		// @github.com/... refs are CANDY references, never stage outputs:
		// never substitute them (the @-grammar would otherwise eat the prefix).
		if strings.HasPrefix(m, "@github") {
			return m
		}
		key := strings.Trim(m, "${}@")
		if v, ok := vars[key]; ok {
			val := rc.resolveValue(v)
			if val == nil {
				return "" // nil vars render as empty (a skip plan has no checks), never \"null\"
			}
			if a, isArr := val.([]any); isArr && len(a) > 0 {
				if block := renderCheckBlock(a, m, tmpl); block != "" {
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

// oracle checks injection (the ADE contract): the template's own-line ${checks}
// marker becomes one agent-check: prose step per plan-json check - graded by the
// live agent in the venue (the org-wide ADE), NOT a shell-command contract. The
// oracle's assertion rides in the prose as the grading hint; indentation comes
// from the marker line.
func renderCheckBlock(a []any, token, tmpl string) string {
	lines := strings.Split(tmpl, "\n")
	indent := "              "
	for _, l := range lines {
		if strings.Contains(l, token) {
			indent = l[:strings.Index(l, token)]
			break
		}
	}
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
		prose := what
		if assertion != "" {
			prose = what + " - verify with: " + assertion
		}
		// the FIRST line carries no indent: the marker line's own leading
		// whitespace already prefixes it in the template.
		prefix := indent
		if len(out) == 0 {
			prefix = ""
		}
		// the prose may contain colons: a block scalar keeps the YAML valid.
		out = append(out, prefix+"- agent-check: >-")
		for _, pl := range strings.Split(prose, "\n") {
			out = append(out, indent+"    "+pl)
		}

		out = append(out, indent+"  id: behavior-"+itoa(i+1))
		out = append(out, indent+"  context: [runtime]")
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
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
