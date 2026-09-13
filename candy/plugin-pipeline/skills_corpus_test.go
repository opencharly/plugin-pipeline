package pluginpipeline

import (
	"path/filepath"
	"testing"
)

// skillCorpus must REF-RESOLVE the entity's skills.corpus so a lane can point at
// a generated corpus outside its own tree (the marketplace projection) via
// $env.EVAL_UMBRELLA, and must still join a relative literal with the workdir.
func TestSkillCorpus_RefResolved(t *testing.T) {
	umbrella := t.TempDir()
	wd := t.TempDir()
	rc := &runCtx{
		workdir: wd,
		env:     map[string]string{"EVAL_UMBRELLA": umbrella},
		skills:  map[string]any{"corpus": "$env.EVAL_UMBRELLA/marketplace/distros/skills"},
	}
	got := skillCorpus(rc)
	want := filepath.Join(umbrella, "marketplace", "distros", "skills")
	if got != want {
		t.Fatalf("skillCorpus = %q, want %q (the $env ref must resolve)", got, want)
	}
}

func TestSkillCorpus_RelativeLiteralJoinsWorkdir(t *testing.T) {
	wd := t.TempDir()
	rc := &runCtx{
		workdir: wd,
		env:     map[string]string{},
		skills:  map[string]any{"corpus": "candy/eval-lane/skills"},
	}
	got := skillCorpus(rc)
	want := filepath.Join(wd, "candy/eval-lane/skills")
	if got != want {
		t.Fatalf("skillCorpus = %q, want %q (relative literal joins the workdir)", got, want)
	}
}

// The $workdir arm is one of the reference grammar's builtins; the corpus may
// be expressed relative to the run workdir as well as to an $env var.
func TestSkillCorpus_WorkdirRefResolved(t *testing.T) {
	wd := t.TempDir()
	rc := &runCtx{
		workdir: wd,
		env:     map[string]string{},
		skills:  map[string]any{"corpus": "$workdir/generated/skills"},
	}
	got := skillCorpus(rc)
	want := filepath.Join(wd, "generated", "skills")
	if got != want {
		t.Fatalf("skillCorpus = %q, want %q (the $workdir ref must resolve)", got, want)
	}
}

func TestSkillCorpus_AbsoluteLiteralUnchanged(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "corpus")
	rc := &runCtx{workdir: t.TempDir(), env: map[string]string{}, skills: map[string]any{"corpus": abs}}
	if got := skillCorpus(rc); got != abs {
		t.Fatalf("skillCorpus = %q, want the absolute %q unchanged", got, abs)
	}
}

func TestSkillCorpus_NilCtx(t *testing.T) {
	if got := skillCorpus(nil); got != "" {
		t.Fatalf("skillCorpus(nil) = %q, want empty", got)
	}
}
