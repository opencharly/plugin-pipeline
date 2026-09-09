package pluginpipeline

// probes_extra.go — the plan probe set additions (§2.3): `artifact` (a required
// file present/nonempty/min-bytes) and `expect_exit` (assert a RECORDED exit code
// from a prior stage output) — generic verbs with structured input.

import (
	"fmt"
	"os"
)

func probeArtifact(input map[string]any) (bool, string) {
	path := s(input["path"])
	if path == "" {
		return false, "artifact: path required"
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("artifact: missing %s", path)
	}
	if want := i(input["min_bytes"]); want > 0 && st.Size() < int64(want) {
		return false, fmt.Sprintf("artifact: size %d < min %d", st.Size(), want)
	}
	return true, ""
}

func probeExpectExit(input map[string]any) (bool, string) {
	got := i(input["got"])
	want := i(input["want"])
	if want == 0 && input["want"] == nil {
		return false, "expect_exit: want required"
	}
	if got != want {
		return false, fmt.Sprintf("expect_exit: got %d, want %d", got, want)
	}
	return true, ""
}
