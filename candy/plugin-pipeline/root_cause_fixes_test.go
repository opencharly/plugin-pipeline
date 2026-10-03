package pluginpipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// root_cause_fixes_test.go — the regression locks for the live-caught defects
// found while running the real eval-pr-plan lane.

// TestLastLinesKeepsTheDiagnosisTail: the ADE summary must keep WHOLE trailing
// lines (the error), never a mid-word byte slice.
//
// Live defect: the predecessor kept the last 400 BYTES, so the control stage's
// failure surfaced as the garbled fragment
// `check-run exit 1: eline/candy/plugin-pipeline` — the diagnosis ("already
// running … lock") was dropped and the message began mid-word.
func TestLastLinesKeepsTheDiagnosisTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("banner line without the diagnosis\n")
	}
	b.WriteString("charly: error: check bed \"check-x\" is already running in this project\n")
	out := lastLines(b.String(), 12, 400)
	if strings.HasPrefix(out, "…") && strings.Contains(out, "eline/") {
		t.Fatalf("summary is a mid-word fragment: %q", out)
	}
	if !strings.Contains(out, "already running") {
		t.Fatalf("summary must retain the trailing diagnosis, got %q", out)
	}
	// every retained line must be a whole line (no leading partial word)
	for _, ln := range strings.Split(out, "\n") {
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "banner line") && !strings.HasPrefix(ln, "charly: error:") && !strings.HasPrefix(ln, "…") {
			t.Fatalf("summary carries a partial line: %q", ln)
		}
	}
}

// TestAdeBedIsRequiredNotDefaulted: an empty bed must FAIL LOUD, never fall back
// to a hardcoded lane name.
//
// Validator block on PR #26 (R3/R5): three sites substituted a hardcoded
// `check-omarchy-pr-<pr>-vm` — a lane's private naming scheme in the
// domain-neutral engine. #AdeStage.bed is schema-REQUIRED, so an empty bed is a
// caller defect; defaulting it silently mis-targeted any other project and made
// the bed-naming rule live in two places with different rules.
func TestAdeBedIsRequiredNotDefaulted(t *testing.T) {
	if _, _, _, err := runAdeBedKit(context.Background(), "7", "", t.TempDir(), nil); err == nil {
		t.Fatal("an empty bed must be a hard error, not defaulted")
	}
	if _, _, err := adeVerdict(context.Background(), "7", "sha", "", t.TempDir()); err == nil {
		t.Fatal("adeVerdict with an empty bed must be a hard error, not defaulted")
	}
	// the literal fallback scheme must be gone from the engine entirely
	for _, f := range []string{"ade.go", "adekit.go", "cli.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"check-omarchy-pr-" + pr + "-vm"`) {
			t.Fatalf("%s still substitutes the hardcoded omarchy bed name", f)
		}
	}
}

// TestVenueHasOneOwner: the venue lifecycle must have exactly ONE owner.
//
// Validator block on PR #26 (R1/R3/R5): a batch-level `teardownLaneBeds` was a
// SECOND owner that (a) resolved bed refs against the PROCESS env rather than
// the lane's run context, and (b) used the signal-cancelled ctx, so it was
// skipped exactly in the abort scenario teardown exists for. The `ade` stage's
// deferred destroyVenue is the one owner; no batch-level teardown may return.
func TestVenueHasOneOwner(t *testing.T) {
	// the removed duplicate owner must not exist as a symbol
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "func teardownLaneBeds") || strings.Contains(string(src), "func teardownVenue") {
		t.Fatal("a batch-level venue teardown must not exist — the ade stage owns the venue lifecycle")
	}
	// the destroy must be detached from cancellation so an abort still tears down
	adeSrc, err := os.ReadFile("ade.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adeSrc), "context.WithoutCancel") {
		t.Fatal("the venue destroy must run on a context detached from cancellation (an abort must not strand the VM)")
	}
}

// TestProbeSequencingRequiresABedPrefix: the sequencing gate is DERIVED from the
// lane's declared bed prefix. The predecessor matched a literal
// `check-omarchy-pr-<N>-vm`, so any other project's gate passed vacuously.
func TestProbeSequencingRequiresABedPrefix(t *testing.T) {
	ok, msg := probeSequencing(map[string]any{})
	if ok {
		t.Fatal("sequencing with no bed_prefix must FAIL, not pass vacuously")
	}
	if !strings.Contains(msg, "bed_prefix") {
		t.Fatalf("the failure must name the missing input, got %q", msg)
	}
}

// TestProbeGoldenPresentRequiresTheGoldenPath: no silent eval-omarchy default.
func TestProbeGoldenPresentRequiresTheGoldenPath(t *testing.T) {
	ok, msg := probeGoldenPresent(map[string]any{})
	if ok {
		t.Fatal("golden_present with no golden must FAIL, not silently substitute a hardcoded path")
	}
	if !strings.Contains(msg, "golden required") {
		t.Fatalf("the failure must name the missing input, got %q", msg)
	}
	// a missing file is still an honest failure
	ok, msg = probeGoldenPresent(map[string]any{"golden": filepath.Join(t.TempDir(), "absent.qcow2")})
	if ok || !strings.Contains(msg, "golden missing") {
		t.Fatalf("a missing golden must fail: ok=%v msg=%q", ok, msg)
	}
}

// TestProbeHeadFreshnessUsesTheCanonicalClient: the probe must go through headSHA
// (ghkit), not a hand-rolled `gh` subprocess. A repo that cannot be resolved is
// an HONEST failure naming the repo, never a bare "gh failed".
func TestProbeHeadFreshnessUsesTheCanonicalClient(t *testing.T) {
	ok, msg := probeHeadFreshness(map[string]any{"plan_sha": "abc", "pr": 1, "repo": ""})
	if ok || !strings.Contains(msg, "required") {
		t.Fatalf("missing repo must fail with a named reason, got ok=%v msg=%q", ok, msg)
	}
	// an unresolvable repo: the diagnostic must name it (the subprocess version
	// said only "gh failed").
	ok, msg = probeHeadFreshness(map[string]any{"plan_sha": "abc", "pr": 1, "repo": "no-such-org-xyz/no-such-repo"})
	if ok {
		t.Fatal("an unresolvable repo must fail")
	}
	if !strings.Contains(msg, "no-such-org-xyz/no-such-repo") {
		t.Fatalf("the failure must name the repo, got %q", msg)
	}
}

// TestPipelineToolGroupIsGone: the broken `pipeline` agent-tool group (empty
// parameter schemas + empty dispatch input, unused, duplicating the probe
// stages) is DELETED (R3/R5) — a `tools: [pipeline]` reference must resolve to
// no tools rather than to nine vacuous ones.
func TestPipelineToolGroupIsGone(t *testing.T) {
	if _, ok := toolCatalog["pipeline"]; ok {
		t.Fatal("the broken pipeline tool group must be deleted, not retained")
	}
	if got := buildTools([]string{"pipeline"}); len(got) != 0 {
		t.Fatalf("tools: [pipeline] must yield no tools, got %d", len(got))
	}
	// the surviving groups still render
	if got := buildTools([]string{"pr", "ledger"}); len(got) != 5 {
		t.Fatalf("pr+ledger must yield 5 tools, got %d", len(got))
	}
}
