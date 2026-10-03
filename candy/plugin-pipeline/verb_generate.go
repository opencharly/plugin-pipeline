package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_generate.go — the lifted `generate` verb. The plan stage body (runGenerate)
// MOVED here from render.go when the stage kind became a reachable verb: it is
// registered as {Class: "verb", Word: "generate", InputDef: "#PipelineGenerateInput"}
// and dispatched by Invoke (a
// `<word>: <input>` step in any plan). The template ENGINE (tmplRe, the marker
// transforms, the frontmatter validator) stays in render.go; only the stage body
// moved. ONE body, one caller (R3).

// runVerbGenerate is the verb handler. generate produces no outputs — its
// artifact is the rendered `out` file — so only the error is returned.
func runVerbGenerate(in params.PipelineGenerateInput, e *verbEnv) (map[string]any, error) {
	return nil, e.runGenerate(e.stageRaw(in))
}

// runGenerate renders an INLINE template with typed vars and writes it to out,
// then runs the registered validator in-process (report-frontmatter). A marker
// may carry a per-marker transform (`${var:negate}` / `${var:json}` /
// `${var:yaml}`) applied to that marker alone, so ONE template renders a whole
// record (both beds + the structured results).
func (rc *verbEnv) runGenerate(raw map[string]any) error {
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
	var renderErr error
	rendered := tmplRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		if renderErr != nil {
			return m
		}
		// @github.com/... refs are CANDY references, never stage outputs:
		// never substitute them (the @-grammar would otherwise eat the prefix).
		if strings.HasPrefix(m, "@github") {
			return m
		}
		// the per-marker transform: ${var:negate} / ${var:json} / ${var:yaml} /
		// ${var:indent} / ${var:bullets}. Applied to THIS marker alone (the
		// stage-level negate_checks applies to ALL), so ONE template can render
		// both the treatment bed and its negative control. `origMarker` keeps the
		// full token for indent lookup. An UNKNOWN transform is a hard error — a
		// typo (`:negte`) must never silently render the treatment copy as the
		// control (a vacuous assertion-integrity proof).
		origMarker := m
		transform := ""
		if i := strings.LastIndex(m, ":"); i >= 0 {
			transform = strings.TrimSuffix(m[i+1:], "}")
			m = m[:i] + "}"
		}
		if transform != "" && !validTransforms[transform] {
			renderErr = errString("generate: unknown marker transform :" + transform +
				" (valid: negate, json, yaml, indent, bullets)")
			return m
		}
		key := strings.Trim(m, "${}@")
		if v, ok := vars[key]; ok {
			val := rc.resolveValue(v)
			if val == nil {
				return "" // nil vars render as empty (a skip plan has no checks), never "null"
			}
			if transform == "json" {
				return jsonStr(val)
			}
			if transform == "yaml" {
				return renderYAMLBlock(val, origMarker, tmpl)
			}
			if transform == "indent" {
				return indentContinuation(scalar(val), origMarker, tmpl)
			}
			if transform == "bullets" {
				return bulletsBlock(val, origMarker, tmpl)
			}
			if a, isArr := val.([]any); isArr && len(a) > 0 {
				negate := negateAll || transform == "negate"
				if block := renderCheckBlock(a, origMarker, tmpl, negate); block != "" {
					return block
				}
			} else if transform == "negate" {
				// :negate on a non-array has no negation semantics — error rather
				// than emit the value unchanged (a silent no-op transform).
				renderErr = errString("generate: :negate applied to a non-list marker " + origMarker)
				return m
			}
			return scalar(val)
		}
		return rc.resolveRefs(m)
	})
	if renderErr != nil {
		return renderErr
	}
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
		return runAuthoredValidator(rc, v)
	}
	return nil
}
