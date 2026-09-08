package pluginpipeline

import (
	"os"
	"path/filepath"
	"strings"
)

// skills.go — skills are passed by @github GITLINKS to the CANDIES that provide
// the skill: entities (the standard require:/candy:/import: mechanism). The engine
// resolves each ref against the corpus the candies render (marketplace output at
// PIPELINE_CORPUS_DIR) and appends the skill: entity content to the agent system
// prompt. Unresolved refs degrade the prompt (reported by the ade-pipeline-valid
// check when the corpus is absent).

func corpusDir() string { return os.Getenv("PIPELINE_CORPUS_DIR") }

func resolveSkills(sys string) string {
	refs := os.Getenv("PIPELINE_SKILLS")
	corpus := corpusDir()
	if refs == "" || corpus == "" {
		return sys
	}
	var sb strings.Builder
	sb.WriteString(sys)
	for _, ref := range strings.Split(refs, ",") {
		repo := repoKey(ref)
		if repo == "" {
			continue
		}
		for _, dir := range skillDirs {
			p := filepath.Join(corpus, dir, "SKILL.md")
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			sb.WriteString("\n\n--- " + dir + " ---\n" + string(b))
		}
	}
	return sb.String()
}

// skillDirs: the rendered skill: entities served by the eval candies (Phase 3).
var skillDirs = []string{
	"omarchy-eval-lane", "omarchy-eval-oracle", "omarchy-eval-tiers", "omarchy-eval-golden",
	"omarchy-eval-sequencing", "omarchy-eval-media", "omarchy-eval-work-lane",
	"omarchy-eval-full-loop", "omarchy-eval-cold-reader",
}

// repoKey extracts the repo key from a ref like
// '@github.com/opencharly/distro-omarchy:v2026.250.0359' -> distro-omarchy.
func repoKey(ref string) string {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "@"))
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i]
	}
	if i := strings.Index(ref, "/candy/"); i > 0 {
		ref = ref[:i]
	}
	ref = strings.TrimPrefix(ref, "github.com/opencharly/")
	return ref
}
