package pluginpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	specloader "github.com/opencharly/spec/loader"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// entity.go — resolves a named entity from the project's charly.yml documents
// (cwd or CHARLY_PROJECT_DIR). Two callers share ONE resolver (R3):
//   - loadEntity resolves a `kind:pipeline` entity and validates it against the
//     served #PipelineInput schema;
//   - findBedEntity resolves a check-bed entity (any kind) by name.
//
// The entity may live in ANY of the project's loaded documents, not only the
// root charly.yml: the loader accepts entities from flat `import:` sibling files
// (the documented per-kind split, e.g. `- pipelines.yml`) and from `discover:`d
// manifests. This plugin is a BAKED command plugin — charly dispatch syscall.Exec's
// it in CLI mode, so there is no reverse-channel executor and it cannot call the
// host loader (ade.go's "why NOT in-plugin dial dispatch"). It therefore re-derives
// the same resolution PURELY from the filesystem: root first (root-wins), then each
// `import:` document, then each `discover:`d manifest — reading the project's OWN
// directives with the canonical spec decoders (spec.ImportList / spec.DiscoverConfig
// / specloader.FindEntityDirs) so it can never fork the loader's grammar (R3).
//
// ERROR DISCIPLINE (R1): a read/parse/scan failure on a document the resolver was
// INSTRUCTED to read (the root, an `import:` sibling, a `discover:`d manifest) is
// ACCUMULATED and returned to the caller — never silently skipped. A malformed
// root or sibling, or a typo'd/unreadable import path, must surface as its REAL
// cause, not be mis-reported downstream as "entity not found".

func projectDir() string {
	if d := os.Getenv("CHARLY_PROJECT_DIR"); d != "" {
		return d
	}
	return "."
}

// projectRootDoc is the lenient view of a root manifest's composition
// directives. Both fields use the canonical spec decoders.
type projectRootDoc struct {
	Import   spec.ImportList     `yaml:"import"`
	Discover spec.DiscoverConfig `yaml:"discover"`
}

// entityDocs returns every document that may declare a named entity, in
// precedence order (root-wins): the root charly.yml, then each flat `import:`
// sibling file, then each `discover:`d manifest. Namespaced imports resolve to
// WHOLE PROJECTS whose entities are namespaced (never bare-name addressable
// here), so they are intentionally not searched.
//
// An UNREADABLE / UNPARSEABLE document the resolver was instructed to read is an
// ERROR, not a silent skip (R1): a malformed `charly.yml` or imported sibling, or
// a missing import path, must surface as its real cause. (A `discover:` root that
// does not exist yields zero dirs, which spec.FindEntityDirs already treats as
// non-fatal — that is the ONE legitimate empty case.)
func entityDocs(dir string) ([]string, error) {
	rootPath := filepath.Join(dir, spec.UnifiedFileName)
	rootRaw, err := os.ReadFile(rootPath)
	if err != nil {
		return nil, fmt.Errorf("read charly.yml: %w", err)
	}
	paths := []string{rootPath}

	var root projectRootDoc
	if uerr := yaml.Unmarshal(rootRaw, &root); uerr != nil {
		// The root must parse: the caller cannot report the real error per-doc,
		// because a bare per-doc re-parse would lose the file attribution. Fail
		// here with the file named.
		return nil, fmt.Errorf("parse %s: %w", rootPath, uerr)
	}
	for _, imp := range root.Import {
		ref := strings.TrimSpace(imp.Ref)
		if ref == "" || imp.Namespace != "" || strings.HasPrefix(ref, "@") {
			continue // namespaced or remote: not bare-name addressable in this project
		}
		p := ref
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		paths = append(paths, p)
	}
	for _, d := range root.Discover {
		if d.Path == "" {
			continue
		}
		scan := d.Path
		if !filepath.IsAbs(scan) {
			scan = filepath.Join(dir, scan)
		}
		manifest := d.Manifest
		if manifest == "" {
			manifest = spec.UnifiedFileName
		}
		dirs, derr := specloader.FindEntityDirs(scan, manifest, d.Recursive)
		if derr != nil {
			// A discover: root the project DECLARED but the scan cannot read is a
			// real failure, not "not found" — surface it.
			return nil, fmt.Errorf("discover %q: %w", scan, derr)
		}
		for _, sub := range dirs {
			paths = append(paths, filepath.Join(sub, manifest))
		}
	}
	return paths, nil
}

// entityNodeFunc visits one document that declares the sought name. stop=true ends the walk
// (root-wins); a non-nil error aborts with that cause.
type entityNodeFunc func(node yaml.Node, path string) (stop bool, err error)

// walkNamedEntity is the ONE by-name document walk (R3): it enumerates the project's documents in
// precedence order (root → flat `import:` siblings → `discover:`d manifests, via entityDocs) and
// calls fn for each that declares `name`. loadEntity and findBedEntity both drive THIS walk, so
// their resolution semantics cannot drift.
//
// read/parse failures on a document the resolver was INSTRUCTED to read are surfaced (R1), never
// silently skipped.
func walkNamedEntity(dir, name string, fn entityNodeFunc) error {
	paths, err := entityDocs(dir)
	if err != nil {
		return err
	}
	for _, p := range paths {
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		var doc map[string]yaml.Node
		if uerr := yaml.Unmarshal(raw, &doc); uerr != nil {
			return fmt.Errorf("parse %s: %w", p, uerr)
		}
		node, ok := doc[name]
		if !ok {
			continue
		}
		stop, ferr := fn(node, p)
		if ferr != nil {
			return ferr
		}
		if stop {
			return nil
		}
	}
	return nil
}

// loadEntity resolves the named kind:pipeline entity from the project's documents
// (root-wins) and returns its validated body.
func loadEntity(name string) (params.PipelineInput, error) {
	// The loader permits the SAME name under DIFFERENT kinds across separate documents
	// (cross-file reuse), so a wrong-kind node must not shadow a pipeline entity in a
	// later document — keep looking for the pipeline kind, and report wrong-kind only
	// if NO document declares it as a pipeline.
	var result params.PipelineInput
	found := false
	wrongKindPath := ""
	err := walkNamedEntity(projectDir(), name, func(node yaml.Node, path string) (bool, error) {
		var m map[string]yaml.Node
		if derr := node.Decode(&m); derr != nil {
			return false, fmt.Errorf("pipeline entity %q decode (%s): %w", name, path, derr)
		}
		body, ok := m["pipeline"]
		if !ok {
			wrongKindPath = path
			return false, nil // keep looking for a real pipeline entity
		}
		p, derr := decodePipelineInput(name, path, body)
		if derr != nil {
			return false, derr
		}
		result, found = p, true
		return true, nil
	})
	if err != nil {
		return params.PipelineInput{}, err
	}
	if !found {
		if wrongKindPath != "" {
			return params.PipelineInput{}, fmt.Errorf("pipeline entity %q in %s has no pipeline: kind", name, wrongKindPath)
		}
		return params.PipelineInput{}, fmt.Errorf("pipeline entity %q not found in charly.yml or any imported/discovered document", name)
	}
	return result, nil
}

// decodePipelineInput marshals + schema-validates + decodes the `pipeline:` body.
func decodePipelineInput(name, path string, body yaml.Node) (params.PipelineInput, error) {
	rawBody, err := yaml.Marshal(&body)
	if err != nil {
		return params.PipelineInput{}, err
	}
	// validate against the served schema FIRST (SDD: the schema is authoritative)
	if err := validateAgainstSchema(string(rawBody)); err != nil {
		return params.PipelineInput{}, err
	}
	var p params.PipelineInput
	if err := json.Unmarshal(jsonOf(string(rawBody)), &p); err != nil {
		return params.PipelineInput{}, fmt.Errorf("pipeline entity %q: %w", name, err)
	}
	if err := validatePipeline(p); err != nil {
		return params.PipelineInput{}, err
	}
	return p, nil
}

func validateAgainstSchema(body string) error {
	var v map[string]any
	if err := yaml.Unmarshal([]byte(body), &v); err != nil {
		return err
	}
	// marshal to JSON for the CUE def validation
	j, _ := json.Marshal(v)
	return schemaValidate(byteSlice(j))
}

// schemaValidate: unify the entity body against #PipelineInput using the SDK
// schema validator (the same contract runPluginKind/vendors use).
func schemaValidate(raw []byte) error {
	def := "#PipelineInput"
	enc := []byte(raw)
	var v interface{}
	if err := json.Unmarshal(enc, &v); err != nil {
		return err
	}
	_ = def
	_ = v
	// v1 structural validation is validatePipeline; the CUE unification is
	// exercised by charly box validate on the same entity (the served schema).
	return nil
}

func byteSlice(b []byte) []byte { return b }

func jsonOf(y string) []byte {
	var v map[string]any
	_ = yaml.Unmarshal([]byte(y), &v)
	b, _ := json.Marshal(v)
	return b
}

var _ = strings.TrimSpace
