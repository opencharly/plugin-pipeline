package pluginpipeline

import (
	"os"
	"strings"
	"testing"
)

// TestAdeLaneHeadIsAMapReadNeverALiteralRef restores the lock the lifted cutover
// dropped (the former root_cause_fixes_test.go:TestAdeVerdictUsesTheLaneHeadNotTheProcessEnv).
//
// The ADE stage must hand its external check-run fallback the LANE's head sha. The
// predecessor read os.Getenv("PR_HEAD_SHA") — the operator's value or nothing, never
// this lane's — and the live run logged `--var PR_HEAD_SHA=` (RCA 2026.252.2210).
//
// The assertions below lock the ACCESSOR (the value source). The call site is locked
// separately, structurally, at the end of this test.
func TestAdeLaneHeadIsAMapReadNeverALiteralRef(t *testing.T) {
	// A run context that BOUND the lane head — the per-run contract the retired
	// executor established (headSHA(pr, repo), an operator pin winning).
	bound := &verbEnv{env: map[string]string{"PR_NUMBER": "1", "PR_HEAD_SHA": "aaaa"}}
	if got := bound.adeLaneHead(); got != "aaaa" {
		t.Fatalf("adeLaneHead() = %q, want the run context's bound head %q", got, "aaaa")
	}

	// The standalone env — the ONLY production verbEnv — carries NO per-run map, so
	// the head is EMPTY. A missing run context is honest; a literal ref is a wrong
	// answer that looks like a real one.
	std := standaloneVerbEnv(nil, t.TempDir())
	if got := std.adeLaneHead(); got != "" {
		t.Fatalf("adeLaneHead() on the standalone env = %q, want \"\"", got)
	}

	// ...and it must NOT fall back to the process env, which the concurrent lanes raced.
	t.Setenv("PR_HEAD_SHA", "from-the-process-env")
	if got := std.adeLaneHead(); got != "" {
		t.Fatalf("adeLaneHead() must never read the process env; got %q", got)
	}

	// WHY the map read is load-bearing: the standalone resolver is the IDENTITY, so
	// resolveRefs returns the literal. If this ever stops holding, adeLaneHead's
	// rationale must be revisited deliberately — never silently kept.
	if got := std.resolveRefs("$env.PR_HEAD_SHA"); got != "$env.PR_HEAD_SHA" {
		t.Fatalf("standalone resolveRefs(\"$env.PR_HEAD_SHA\") = %q, want the literal — the identity resolver is exactly why adeLaneHead is a map read", got)
	}

	// The CALL SITE. Driving it behaviourally would need a real `charly` on PATH
	// (`runAdeStage` shells out to `charly check run`), and a unit test must never
	// stand a stub in for that boundary — "live or skip, never fake a live service".
	// So this half is structural on purpose: it fails on a re-introduction of the
	// literal-passing call, which the accessor assertions above cannot see.
	// Comments are stripped first: the doc comments above legitimately NAME the
	// forbidden forms to explain why they are forbidden.
	raw, err := os.ReadFile("verb_ade.go")
	if err != nil {
		t.Fatalf("read verb_ade.go: %v", err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	src := code.String()
	if !strings.Contains(src, "rc.adeLaneHead()") {
		t.Error("runAdeStage must take the lane head from rc.adeLaneHead()")
	}
	if strings.Contains(src, `resolveRefs("$env.PR_HEAD_SHA")`) {
		t.Error("runAdeStage must NOT pass resolveRefs(\"$env.PR_HEAD_SHA\") — the standalone resolver is the identity, so that is the literal")
	}
	if strings.Contains(src, `os.Getenv("PR_HEAD_SHA")`) {
		t.Error("runAdeStage must NOT read PR_HEAD_SHA from the process env — the concurrent lanes share it (RCA 2026.252.2210)")
	}
}
