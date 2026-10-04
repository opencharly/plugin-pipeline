package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// verb_emit.go — the lifted `emit` verb. The plan stage body (runEmit) MOVED here
// from emit.go when the stage kind became a reachable verb: it is registered as
// {Class: "verb", Word: "emit", InputDef: "#PipelineEmitInput"} and dispatched
// by Invoke (a `<word>: <input>`
// step in any plan). The emit SCHEMA machinery (emitSchema, resolveEmitValue, the
// schema-first writer) stays in emit.go. ONE body, one caller (R3).

// runVerbEmit is the verb handler. emit produces no outputs — its artifact is the
// serialized `out` file — so only the error is returned.
func runVerbEmit(in params.PipelineEmitInput, e *verbEnv) (map[string]any, error) {
	return nil, e.runEmit(e.stageRaw(in))
}

// runEmit implements the `emit` stage: assemble `value` (ref-resolved), validate
// it against `schema`, then marshal to `out` (yaml by default).
func (rc *verbEnv) runEmit(raw map[string]any) error {
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
	//    against `vars` with the SHARED MARKER SYNTAX but emit's OWN transform
	//    set (json/yaml/indent/bullets; a bare ${name} is the scalar default;
	//    `negate` is rejected — see emitValidTransforms). The result is a
	//    STRUCTURED string leaf, so the CUE encoder quotes it correctly.
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
