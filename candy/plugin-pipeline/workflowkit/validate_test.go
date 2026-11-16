package workflowkit

import (
	"strings"
	"testing"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/spec/spec"
)

// validate_test.go — the stage gate. Every case is a document defect that must be named at
// LOAD, not discovered at 03:00 when the cron fires.

// validPipeline is the control every case starts from: it must validate clean, so a failure
// below is the case's mutation and not the fixture's noise.
func validPipeline() *params.Pipeline {
	return &params.Pipeline{
		Description: "valid fixture",
		Args:        map[string]spec.TaskParamSpec{"beds": {Description: "the beds"}},
		Stage: map[string]spec.Stage{
			"build": stepStage(charlyCmd("build it", `echo build`)),
			"check": ranAfter(stepStage(charlyCmd("check it", `echo check`)), "build"),
		},
	}
}

func TestValidateAcceptsTheControl(t *testing.T) {
	if err := ValidatePipeline(validPipeline()); err != nil {
		t.Fatalf("the control pipeline must validate clean: %v", err)
	}
}

func TestValidateRejectsNoStage(t *testing.T) {
	err := ValidatePipeline(&params.Pipeline{Description: "empty"})
	if err == nil || !strings.Contains(err.Error(), "at least one stage") {
		t.Fatalf("a pipeline with no stage must be refused: %v", err)
	}
}

func TestValidateRejectsArmCount(t *testing.T) {
	cases := map[string]struct {
		stage spec.Stage
		want  string
	}{
		"none": {spec.Stage{}, "sets no arm"},
		"two": {
			spec.Stage{
				Step:     spec.StageStep{Step: []spec.Step{charlyCmd("a", `echo a`)}},
				Ask:      spec.StageAsk{Prompt: "really?"},
				Pipeline: spec.StagePipeline{Pipeline: "other"},
			},
			"sets 3 arms",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPipeline()
			p.Stage["bad"] = tc.stage
			err := ValidatePipeline(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateRejectsBadAfter(t *testing.T) {
	p := validPipeline()
	p.Stage["orphan"] = ranAfter(stepStage(charlyCmd("x", `echo x`)), "ghost")
	err := ValidatePipeline(p)
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("an `after:` naming no stage must be refused by name: %v", err)
	}

	p = validPipeline()
	p.Stage["self"] = ranAfter(stepStage(charlyCmd("x", `echo x`)), "self")
	err = ValidatePipeline(p)
	if err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("a stage that runs after itself must be refused: %v", err)
	}

	p = validPipeline()
	p.Stage["twice"] = ranAfter(stepStage(charlyCmd("x", `echo x`)), "build", "build")
	err = ValidatePipeline(p)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a duplicated `after:` must be refused: %v", err)
	}
}

func TestValidateRejectsLaneOverUndeclaredParameter(t *testing.T) {
	p := validPipeline()
	st := stepStage(charlyCmd("x", `echo x`))
	st.Lane = spec.Lane{Over: "ghost"}
	p.Stage["lane"] = st
	err := ValidatePipeline(p)
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("a lane over an undeclared parameter must be refused by name: %v", err)
	}
}

func TestValidateRejectsBadDurations(t *testing.T) {
	p := validPipeline()
	st := stepStage(charlyCmd("x", `echo x`))
	st.Timeout = "soon"
	p.Stage["bad-timeout"] = st
	if err := ValidatePipeline(p); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("a bad timeout must be refused: %v", err)
	}

	p = validPipeline()
	st = stepStage(charlyCmd("x", `echo x`))
	st.Retry = spec.Retry{Attempt: 2, Interval: "eventually"}
	p.Stage["bad-interval"] = st
	if err := ValidatePipeline(p); err == nil || !strings.Contains(err.Error(), "retry.interval") {
		t.Fatalf("a bad retry interval must be refused: %v", err)
	}
}

func TestValidateRejectsBadStageName(t *testing.T) {
	p := validPipeline()
	p.Stage["Not A Name"] = stepStage(charlyCmd("x", `echo x`))
	if err := ValidatePipeline(p); err == nil || !strings.Contains(err.Error(), "#EntityRef") {
		t.Fatalf("a stage name outside #EntityRef must be refused: %v", err)
	}
}

// TestValidateJoinsEveryProblem pins the one-round contract: an author fixes the document
// once, so the gate names every defect it found rather than only the first.
func TestValidateJoinsEveryProblem(t *testing.T) {
	p := &params.Pipeline{Stage: map[string]spec.Stage{
		"nobody":   {},
		"ghostbed": ranAfter(stepStage(charlyCmd("x", `echo x`)), "ghost"),
	}}
	err := ValidatePipeline(p)
	if err == nil {
		t.Fatal("want refusals")
	}
	for _, want := range []string{"nobody", "ghostbed", "ghost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the joined error does not name %q: %v", want, err)
		}
	}
}
