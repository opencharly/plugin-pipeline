package pluginpipeline

import (
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/errors"
	cueyaml "cuelang.org/go/encoding/yaml"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// refs.go — the reference grammar and the small shared readers the lifted verb
// bodies and the generate/emit template engines consume. It was the surviving
// half of the retired in-process executor: the plan-driven run context, the
// stage ledger and the per-stage conditional-skip/redo control flow are gone
// (the lobster engine owns workflow control now), but the ref substitution, the
// schema-first writer, and the tiny accessors are still the ONE implementation
// every verb body uses (R3).

// refRe matches the built-in refs: $pr/$calver/$workdir and $env.NAME. The
// @stage.output arm is retained in the pattern only so an authored @-ref is
// matched (and left for the resolver to decide) rather than silently passed
// through as literal text.
var refRe = regexp.MustCompile(`\$(pr|calver|workdir)|\$env\.([A-Z0-9_]+)|@([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+)(\.([A-Za-z0-9_-]+))?`)

// resolveRefs replaces $pr/$calver/$workdir/$env.NAME in a string. When a
// resolver override is set (a standalone verb dispatch), it IS the resolver and
// the text is returned verbatim by it. `@github…` refs are CANDY references,
// never refs to substitute.
func (rc *verbEnv) resolveRefs(s string) string {
	if rc == nil {
		return s
	}
	if rc.resolve != nil {
		return rc.resolve(s)
	}
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "@github") {
			return m
		}
		g := refRe.FindStringSubmatch(m)
		switch {
		case g[1] != "":
			switch g[1] {
			case "pr":
				return rc.pr
			case "calver":
				return rc.calver
			case "workdir":
				return rc.workdir
			}
		case g[2] != "":
			if v, ok := rc.env[g[2]]; ok {
				return v
			}
			return ""
		}
		return ""
	})
}

// resolveValue returns a typed value for YAML inputs (strings are ref-resolved;
// maps/slices are resolved recursively — never shell text).
func (rc *verbEnv) resolveValue(v any) any {
	switch t := v.(type) {
	case string:
		return rc.resolveRefs(t)
	case map[string]any:
		m := map[string]any{}
		for k, x := range t {
			m[k] = rc.resolveValue(x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = rc.resolveValue(x)
		}
		return out
	default:
		return v
	}
}

// scalar renders a value as its scalar string form (the template default).
func scalar(v any) string {
	if v == nil {
		return "" // nil never renders "null" in a template
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	case []string:
		// a string list renders SPACE-JOINED — the natural shell form for the
		// pr-apply file list and the tests for-loop (the JSON-array rendering
		// broke the rendered bed commands).
		return strings.Join(t, " ")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// mapOf materialises a typed value back into the CUE-shaped map the verb bodies
// read (a JSON round trip, so field names match the served schema).
func mapOf(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// envMap: the process environment as a map (the ADE kit runner's Env seam).
func envMap() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strList(v any) []string {
	if x, ok := v.([]any); ok {
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func anyMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// triggerOnFail reads a stage's declared redo trigger, defaulting to def.
func triggerOnFail(raw map[string]any, def string) string {
	if m, ok := raw["redo"].(map[string]any); ok {
		if v, ok := m["on_fail"]; ok {
			switch t := v.(type) {
			case string:
				return t
			case []any:
				if len(t) > 0 {
					if s, ok := t[0].(string); ok {
						return s
					}
				}
			}
		}
	}
	return def
}

// marshalSchemaFirst is the ONE schema-first writer the `emit` stage uses: it
// validates `value` against a COMPILED schema value with Concreteness required,
// then marshals it through the CUE YAML encoder — correct quoting/escaping by
// construction, and no bytes exist until the value has passed the schema.
func marshalSchemaFirst(schemaVal cue.Value, value any) ([]byte, error) {
	v := emitCtx.Encode(value)
	if v.Err() != nil {
		return nil, v.Err()
	}
	u := v.Unify(schemaVal)
	if err := u.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("%s", errors.Details(err, nil))
	}
	return cueyaml.Encode(u)
}

// evalCondition: a minimal condition evaluator: "A == B && C in [X, Y]".
func evalCondition(cond string) (bool, string) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true, ""
	}
	for _, part := range strings.Split(cond, "&&") {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, " in ["):
			lhs := strings.TrimSpace(strings.SplitN(part, " in [", 2)[0])
			rest := strings.SplitN(part, " in [", 2)[1]
			rest = strings.TrimSuffix(rest, "]")
			rest = strings.TrimSuffix(rest, "]")
			ok := false
			for _, cand := range strings.Split(rest, ",") {
				if strings.TrimSpace(lhs) == strings.TrimSpace(strings.TrimSpace(cand)) {
					ok = true
				}
			}
			if !ok {
				return false, "condition false: " + part
			}
		case strings.Contains(part, "=="):
			kv := strings.SplitN(part, "==", 2)
			if strings.TrimSpace(kv[0]) != strings.TrimSpace(strings.Trim(kv[1], "\"")) {
				return false, "condition false: " + part
			}
		}
	}
	return true, ""
}
