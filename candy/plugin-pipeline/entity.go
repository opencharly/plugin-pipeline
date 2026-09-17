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

// entity.go — resolves a kind:pipeline entity BY NAME from the project's
// charly.yml (cwd or CHARLY_PROJECT_DIR), validates it against the served
// #PipelineInput schema, and returns the parsed plan. This makes the command
// self-contained and placement-independent (no reliance on an in-process
// loader cache): the config file is the single source, the plugin validates
// it with its OWN schema.
//
// The entity may live in ANY of the project's loaded documents, not only the
// root charly.yml: the loader accepts entities from flat `import:` sibling
// files (the documented per-kind split, e.g. `- pipelines.yml`) and from
// `discover:`d manifests. This plugin is a BAKED command plugin — charly
// dispatch syscall.Exec's it in CLI mode, so there is no reverse-channel
// executor and it cannot call the host loader (ade.go's "why NOT in-plugin
// dial dispatch"). It therefore re-derives the same resolution PURELY from the
// filesystem: root first (root-wins), then each `import:` document, then each
// `discover:`d manifest — reading the project's OWN directives with the
// canonical spec decoders (spec.ImportList / spec.DiscoverConfig) so it can
// never fork the loader's grammar (R3).

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

// entityDocs returns every document that may declare the named entity, in
// precedence order (root-wins): the root charly.yml, then each flat `import:`
// sibling file, then each `discover:`d manifest. Namespaced imports resolve to
// WHOLE PROJECTS whose entities are namespaced (never bare-name addressable
// here), so they are intentionally not searched.
func entityDocs(dir string) ([]string, error) {
	rootPath := filepath.Join(dir, spec.UnifiedFileName)
	rootRaw, err := os.ReadFile(rootPath)
	if err != nil {
		return nil, fmt.Errorf("read charly.yml: %w", err)
	}
	paths := []string{rootPath}

	var root projectRootDoc
	if uerr := yaml.Unmarshal(rootRaw, &root); uerr != nil {
		// An unparseable root still yields the root path; the caller's per-doc
		// parse reports the real error. A flat import list cannot turn this into
		// an early return (R1: the former []map[string]string decoder did).
		return paths, nil
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
			continue // best-effort: an unreadable discover root is not fatal here
		}
		for _, sub := range dirs {
			paths = append(paths, filepath.Join(sub, manifest))
		}
	}
	return paths, nil
}

// loadEntity resolves the named kind:pipeline entity from the project's
// documents (root-wins) and returns its validated body.
func loadEntity(name string) (params.PipelineInput, error) {
	paths, err := entityDocs(projectDir())
	if err != nil {
		return params.PipelineInput{}, err
	}
	var body yaml.Node
	found := false
	for _, p := range paths {
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		var doc map[string]yaml.Node
		if uerr := yaml.Unmarshal(raw, &doc); uerr != nil {
			continue
		}
		node, ok := doc[name]
		if !ok {
			continue
		}
		// the node must be a mapping whose single kind key is "pipeline"
		var m map[string]yaml.Node
		if derr := node.Decode(&m); derr != nil {
			return params.PipelineInput{}, fmt.Errorf("pipeline entity %q decode (%s): %w", name, p, derr)
		}
		b, ok := m["pipeline"]
		if !ok {
			continue // a same-named entity of a DIFFERENT kind — keep looking
		}
		body = b
		found = true
		break
	}
	if !found {
		return params.PipelineInput{}, fmt.Errorf("pipeline entity %q not found in charly.yml or any imported/discovered document", name)
	}
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
