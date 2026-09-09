package pluginpipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHeadSHA_RepoFromEntity: the executor subprocess does not see the
// operator's shell env (RCA 2026.252.2210) — the repo must come from the
// ENTITY via the runCtx, with the env as the standalone-CLI fallback only.
// GH_BIN is a fake gh that echoes the repo it was asked about.
func TestHeadSHA_RepoFromEntity(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "gh")
	script := "#!/bin/sh\necho \"asked:$2\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Setenv("GH_BIN", fake)
	defer os.Unsetenv("GH_BIN")
	os.Unsetenv("EVAL_REPO") // the executor subprocess contract: no operator env
	os.Unsetenv("PR_REPO")

	// the entity repo wins even with NO env present
	if got := headSHA("101", "omacom/omarchy"); got != "asked:repos/omacom/omarchy/pulls/101" {
		t.Errorf("headSHA with the entity repo = %q", got)
	}

	// the CLI fallback: no entity repo, the env carries it
	os.Setenv("EVAL_REPO", "omacom/omarchy")
	defer os.Unsetenv("EVAL_REPO")
	if got := headSHA("202", ""); got != "asked:repos/omacom/omarchy/pulls/202" {
		t.Errorf("headSHA with the env fallback = %q", got)
	}

	// no repo anywhere: empty, never a malformed gh call
	os.Unsetenv("EVAL_REPO")
	if got := headSHA("303", ""); got != "" {
		t.Errorf("headSHA without any repo = %q, want empty", got)
	}
}
