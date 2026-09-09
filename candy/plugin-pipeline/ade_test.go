package pluginpipeline

import "testing"

// TestAdeVerdictForExit pins the check-run exit contract mapping (the B12 test:
// this change's new behavior - FAIL without the mapping).
func TestAdeVerdictForExit(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{0, "PASS"},
		{2, "FAIL"},
		{3, "NO_VALIDATION"},
		{99, "NO_VALIDATION"},
	}
	for _, c := range cases {
		if got := adeVerdictForExit(c.code); got != c.want {
			t.Fatalf("adeVerdictForExit(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}
