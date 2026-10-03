package pluginpipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/sdk/workflowkit"
	specloader "github.com/opencharly/spec/loader"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// entity.go — resolves a named entity from the project's charly.yml documents
// (cwd or CHARLY_PROJECT_DIR). Two callers share ONE resolver (R3):
//   - loadEntity resolves a `kind:pipeline` entity and validates it with
//     sdk/workflowkit.ValidatePipeline;
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
// (root-wins) and returns its validated spec.Pipeline body. The authored grammar
// is `steps:` (spec.Pipeline), validated by sdk/workflowkit.ValidatePipeline —
// the SAME gate every engine runs before lowering.
func loadEntity(name string) (spec.Pipeline, error) {
	// The loader permits the SAME name under DIFFERENT kinds across separate documents
	// (cross-file reuse), so a wrong-kind node must not shadow a pipeline entity in a
	// later document — keep looking for the pipeline kind, and report wrong-kind only
	// if NO document declares it as a pipeline.
	var result spec.Pipeline
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
		return spec.Pipeline{}, err
	}
	if !found {
		if wrongKindPath != "" {
			return spec.Pipeline{}, fmt.Errorf("pipeline entity %q in %s has no pipeline: kind", name, wrongKindPath)
		}
		return spec.Pipeline{}, fmt.Errorf("pipeline entity %q not found in charly.yml or any imported/discovered document", name)
	}
	return result, nil
}

// decodePipelineInput decodes the `pipeline:` body into spec.Pipeline and
// validates it with the shared workflowkit gate.
//
// It desugars FIRST (desugarNestedPlans), because the authored step grammar is the
// loader's `<word>: <input>` plugin-verb sugar, and spec.Step only carries the
// internal plugin/plugin_input envelope that sugar is rewritten into. Skipping the
// rewrite does not fail — yaml.v3 silently DROPS the unknown sugar key, so a
// builtin-verb plan step lowers to an EMPTY step `- {}` and the workflow runs
// nothing. Live-caught while authoring the R10 bed's mixed-grammar fixture.
func decodePipelineInput(name, path string, body yaml.Node) (spec.Pipeline, error) {
	if err := desugarNestedPlans(name, "pipeline", &body); err != nil {
		return spec.Pipeline{}, fmt.Errorf("pipeline entity %q (%s): %w", name, path, err)
	}
	var p spec.Pipeline
	if err := body.Decode(&p); err != nil {
		return spec.Pipeline{}, fmt.Errorf("pipeline entity %q (%s): %w", name, path, err)
	}
	if err := workflowkit.ValidatePipeline(&p); err != nil {
		return spec.Pipeline{}, fmt.Errorf("pipeline entity %q (%s): %w", name, path, err)
	}
	return p, nil
}

// memberKindSet is the resource-member discriminator set the sugar walk stops at —
// the SAME stop rule the loader applies (loaderkit's classifyKind asChild arm reads
// spec.ResourceKinds); the loader never desugars inside a resource member, so neither
// does the walk. Built from the CUE-derived vocabulary, never hand-maintained (R3).
var memberKindSet = func() map[string]bool {
	m := make(map[string]bool, len(spec.ResourceKinds))
	for _, k := range spec.ResourceKinds {
		m[k] = true
	}
	return m
}()

// desugarNestedPlans rewrites every authored `<word>: <input>` plugin-verb sugar inside
// every nested `plan:` of a pipeline body into the internal plugin/plugin_input pair —
// the loader's own parse-time rewrite, so the front-end reads the entity EXACTLY as the
// host loader does (R3; the walk mirrors loaderkit.desugarNestedPlans, whose per-plan
// rewrite itself is the SDK's exported loaderkit.DesugarSteps — the canonical
// implementation, never re-implemented here).
//
// The walk is kind-blind apart from the member-kind stop rule, and it stops at a
// resource member exactly as the loader does, so the two never drift.
func desugarNestedPlans(entity, path string, n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := desugarNestedPlans(entity, fmt.Sprintf("%s[%d]", path, i), c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			if memberKindSet[k] {
				continue
			}
			if k == "plan" && v.Kind == yaml.SequenceNode && len(v.Content) > 0 && v.Content[0].Kind == yaml.MappingNode {
				if err := loaderkit.DesugarSteps(entity, v, spec.Threaded{}); err != nil {
					return fmt.Errorf("%s.plan: %w", path, err)
				}
				continue
			}
			if err := desugarNestedPlans(entity, path+"."+k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
