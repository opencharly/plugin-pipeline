package pluginpipeline

import (
	"os"
	"strings"
	"testing"
)

// TestTriggeredFailIsNotFailHard: the redo-loop contract — a TRIGGERED fail
// (the corpus-gate's oracle-gap redo) is a REDO restart, never FAIL-HARD.
// The FAIL-HARD branch fires only when NO trigger is set. This test locks the
// branch condition: without it, the trigger handling below the FAIL-HARD
// check is unreachable and every probe fail ends the lane.
func TestTriggeredFailIsRedoNotFailHard(t *testing.T) {
	src, readErr := os.ReadFile("./executor.go")
	if readErr != nil {
		t.Fatalf("reading executor.go: %v", readErr)
	}
	code := string(src)
	// The FAIL-HARD branch must be guarded by the trigger-aware condition —
	// the corpus-gate redo depends on it.
	if !strings.Contains(code, "if err != nil && res.Trigger == \"\" {") {
		t.Fatal("the FAIL-HARD branch is not trigger-aware — a triggered probe fail ends the lane instead of redoing")
	}
}
