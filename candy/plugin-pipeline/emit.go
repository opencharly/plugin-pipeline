package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"regexp"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"

	"github.com/opencharly/spec/schemaconcat"
)

// emit.go — the SCHEMA-FIRST artifact writer (the `emit` stage kind).
//
// WHY THIS EXISTS: the `generate` stage renders a free-form string template.
// A template is a writer the caller cannot validate at authoring time — the
// record it emits is only as correct as the template, and a single unquoted
// scalar containing ": " (e.g. `what: Docs-only change: …`) produces YAML no
// parser can read. A lane that then treats that file as a committed plan
// (the render-once cache) crashes on the NEXT run with "unreadable committed
// plan". Structuring the value in Go and validating it against CUE BEFORE the
// write makes an invalid artifact impossible: the stage fails informatively
// instead of shipping bytes no reader can consume.
//
// The `emit` stage is NOT a special case for one record: it is the generic
// schema-first counterpart of `generate`, usable for any artifact an engine
// step must write (a record, a plan, a manifest). The schema is authoritative
// and named; the value is structured (ref-resolved); the marshaller is the
// CUE yaml encoder (not a hand-rolled concatenation), so quoting/escaping is
// correct by construction.

// emitCtx is the CUE context for the emit-stage schema compilation (one per
// process; compilation is cheap and deterministic).
var emitCtx = cuecontext.New()

// defNameRe matches a bare CUE definition NAME (`#EvalRecord`) — the discriminator
// between a def in the plugin's schema and a literal CUE source string. A source
// string contains whitespace/colons and so never matches.
var defNameRe = regexp.MustCompile(`^#[A-Za-z][A-Za-z0-9_]*$`)

// emitValidTransforms is the transform set a STRING LEAF accepts. It is NOT the
// generate set: `negate` has no meaning for a string leaf (there is no check to
// negate), so it is rejected rather than silently rendered un-negated — the emit
// grammar's whole point is that a value the code advertises is either implemented
// or a hard error.
var emitValidTransforms = map[string]bool{"json": true, "yaml": true, "indent": true, "bullets": true}

// emitPluginSchema compiles the plugin's own served schema (schema/pipeline.cue
// via the embedded FS) — the SAME source Describe publishes, so a def added
// here is authorable by any pipeline entity without a second schema.
var emitPluginSchema = sync.OnceValues(func() (cue.Value, error) {
	body, _, err := schemaconcat.ConcatSchema(schemaFS, "schema", nil)
	if err != nil {
		return cue.Value{}, fmt.Errorf("compile plugin schema: %w", err)
	}
	v := emitCtx.CompileString(body)
	if v.Err() != nil {
		return cue.Value{}, fmt.Errorf("compile plugin schema: %v", errors.Details(v.Err(), nil))
	}
	return v, nil
})

// runEmit implements the `emit` stage: assemble `value` (ref-resolved), validate
// it against `schema`, then marshal to `out` (yaml by default).
func (rc *runCtx) runEmit(raw map[string]any) error {
	schema := s(raw["schema"])
	if schema == "" {
		return errString("emit: schema required (a CUE def name or a literal CUE source)")
	}
	out := rc.resolveRefs(s(raw["out"]))
	if out == "" {
		return errString("emit: out required")
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(rc.workdir, out)
	}

	// 1. Assemble the structured value from the ref grammar. resolveValue keeps
	//    the TYPE of every leaf (an array stays an array, a map a map), so the
	//    CUE validation sees the real shape — never a stringified one. A string
	//    leaf containing ${name} markers is a per-marker TEMPLATE rendered
	//    against `vars` (the same grammar as generate, but the result is a
	//    STRUCTURED string leaf — the CUE encoder then quotes it correctly).
	vars := mm(raw["vars"])
	value, err := rc.resolveEmitValue(anyMap(raw["value"]), vars)
	if err != nil {
		return err
	}
	valMap, ok := value.(map[string]any)
	if !ok {
		return errString("emit: value must be a mapping")
	}

	// 2. Compile the schema and marshal the assembled value — the ONE
	//    schema-first writer shared with the internal ledger dump (R3): it
	//    validates against the schema with Concreteness required BEFORE any
	//    bytes exist.
	schemaVal, err := emitSchema(rc.workdir, schema)
	if err != nil {
		return err
	}
	format := s(raw["format"])
	if format == "" {
		format = "yaml"
	}
	var body []byte
	switch format {
	case "yaml":
		body, err = marshalSchemaFirst(schemaVal, valMap)
		if err != nil {
			return fmt.Errorf("emit: value violates %s: %w", schema, err)
		}
	case "json":
		// JSON is a YAML subset: still validate against the schema FIRST, then
		// marshal as JSON.
		if _, err := marshalSchemaFirst(schemaVal, valMap); err != nil {
			return fmt.Errorf("emit: value violates %s: %w", schema, err)
		}
		body, err = json.Marshal(valMap)
		if err != nil {
			return fmt.Errorf("emit: json encode: %w", err)
		}
	default:
		return errString("emit: unknown format " + format + " (valid: yaml, json)")
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return err
	}

	// 3. The authored post-write validator (optional), same contract as generate.
	if v := s(raw["validate"]); v != "" {
		return runAuthoredValidator(rc, v)
	}
	return nil
}

// resolveEmitValue is the `emit` stage's value resolver: like resolveValue, but a
// string leaf containing ${var} markers is rendered as a TEMPLATE against vars
// (the per-marker grammar: `${v}`, `${v:indent}`, `${v:bullets}`, `${v:yaml}`),
// while a string leaf without markers is plain ref-resolved. The RESULT is always
// a structured string, so the CUE encoder owns YAML quoting — the difference from
// `generate`, where the template IS the file.
func (rc *runCtx) resolveEmitValue(v any, vars map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		// $report.<key> names a string in the entity's report: block (the prose
		// template) — resolved BEFORE marker rendering so a template can itself
		// carry ${var} markers.
		if s, ok := rc.reportRef(t); ok {
			return rc.renderStringLeaf(s, vars)
		}
		if strings.Contains(t, "${") {
			return rc.renderStringLeaf(t, vars)
		}
		return rc.resolveValue(t), nil
	case map[string]any:
		m := map[string]any{}
		for k, x := range t {
			rv, err := rc.resolveEmitValue(x, vars)
			if err != nil {
				return nil, err
			}
			m[k] = rv
		}
		return m, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			rv, err := rc.resolveEmitValue(x, vars)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// reportRef resolves a `$report.<key>` reference to the entity report: block's
// string value (the prose template). Mirrors the generate stage's `$report.X`
// contract — one lookup, no duplication of the template machinery.
func (rc *runCtx) reportRef(ref string) (string, bool) {
	if !strings.HasPrefix(ref, "$report.") || rc.report == nil {
		return "", false
	}
	v, ok := rc.report[strings.TrimPrefix(ref, "$report.")]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// renderStringLeaf renders ONE string leaf with the ${name[:transform]} grammar
// against vars, returning a plain string. It never emits a YAML fragment itself —
// the leaf is a value the CUE encoder serializes. The accepted transforms are
// emitValidTransforms (json/yaml/indent/bullets); a BARE `${name}` (no transform)
// is the scalar default. `negate` from the generate grammar is deliberately absent
// (a string leaf has no check to negate) and hard-errors.
//
// HARD ERRORS, matching the generate grammar's contract (R1/R4): an UNKNOWN
// transform and an UNRESOLVED ${name} are stage failures, never a silent
// passthrough. A CUE `string` leaf cannot catch a leftover `${x:bogus}`, so
// returning the marker unchanged would ship a corrupt artifact — exactly the
// bug class this stage exists to remove. `@github...` candy refs stay literal
// (the @-grammar would otherwise eat the prefix) and are NOT a var miss.
func (rc *runCtx) renderStringLeaf(tmpl string, vars map[string]any) (string, error) {
	var renderErr error
	out := tmplRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		if renderErr != nil {
			return m
		}
		if strings.HasPrefix(m, "@github") {
			return m
		}
		transform := ""
		if i := strings.LastIndex(m, ":"); i >= 0 {
			transform = strings.TrimSuffix(m[i+1:], "}")
			m = m[:i] + "}"
		}
		if transform != "" && !emitValidTransforms[transform] {
			renderErr = errString("emit: unknown marker transform :" + transform +
				" (valid for a string leaf: json, yaml, indent, bullets)")
			return m
		}
		key := strings.Trim(m, "${}@")
		v, ok := vars[key]
		if !ok {
			// A ${name} that resolves through the ref grammar ($env/$pr/@stage)
			// is legitimate; one that resolves to nothing is a typo and a FAIL.
			if resolved := rc.resolveRefs(m); resolved != m {
				return resolved
			}
			renderErr = errString("emit: unresolved marker " + m + " (not in vars and not a resolvable ref)")
			return m
		}
		val := rc.resolveValue(v)
		switch transform {
		case "indent":
			return scalar(val)
		case "bullets":
			items := ss(val)
			if len(items) == 0 {
				if s := scalar(val); s != "" {
					items = []string{s}
				}
			}
			lines := make([]string, 0, len(items))
			for _, it := range items {
				lines = append(lines, "- "+it)
			}
			return strings.Join(lines, "\n")
		case "yaml", "json":
			// A string LEAF is not a YAML fragment: render the structured value
			// inline (JSON) so the CUE encoder quotes it — never a raw block.
			return jsonStr(val)
		default:
			return scalar(val)
		}
	})
	if renderErr != nil {
		return "", renderErr
	}
	return out, nil
}

// emitSchema resolves the `schema` field to a compiled CUE value. A bare def
// NAME (`#EvalRecord`, no spaces/colons) names a def in the plugin's own served
// schema (schema/pipeline.cue); anything else is treated as a literal CUE source
// string (a self-contained def block), so an entity can ship its own record
// schema without a plugin release.
func emitSchema(workdir, schema string) (cue.Value, error) {
	// (a) a bare def NAME (`#EvalRecord`) in the plugin's own served schema.
	if defNameRe.MatchString(schema) {
		root, err := emitPluginSchema()
		if err != nil {
			return cue.Value{}, err
		}
		def := root.LookupPath(cue.ParsePath(schema))
		if !def.Exists() {
			return cue.Value{}, fmt.Errorf("emit: schema def %s is not in the plugin schema (add it to schema/pipeline.cue), a .cue file, or a literal CUE source", schema)
		}
		return def, nil
	}
	// (b) a `.cue` FILE path — the project owns a committed, reviewable CUE
	//     schema (the strongest form): the file is read, compiled, and a def in
	//     it is validated against. `path.cue#Def` selects the def explicitly;
	//     a bare `path.cue` uses the FIRST `#Def` in the file. Project-relative
	//     paths root at the run workdir.
	if path, def, hasDef := splitSchemaFile(schema); hasDef {
		p := path
		if !filepath.IsAbs(p) {
			p = filepath.Join(workdir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return cue.Value{}, fmt.Errorf("emit: read schema file %s: %w", p, err)
		}
		name := def
		if name == "" {
			name = firstDefName(string(b))
		}
		return compileEmitSchema(string(b), name, p)
	}
	// (c) literal CUE source (a self-contained def block).
	return compileEmitSchema(schema, firstDefName(schema), "literal schema")
}

// compileEmitSchema compiles a CUE source string and resolves `name` as its def.
func compileEmitSchema(src, name, label string) (cue.Value, error) {
	if name == "" {
		return cue.Value{}, fmt.Errorf("emit: %s names no #Def", label)
	}
	if !strings.Contains(src, "package ") {
		src = "package emit\n" + src
	}
	root := emitCtx.CompileString(src)
	if root.Err() != nil {
		return cue.Value{}, fmt.Errorf("emit: compile %s: %v", label, errors.Details(root.Err(), nil))
	}
	def := root.LookupPath(cue.ParsePath(name))
	if !def.Exists() {
		return cue.Value{}, fmt.Errorf("emit: %s has no def %s", label, name)
	}
	return def, nil
}

// splitSchemaFile splits a `path.cue[#Def]` schema reference into its file path
// and optional def name. hasFile is false when the string is not a .cue file ref
// (a bare def name or a literal CUE source).
func splitSchemaFile(schema string) (path, def string, hasFile bool) {
	s := strings.TrimSpace(schema)
	if i := strings.IndexByte(s, '#'); i >= 0 && strings.HasSuffix(s[:i], ".cue") {
		return s[:i], s[i:], true
	}
	if strings.HasSuffix(s, ".cue") {
		return s, "", true
	}
	return "", "", false
}

// firstDefName returns the first '#Name' token in a CUE source string.
func firstDefName(src string) string {
	i := strings.IndexByte(src, '#')
	if i < 0 {
		return ""
	}
	j := i + 1
	for j < len(src) {
		c := src[j]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			j++
			continue
		}
		break
	}
	return src[i:j]
}
