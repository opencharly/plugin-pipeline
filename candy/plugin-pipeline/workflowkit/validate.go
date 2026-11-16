package workflowkit

// validate.go — the workflow IR validator. It is the ONE gate every engine (plugin-lobster
// today, any future engine) runs before lowering: the authored `kind: pipeline` body must
// be well-formed and self-consistent, so a workflow never fails halfway through a live run.
//
// WHAT IT JUDGES (P26 unit 2, opencharly/plugin-pipeline#66):
//
//   - every stage sets EXACTLY ONE arm (stageArm — the contract's four optional payload
//     fields, and "exactly one" is a Go rule there, so this file is its one implementation);
//   - every `after:` names a stage of THIS pipeline, and never the stage itself — an
//     ordering edge to nothing is a document defect, and silently dropping it would run a
//     consumer before its producer (the defect charly#1168 recorded);
//   - a `lane:` iterates a parameter the pipeline DECLARES (D-FLOW-1: "once per item of the
//     list a parameter holds"), so a lane over an undeclared word can never resolve;
//   - every duration the stage authors parses (the engine takes milliseconds; a bad
//     duration must fail here, not at 03:00 when the cron fires);
//   - the graph has no cycle (StageOrder, which the lowering also runs — a cycle is a load
//     error naming the stages in it).
//
// It returns a JOINED error naming every problem, never just the first, so an author fixes
// one round.
//
// WHAT IT NO LONGER JUDGES: the lobster step grammar (exec-arm XOR, unique step ids, loop
// body namespaces, the expression grammar on `when:`). Those fields are not authored any
// more — the lobster IR is what the LOWERING emits, and its own loader judges it.

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
)

// stageNameRe is the contract's #EntityRef grammar, which #StageName and #StageRef both
// are. A stage name outside it cannot be an `after:` target (the contract's own admission)
// and cannot name a generated charly entity.
var stageNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// onErrorWords is the contract's closed failure-policy vocabulary (#StageAlt.on_error).
var onErrorWords = map[string]bool{"fail": true, "continue": true}

// ValidatePipeline validates ONE authored pipeline: its stages, their arms, their ordering
// edges, their lanes and their durations, plus the graph itself (no cycle).
func ValidatePipeline(p *params.Pipeline) error {
	if p == nil {
		return errors.New("pipeline is nil")
	}
	var errs []error
	if len(p.Stage) == 0 {
		errs = append(errs, errors.New("stage: a pipeline declares at least one stage"))
	}

	for _, name := range StageNames(p) {
		st := p.Stage[name]
		where := fmt.Sprintf("stage %q", name)
		if !stageNameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: a stage name is an #EntityRef (lowercase words joined by single hyphens)", where))
		}
		if _, aerr := stageArm(st); aerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, aerr))
		}

		// --- ordering edges ---
		seenAfter := map[string]bool{}
		for _, ref := range st.After {
			producer := string(ref)
			if producer == name {
				errs = append(errs, fmt.Errorf("%s: after: names itself — a stage cannot run after itself", where))
				continue
			}
			if _, ok := p.Stage[producer]; !ok {
				errs = append(errs, fmt.Errorf("%s: after: %q names no stage of this pipeline", where, producer))
				continue
			}
			if seenAfter[producer] {
				errs = append(errs, fmt.Errorf("%s: after: %q is listed twice", where, producer))
			}
			seenAfter[producer] = true
		}

		// --- lane over a declared parameter ---
		if over := string(st.Lane.Over); over != "" {
			if _, ok := p.Args[over]; !ok {
				errs = append(errs, fmt.Errorf("%s: lane: over %q names no parameter — declare it under `args:`", where, over))
			}
		}

		// --- durations and the small closed sets ---
		for _, d := range []struct{ field, value string }{
			{"timeout", st.Timeout},
			{"retry.interval", st.Retry.Interval},
			{"retry.until", st.Retry.Until},
		} {
			if d.value == "" {
				continue
			}
			if _, derr := time.ParseDuration(d.value); derr != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %q is not a duration: %w", where, d.field, d.value, derr))
			}
		}
		if oe := stageOnError(st); !onErrorWords[oe] {
			errs = append(errs, fmt.Errorf("%s: on_error: %q is not one of fail, continue", where, oe))
		}
		if st.Concurrency < 0 {
			errs = append(errs, fmt.Errorf("%s: concurrency: %d must be at least 1", where, st.Concurrency))
		}
	}

	// The graph. A cycle is a load error, and it names the stages in it.
	if _, oerr := StageOrder(p.Stage); oerr != nil {
		errs = append(errs, oerr)
	}
	return errors.Join(errs...)
}

// StageNames returns the pipeline's stage names in declaration-independent (sorted) order:
// the map carries no order, and every reader that needs a stable list (a count, a
// diagnostic) must not re-derive one.
func StageNames(p *params.Pipeline) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Stage))
	for name := range p.Stage {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// StageSummary renders "N stages: a, b, c" for a CLI line, so the callers that print a
// pipeline's size agree on both the count and the words.
func StageSummary(p *params.Pipeline) string {
	names := StageNames(p)
	return fmt.Sprintf("%d stage%s: %s", len(names), plural(len(names)), strings.Join(names, ", "))
}

// plural is the one s the summary needs.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
