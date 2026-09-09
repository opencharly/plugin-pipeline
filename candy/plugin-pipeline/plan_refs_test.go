package pluginpipeline

import (
	"testing"
)

func TestEnvRefs(t *testing.T) {
	set := map[string]bool{}
	scanEnvRefs("default is $env.EVAL_CHANNEL and $env.PR_HEAD_SHA", set)
	if !set["EVAL_CHANNEL"] || !set["PR_HEAD_SHA"] {
		t.Fatalf("env refs not found: %v", set)
	}
	if set["OTHER"] {
		t.Fatal("unexpected ref")
	}
}
