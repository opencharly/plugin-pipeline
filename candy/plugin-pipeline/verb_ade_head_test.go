package pluginpipeline

import (
	"os"
	"strings"
	"testing"
)

// TestAdeLaneHeadIsARunContextReadNeverTheProcessEnv restores the lock the lifted cutover
// dropped (the former root_cause_fixes_test.go:TestAdeVerdictUsesTheLaneHeadNotTheProcessEnv).
//
// The ADE stage must hand its external check-run fallback the LANE's head sha. The
// predecessor read os.Getenv("PR_HEAD_SHA") — the operator's value or nothing, never
// this lane's — and the live run logged `--var PR_HEAD_SHA=` (RCA 2026.252.2210).
//
// The assertions below lock the ACCESSOR (the value source). The call site is locked
// separately, structurally, at the end of this test. The reason the ref form is wrong
// is no longer "the grammar is dead": the grammar is live and reads the PROCESS
// environment, which is exactly the source a per-lane head may not come from.
func TestAdeLaneHeadIsARunContextReadNeverTheProcessEnv(t *testing.T) {
	// A run context that BOUND the lane head — the per-run contract the retired
	// executor established (headSHA(pr, repo), an operator pin winning).
	bound := &verbEnv{env: map[string]string{"PR_NUMBER": "1", "PR_HEAD_SHA": "aaaa"}}
	if got := bound.adeLaneHead(); got != "aaaa" {
		t.Fatalf("adeLaneHead() = %q, want the run context's bound head %q", got, "aaaa")
	}

	// The standalone env — the ONLY production verbEnv — carries NO per-run map, so
	// the head is EMPTY. A missing run context is honest; the process environment's
	// value is a wrong answer that looks like a real one.
	std := standaloneVerbEnv(nil, t.TempDir())
	if got := std.adeLaneHead(); got != "" {
		t.Fatalf("adeLaneHead() on the standalone env = %q, want \"\"", got)
	}

	// ...and it must NOT fall back to the process env, which the concurrent lanes share.
	// `std` snapshots the environment at construction, so the variable is set first and
	// a SECOND constructor is built with it present — the read must still be "".
	t.Setenv("PR_HEAD_SHA", "from-the-process-env")
	live := standaloneVerbEnv(nil, t.TempDir())
	if got := live.adeLaneHead(); got != "" {
		t.Fatalf("adeLaneHead() must never read the process env; got %q", got)
	}

	// WHY the map read is load-bearing: the ref grammar is LIVE and reads the PROCESS
	// environment, so resolveRefs would hand the fallback the operator's value — a
	// string that looks like a head and is not this lane's. The identity resolver is
	// gone; the process environment is what makes the ref the wrong source. If this
	// ever stops holding, adeLaneHead's rationale must be revisited deliberately.
	if got := live.resolveRefs("$env.PR_HEAD_SHA"); got != "from-the-process-env" {
		t.Fatalf("standalone resolveRefs(\"$env.PR_HEAD_SHA\") = %q, want the PROCESS environment's %q — a live grammar over the shared process env is exactly why adeLaneHead is a run-context map read", got, "from-the-process-env")
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
		t.Error("runAdeStage must NOT pass resolveRefs(\"$env.PR_HEAD_SHA\") — the grammar reads the shared PROCESS environment, so that is the operator's value, not this lane's head")
	}
	if strings.Contains(src, `os.Getenv("PR_HEAD_SHA")`) {
		t.Error("runAdeStage must NOT read PR_HEAD_SHA from the process env — the concurrent lanes share it (RCA 2026.252.2210)")
	}
}
